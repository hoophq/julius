package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hoophq/julius/internal/execx"
	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/ledger"
	"github.com/hoophq/julius/internal/router"
	"github.com/hoophq/julius/internal/session"
	"github.com/hoophq/julius/internal/tokens"
)

const (
	// postMinBytes: outputs smaller than this stay untouched — the
	// replacement marker would eat most of the win.
	postMinBytes = 500
	// grepMaxLines caps content-mode Grep results.
	grepMaxLines = 120
	// globMaxFiles caps Glob file lists.
	globMaxFiles = 100
)

type postToolUseInput struct {
	SessionID      string          `json:"session_id"`
	ToolUseID      string          `json:"tool_use_id"`
	AgentID        string          `json:"agent_id"`
	TranscriptPath string          `json:"transcript_path"`
	ToolName       string          `json:"tool_name"`
	CWD            string          `json:"cwd"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`

	// response is ToolResponse decoded as an object — the shape of every
	// native tool. MCP responses can be a bare content-block array instead
	// and are decoded separately in processMCP.
	response map[string]any
}

type postToolUseOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		UpdatedToolOutput any    `json:"updatedToolOutput"`
	} `json:"hookSpecificOutput"`
}

// Recorder persists a savings event; nil disables recording (tests).
type Recorder func(ledger.HookEvent)

// ProcessPostToolUse compresses native tool results via updatedToolOutput.
// Verified response shapes (captured live, 2026-07-08):
//
//	Bash: {stdout, stderr, interrupted, isImage, noOutputExpected}
//	Grep: {mode, filenames, numFiles} or +{content, numLines} in content mode
//	Glob: {filenames, durationMs, numFiles, truncated, totalMatches, countIsComplete}
//
// The full response map is echoed back with only the compressed fields
// changed, so unknown/extra fields always survive. Writing nothing means
// "no opinion". Read results are deliberately untouched: their content
// feeds exact-match edits downstream.
func ProcessPostToolUse(r io.Reader, w io.Writer, reg *filter.Registry, rec Recorder) {
	var in postToolUseInput
	if err := json.NewDecoder(r).Decode(&in); err != nil || len(in.ToolResponse) == 0 {
		return
	}

	if strings.HasPrefix(in.ToolName, "mcp__") {
		processMCP(in, w, rec)
		return
	}
	if err := json.Unmarshal(in.ToolResponse, &in.response); err != nil || in.response == nil {
		return
	}

	cache := session.Open(session.ScopeID(in.SessionID, in.AgentID, in.TranscriptPath))
	switch in.ToolName {
	case "Bash":
		processBash(in, w, reg, cache, rec)
	case "Grep":
		processGrep(in, w, rec)
	case "Glob":
		processGlob(in, w, rec)
	case "Read":
		processRead(in, w, cache, rec)
	}
	session.PurgeOld()
}

// processMCP compresses MCP tool results (tool names mcp__<server>__<tool>;
// these reach the hook only when the matcher was extended via
// `julius init --mcp`). Two response shapes are handled:
//
//	array:  [{"type":"text","text":"..."}]
//	object: {"content":[...blocks], "isError":bool}
//
// Only text blocks large enough to matter are rewritten, via CompactJSON —
// non-JSON text passes through, so there is no risk of mangling prose.
// Every other field and block survives verbatim, and error results are
// never touched: errors matter, keep them whole.
func processMCP(in postToolUseInput, w io.Writer, rec Recorder) {
	var blocks []any
	var wrapper map[string]any
	if err := json.Unmarshal(in.ToolResponse, &blocks); err != nil {
		if err := json.Unmarshal(in.ToolResponse, &wrapper); err != nil {
			return
		}
		if isErr, _ := wrapper["isError"].(bool); isErr {
			return
		}
		blocks, _ = wrapper["content"].([]any)
	}
	if len(blocks) == 0 {
		return
	}

	newBlocks := make([]any, len(blocks))
	var before, after int
	changed := false
	for i, b := range blocks {
		newBlocks[i] = b
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := block["type"].(string)
		text, _ := block["text"].(string)
		if typ != "text" || len(text) < postMinBytes {
			continue
		}
		res := filter.Finalize(text, filter.CompactJSON(text))
		if !res.Applied || res.Output == text {
			continue
		}
		nb := make(map[string]any, len(block))
		for k, v := range block {
			nb[k] = v
		}
		nb["text"] = res.Output
		newBlocks[i] = nb
		before += tokens.Estimate(text)
		after += tokens.Estimate(res.Output)
		changed = true
	}
	if !changed || after >= before {
		return
	}

	var updated any = newBlocks
	if wrapper != nil {
		nw := make(map[string]any, len(wrapper))
		for k, v := range wrapper {
			nw[k] = v
		}
		nw["content"] = newBlocks
		updated = nw
	}
	emitAny(w, updated)
	if rec != nil {
		rec(ledger.HookEvent{
			SessionID: in.SessionID, Kind: "post_compress", Tool: in.ToolName, Command: in.ToolName,
			TokensBefore: before, TokensAfter: after,
		})
	}
}

// processRead deduplicates repeated reads of the same file within a session.
// Fresh content is NEVER rewritten (it feeds exact-match edits); julius only
// acts when the agent already holds this exact content in context:
//
//	identical re-read → short marker
//	changed re-read   → compact diff against the version in context
//	                    (full content when the change is too large to diff
//	                    profitably)
//
// Partial reads (offset/limit) bypass dedup entirely and refresh nothing.
func processRead(in postToolUseInput, w io.Writer, cache *session.Cache, rec Recorder) {
	var ti struct {
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	_ = json.Unmarshal(in.ToolInput, &ti)
	if ti.Offset > 0 || ti.Limit > 0 || ti.FilePath == "" {
		return
	}
	file, _ := in.response["file"].(map[string]any)
	content, _ := file["content"].(string)
	if len(content) < postMinBytes {
		return
	}

	key := "read:" + ti.FilePath
	d := cache.Decide(key, []byte(content), in.ToolUseID)
	if d.SameEvent {
		// Duplicate invocation of the same hook event (settings.json and a
		// plugin can both register it): the first invocation's decision stands.
		return
	}
	verbatim := session.Entry{
		Content: []byte(content), Form: session.FormVerbatim,
		ToolUseID: in.ToolUseID, Time: time.Now(),
	}

	var replacement string
	form := session.FormVerbatim
	switch d.Verdict {
	case session.VerdictPass:
		cache.Commit(key, verbatim)
		return
	case session.VerdictSuppress:
		// Suppressing verbatim-seen content does not downgrade the form:
		// the referent from the earlier read is still in context.
		replacement = fmt.Sprintf(
			"[julius] %s is unchanged since your last read in this session (%d lines) — the full content is already in context above. Re-read with offset/limit to force full output.",
			ti.FilePath, len(strings.Split(content, "\n")))
	case session.VerdictDiff:
		diff, ok := session.Diff(string(d.Prev.Content), content)
		newLines := len(strings.Split(content, "\n"))
		if !ok || len(strings.Split(diff, "\n"))*10 > newLines*4 { // diff > 40% of file
			cache.Commit(key, verbatim)
			return
		}
		replacement = fmt.Sprintf(
			"[julius] %s changed since your last read — diff against the version in context above (-old/+new). Re-read with offset/limit to force full output.\n%s",
			ti.FilePath, diff)
		// FormDiff: the next identical read re-emits full content and
		// re-anchors the dedup chain on a verbatim referent.
		form = session.FormDiff
	}
	if tokens.Estimate(replacement) >= tokens.Estimate(content) {
		cache.Commit(key, verbatim)
		return
	}
	entry := verbatim
	entry.Form = form
	cache.Commit(key, entry)

	newFile := make(map[string]any, len(file))
	for k, v := range file {
		newFile[k] = v
	}
	newFile["content"] = replacement
	emit(w, in.response, map[string]any{"file": newFile})
	if rec != nil {
		rec(ledger.HookEvent{
			SessionID: in.SessionID, Kind: "session_dedup", Tool: "Read", Command: "read " + ti.FilePath,
			TokensBefore: tokens.Estimate(content), TokensAfter: tokens.Estimate(replacement),
		})
	}
}

func processBash(in postToolUseInput, w io.Writer, reg *filter.Registry, cache *session.Cache, rec Recorder) {
	var ti struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(in.ToolInput, &ti)

	// Double-processing guard: anything already routed through julius was
	// compressed at execution time by the wrapper.
	for _, p := range router.SplitChain(ti.Command) {
		if p.Text == "julius" || strings.HasPrefix(p.Text, "julius ") {
			return
		}
	}

	stdout, _ := in.response["stdout"].(string)
	if len(stdout) < postMinBytes {
		return
	}

	key := "bash:" + ti.Command
	d := cache.Decide(key, []byte(stdout), in.ToolUseID)
	if d.SameEvent {
		// Duplicate invocation of the same hook event (settings.json and a
		// plugin can both register it): the first invocation's decision stands.
		return
	}
	verbatim := session.Entry{
		Content: []byte(stdout), Form: session.FormVerbatim,
		ToolUseID: in.ToolUseID, Time: time.Now(),
	}

	// Identical re-run of a command whose output the agent saw verbatim:
	// suppress, but only with the raw output stashed on disk first — a
	// marker must never be a pointer-less dead end. Stash failure falls
	// through to the filter path. VerdictDiff has no Bash diff feature and
	// falls through as well.
	if d.Verdict == session.VerdictSuppress {
		fields := strings.Fields(ti.Command)
		if len(fields) > 2 {
			fields = fields[:2]
		}
		if hint := execx.Stash(stdout, strings.Join(fields, "-"), time.Now()); hint != "" {
			marker := fmt.Sprintf(
				"[julius] output is identical to the previous run of this command in this session (%d lines) — see the earlier result above.",
				len(strings.Split(stdout, "\n")))
			rawPath := strings.TrimPrefix(hint, "[julius] raw output: ")
			// The agent receives marker + pointer line; the ledger must
			// account for exactly what was emitted, not just the marker.
			emitted := marker + "\n" + hint
			emit(w, in.response, map[string]any{"stdout": emitted})
			entry := verbatim
			entry.StashPath = rawPath
			cache.Commit(key, entry)
			if rec != nil {
				rec(ledger.HookEvent{
					SessionID: in.SessionID, Kind: "session_dedup", Tool: "Bash", Command: ti.Command,
					TokensBefore: tokens.Estimate(stdout), TokensAfter: tokens.Estimate(emitted),
					RawPath: rawPath,
				})
			}
			return
		}
	}

	// JSON on stdout is compacted by shape and carries its own disclosure
	// marker, so it skips the generic line-count marker below. A compaction
	// Finalize rejects deliberately does NOT fall through to the branches
	// below: line-based dedup on a JSON document would corrupt it. Otherwise
	// a content-sniffed format filter runs, then repeated-line dedup.
	var res filter.Result
	jsonCompacted := false
	if j := filter.CompactJSON(stdout); j.Applied {
		res = filter.Finalize(stdout, j)
		jsonCompacted = true
	} else if s := reg.Sniff(stdout); s != nil {
		res = filter.Finalize(stdout, s.Apply(stdout, 0))
	} else {
		res = filter.Finalize(stdout, filter.DedupRepeats(stdout))
	}
	if !res.Applied || res.Output == stdout {
		cache.Commit(key, verbatim)
		return
	}

	compressed := res.Output
	if !jsonCompacted {
		before, after := lineCounts(stdout, res.Output)
		compressed += fmt.Sprintf("\n[julius] filtered: %d→%d lines", before, after)
	}
	if tokens.Estimate(compressed) >= tokens.Estimate(stdout) {
		cache.Commit(key, verbatim)
		return
	}

	entry := verbatim
	entry.Form = session.FormFiltered
	cache.Commit(key, entry)
	emit(w, in.response, map[string]any{"stdout": compressed})
	if rec != nil {
		rec(ledger.HookEvent{
			SessionID: in.SessionID, Kind: "post_compress", Tool: "Bash", Command: ti.Command,
			TokensBefore: tokens.Estimate(stdout), TokensAfter: tokens.Estimate(compressed),
		})
	}
}

func processGrep(in postToolUseInput, w io.Writer, rec Recorder) {
	mode, _ := in.response["mode"].(string)
	content, _ := in.response["content"].(string)
	if mode != "content" || len(content) < postMinBytes {
		return
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) <= grepMaxLines {
		return
	}
	omitted := len(lines) - grepMaxLines
	capped := append(lines[:grepMaxLines], fmt.Sprintf("[julius] +%d more match lines omitted", omitted))
	newContent := strings.Join(capped, "\n")

	emit(w, in.response, map[string]any{
		"content":  newContent,
		"numLines": len(capped),
	})
	if rec != nil {
		var ti struct {
			Pattern string `json:"pattern"`
		}
		_ = json.Unmarshal(in.ToolInput, &ti)
		rec(ledger.HookEvent{
			SessionID: in.SessionID, Kind: "post_compress", Tool: "Grep", Command: "grep " + ti.Pattern,
			TokensBefore: tokens.Estimate(content), TokensAfter: tokens.Estimate(newContent),
		})
	}
}

func processGlob(in postToolUseInput, w io.Writer, rec Recorder) {
	files, _ := in.response["filenames"].([]any)
	if len(files) <= globMaxFiles {
		return
	}
	before := fmt.Sprintf("%v", files)
	capped := files[:globMaxFiles]

	emit(w, in.response, map[string]any{
		"filenames": capped,
		"numFiles":  len(capped),
		"truncated": true,
	})
	if rec != nil {
		var ti struct {
			Pattern string `json:"pattern"`
		}
		_ = json.Unmarshal(in.ToolInput, &ti)
		rec(ledger.HookEvent{
			SessionID: in.SessionID, Kind: "post_compress", Tool: "Glob", Command: "glob " + ti.Pattern,
			TokensBefore: tokens.Estimate(before), TokensAfter: tokens.Estimate(fmt.Sprintf("%v", capped)),
		})
	}
}

// emit writes updatedToolOutput as the original response with overrides
// applied — extra fields the schema gains in the future pass through.
func emit(w io.Writer, response, overrides map[string]any) {
	updated := make(map[string]any, len(response))
	for k, v := range response {
		updated[k] = v
	}
	for k, v := range overrides {
		updated[k] = v
	}
	emitAny(w, updated)
}

// emitAny writes updatedToolOutput of any shape — MCP responses can be a
// bare content-block array, not an object.
func emitAny(w io.Writer, updated any) {
	var out postToolUseOutput
	out.HookSpecificOutput.HookEventName = "PostToolUse"
	out.HookSpecificOutput.UpdatedToolOutput = updated
	_ = json.NewEncoder(w).Encode(out)
}

func lineCounts(before, after string) (int, int) {
	return len(strings.Split(before, "\n")), len(strings.Split(after, "\n"))
}
