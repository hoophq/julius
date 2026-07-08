package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

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
	SessionID    string          `json:"session_id"`
	ToolName     string          `json:"tool_name"`
	CWD          string          `json:"cwd"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolResponse map[string]any  `json:"tool_response"`
}

type postToolUseOutput struct {
	HookSpecificOutput struct {
		HookEventName     string         `json:"hookEventName"`
		UpdatedToolOutput map[string]any `json:"updatedToolOutput"`
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
	if err := json.NewDecoder(r).Decode(&in); err != nil || in.ToolResponse == nil {
		return
	}

	cache := session.Open(in.SessionID)
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
	file, _ := in.ToolResponse["file"].(map[string]any)
	content, _ := file["content"].(string)
	if len(content) < postMinBytes {
		return
	}

	key := "read:" + ti.FilePath
	prev, seen := cache.Load(key)
	cache.Store(key, []byte(content))
	if !seen {
		return
	}

	var replacement string
	switch {
	case string(prev) == content:
		replacement = fmt.Sprintf(
			"[julius] %s is unchanged since your last read in this session (%d lines) — the full content is already in context above. Re-read with offset/limit to force full output.",
			ti.FilePath, len(strings.Split(content, "\n")))
	default:
		d, ok := session.Diff(string(prev), content)
		newLines := len(strings.Split(content, "\n"))
		if !ok || len(strings.Split(d, "\n"))*10 > newLines*4 { // diff > 40% of file
			return
		}
		replacement = fmt.Sprintf(
			"[julius] %s changed since your last read — diff against the version in context above (-old/+new). Re-read with offset/limit to force full output.\n%s",
			ti.FilePath, d)
	}
	if tokens.Estimate(replacement) >= tokens.Estimate(content) {
		return
	}

	newFile := make(map[string]any, len(file))
	for k, v := range file {
		newFile[k] = v
	}
	newFile["content"] = replacement
	emit(w, in.ToolResponse, map[string]any{"file": newFile})
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

	stdout, _ := in.ToolResponse["stdout"].(string)
	if len(stdout) < postMinBytes {
		return
	}

	// Identical re-run of the same command in this session: the agent
	// already holds this exact output in context.
	key := "bash:" + ti.Command
	prev, seen := cache.Load(key)
	cache.Store(key, []byte(stdout))
	if seen && string(prev) == stdout {
		marker := fmt.Sprintf(
			"[julius] output is identical to the previous run of this command in this session (%d lines) — see the earlier result above.",
			len(strings.Split(stdout, "\n")))
		emit(w, in.ToolResponse, map[string]any{"stdout": marker})
		if rec != nil {
			rec(ledger.HookEvent{
				SessionID: in.SessionID, Kind: "session_dedup", Tool: "Bash", Command: ti.Command,
				TokensBefore: tokens.Estimate(stdout), TokensAfter: tokens.Estimate(marker),
			})
		}
		return
	}

	var res filter.Result
	if s := reg.Sniff(stdout); s != nil {
		res = filter.Finalize(stdout, s.Apply(stdout, 0))
	} else {
		res = filter.Finalize(stdout, filter.DedupRepeats(stdout))
	}
	if !res.Applied || res.Output == stdout {
		return
	}

	before, after := lineCounts(stdout, res.Output)
	compressed := res.Output + fmt.Sprintf("\n[julius] filtered: %d→%d lines", before, after)
	if tokens.Estimate(compressed) >= tokens.Estimate(stdout) {
		return
	}

	emit(w, in.ToolResponse, map[string]any{"stdout": compressed})
	if rec != nil {
		rec(ledger.HookEvent{
			SessionID: in.SessionID, Kind: "post_compress", Tool: "Bash", Command: ti.Command,
			TokensBefore: tokens.Estimate(stdout), TokensAfter: tokens.Estimate(compressed),
		})
	}
}

func processGrep(in postToolUseInput, w io.Writer, rec Recorder) {
	mode, _ := in.ToolResponse["mode"].(string)
	content, _ := in.ToolResponse["content"].(string)
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

	emit(w, in.ToolResponse, map[string]any{
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
	files, _ := in.ToolResponse["filenames"].([]any)
	if len(files) <= globMaxFiles {
		return
	}
	before := fmt.Sprintf("%v", files)
	capped := files[:globMaxFiles]

	emit(w, in.ToolResponse, map[string]any{
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
	var out postToolUseOutput
	out.HookSpecificOutput.HookEventName = "PostToolUse"
	out.HookSpecificOutput.UpdatedToolOutput = updated
	_ = json.NewEncoder(w).Encode(out)
}

func lineCounts(before, after string) (int, int) {
	return len(strings.Split(before, "\n")), len(strings.Split(after, "\n"))
}
