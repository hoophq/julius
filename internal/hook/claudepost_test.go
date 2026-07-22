package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/ledger"
	"github.com/hoophq/julius/internal/session"
	"github.com/hoophq/julius/internal/tokens"
)

// TestMain isolates the session cache and raw-output stash from the
// developer's real ones.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "julius-hook-sessions-*")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("JULIUS_SESSION_DIR", dir); err != nil {
		panic(err)
	}
	rawDir, err := os.MkdirTemp("", "julius-hook-raw-*")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("JULIUS_RAW_DIR", rawDir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	_ = os.RemoveAll(rawDir)
	os.Exit(code)
}

func runPost(t *testing.T, input string, rec Recorder) map[string]any {
	t.Helper()
	reg := filter.Load(t.TempDir()) // builtins only
	var out bytes.Buffer
	ProcessPostToolUse(strings.NewReader(input), &out, reg, rec)
	if out.Len() == 0 {
		return nil
	}
	var parsed struct {
		HookSpecificOutput struct {
			HookEventName     string         `json:"hookEventName"`
			UpdatedToolOutput map[string]any `json:"updatedToolOutput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid hook JSON: %v", err)
	}
	if parsed.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Fatalf("wrong event name: %s", parsed.HookSpecificOutput.HookEventName)
	}
	return parsed.HookSpecificOutput.UpdatedToolOutput
}

// bashEvent builds a legacy payload without tool_use_id — older Claude Code
// versions omit the field and dedup must still work for them.
func bashEvent(sid, command, stdout string) string {
	return bashEventID(sid, "", command, stdout)
}

func bashEventID(sid, toolUseID, command, stdout string) string {
	return bashEventAgent(sid, "", toolUseID, command, stdout)
}

func bashEventAgent(sid, agentID, toolUseID, command, stdout string) string {
	ev := map[string]any{
		"session_id": sid, "tool_name": "Bash",
		"tool_input": map[string]any{"command": command},
		"tool_response": map[string]any{
			"stdout": stdout, "stderr": "", "interrupted": false,
			"isImage": false, "noOutputExpected": false,
		},
	}
	if agentID != "" {
		ev["agent_id"] = agentID
	}
	if toolUseID != "" {
		ev["tool_use_id"] = toolUseID
	}
	b, _ := json.Marshal(ev)
	return string(b)
}

// readEvent builds a legacy payload without tool_use_id.
func readEvent(sid, path, content string, offset int) string {
	return readEventID(sid, "", path, content, offset)
}

func readEventID(sid, toolUseID, path, content string, offset int) string {
	return readEventAgent(sid, "", toolUseID, path, content, offset)
}

// readEventAgent mirrors the live payload split: main-context events omit
// agent_id, subagent events carry one alongside the parent's session_id
// and transcript_path.
func readEventAgent(sid, agentID, toolUseID, path, content string, offset int) string {
	ti := map[string]any{"file_path": path}
	if offset > 0 {
		ti["offset"] = offset
	}
	ev := map[string]any{
		"session_id": sid, "tool_name": "Read", "tool_input": ti,
		"tool_response": map[string]any{
			"type": "text",
			"file": map[string]any{
				"filePath": path, "content": content,
				"numLines": len(strings.Split(content, "\n")), "startLine": 1,
				"totalLines": len(strings.Split(content, "\n")),
			},
		},
	}
	if agentID != "" {
		ev["agent_id"] = agentID
	}
	if toolUseID != "" {
		ev["tool_use_id"] = toolUseID
	}
	b, _ := json.Marshal(ev)
	return string(b)
}

func verboseGoTest() string {
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&sb, "=== RUN   TestThing%02d\n--- PASS: TestThing%02d (0.01s)\n", i, i)
	}
	sb.WriteString("PASS\nok  \texample.com/pkg\t0.512s\n")
	return sb.String()
}

func TestPostBashSniffsTestOutput(t *testing.T) {
	var recorded []ledger.HookEvent
	updated := runPost(t, bashEvent(t.Name(), "go test -v ./...", verboseGoTest()),
		func(ev ledger.HookEvent) { recorded = append(recorded, ev) })
	if updated == nil {
		t.Fatal("expected compression, got no output")
	}
	stdout := updated["stdout"].(string)
	if !strings.Contains(stdout, "ok  \texample.com/pkg") || strings.Contains(stdout, "--- PASS") {
		t.Errorf("bad compression:\n%s", stdout)
	}
	if !strings.Contains(stdout, "[julius] filtered:") {
		t.Errorf("missing marker:\n%s", stdout)
	}
	// unknown fields preserved in the echoed shape
	if updated["noOutputExpected"] != false {
		t.Errorf("extra response fields must survive: %v", updated)
	}
	if len(recorded) != 1 || recorded[0].Kind != "post_compress" || recorded[0].SessionID != t.Name() {
		t.Errorf("ledger event wrong: %+v", recorded)
	}
	if recorded[0].TokensAfter >= recorded[0].TokensBefore {
		t.Errorf("no savings recorded: %+v", recorded[0])
	}
}

func TestPostBashSkipsJuliusWrapped(t *testing.T) {
	if u := runPost(t, bashEvent(t.Name(), "julius go test -v ./...", verboseGoTest()), nil); u != nil {
		t.Errorf("wrapped command must be skipped, got %v", u)
	}
	if u := runPost(t, bashEvent(t.Name(), "cd /x && julius go test -v ./...", verboseGoTest()), nil); u != nil {
		t.Errorf("chain with wrapped segment must be skipped, got %v", u)
	}
}

func TestPostBashSkipsSmallOutput(t *testing.T) {
	if u := runPost(t, bashEvent(t.Name(), "go test ./...", "ok  \tpkg\t0.1s\n"), nil); u != nil {
		t.Errorf("small output must be skipped, got %v", u)
	}
}

func TestPostBashDedupFallback(t *testing.T) {
	noisy := strings.Repeat("WARN connection pool exhausted, retrying\n", 40) + "done\n"
	updated := runPost(t, bashEvent(t.Name(), "./run-worker.sh", noisy), nil)
	if updated == nil {
		t.Fatal("expected dedup compression")
	}
	stdout := updated["stdout"].(string)
	if !strings.Contains(stdout, "[julius: repeated 40×]") {
		t.Errorf("missing dedup marker:\n%s", stdout)
	}
}

func TestPostBashCompactsJSON(t *testing.T) {
	// A JSON array payload from an unrecognized command (no builtin filter).
	items := make([]string, 40)
	for i := range items {
		items[i] = fmt.Sprintf(`{"id":"item-%02d","note":"padding text to make this worth compacting %d"}`, i, i)
	}
	payload := `{"items":[` + strings.Join(items, ",") + `]}`

	updated := runPost(t, bashEvent(t.Name(), "someapi list --json", payload), nil)
	if updated == nil {
		t.Fatal("expected JSON compaction")
	}
	stdout := updated["stdout"].(string)
	if !strings.Contains(stdout, "compacted JSON") {
		t.Errorf("missing JSON disclosure marker:\n%s", stdout)
	}
	if strings.Contains(stdout, "[julius] filtered:") {
		t.Errorf("JSON path must not also add the line-count marker:\n%s", stdout)
	}
	if strings.Contains(stdout, `"item-39"`) {
		t.Error("array items past the cap survived")
	}
	if !strings.Contains(stdout, `"item-00"`) {
		t.Error("array items within the cap were lost")
	}
}

func TestPostGrepContentCap(t *testing.T) {
	var lines []string
	for i := 0; i < 400; i++ {
		lines = append(lines, fmt.Sprintf("src/file%03d.go:12:some matching line %03d", i, i))
	}
	b, _ := json.Marshal(map[string]any{
		"session_id": "s1", "tool_name": "Grep",
		"tool_input": map[string]any{"pattern": "matching"},
		"tool_response": map[string]any{
			"mode": "content", "numFiles": 400, "filenames": []string{},
			"content": strings.Join(lines, "\n"), "numLines": 400,
		},
	})
	updated := runPost(t, string(b), nil)
	if updated == nil {
		t.Fatal("expected grep cap")
	}
	content := updated["content"].(string)
	got := strings.Split(content, "\n")
	if len(got) != 121 || !strings.Contains(got[120], "+280 more match lines omitted") {
		t.Errorf("bad cap: %d lines, tail %q", len(got), got[len(got)-1])
	}
	if updated["numLines"].(float64) != 121 {
		t.Errorf("numLines not updated: %v", updated["numLines"])
	}
}

func TestPostGrepFilesModeUntouched(t *testing.T) {
	b, _ := json.Marshal(map[string]any{
		"tool_name":  "Grep",
		"tool_input": map[string]any{"pattern": "x"},
		"tool_response": map[string]any{
			"mode": "files_with_matches", "filenames": []string{"a.go"}, "numFiles": 1,
		},
	})
	if u := runPost(t, string(b), nil); u != nil {
		t.Errorf("files_with_matches mode must pass through, got %v", u)
	}
}

func TestPostGlobCap(t *testing.T) {
	var files []string
	for i := 0; i < 250; i++ {
		files = append(files, fmt.Sprintf("src/pkg/file%03d.go", i))
	}
	b, _ := json.Marshal(map[string]any{
		"tool_name":  "Glob",
		"tool_input": map[string]any{"pattern": "**/*.go"},
		"tool_response": map[string]any{
			"filenames": files, "durationMs": 5, "numFiles": 250,
			"truncated": false, "totalMatches": 250, "countIsComplete": true,
		},
	})
	updated := runPost(t, string(b), nil)
	if updated == nil {
		t.Fatal("expected glob cap")
	}
	if got := len(updated["filenames"].([]any)); got != 100 {
		t.Errorf("filenames capped to %d, want 100", got)
	}
	if updated["truncated"] != true || updated["numFiles"].(float64) != 100 {
		t.Errorf("cap metadata wrong: %v", updated)
	}
	if updated["totalMatches"].(float64) != 250 {
		t.Errorf("totalMatches must stay honest: %v", updated["totalMatches"])
	}
}

func TestPostMalformedSilent(t *testing.T) {
	if u := runPost(t, `{broken`, nil); u != nil {
		t.Errorf("malformed input must be silent, got %v", u)
	}
	if u := runPost(t, `{"tool_name":"Read","tool_response":{"type":"text"}}`, nil); u != nil {
		t.Errorf("Read without file payload must be untouched, got %v", u)
	}
}

func bigFile(marker string) string {
	var sb strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&sb, "func Handler%02d(w http.ResponseWriter, r *http.Request) {} // %s\n", i, marker)
	}
	return sb.String()
}

func TestPostReadDedupUnchanged(t *testing.T) {
	content := bigFile("v1")
	var recorded []ledger.HookEvent
	rec := func(ev ledger.HookEvent) { recorded = append(recorded, ev) }

	if u := runPost(t, readEvent(t.Name(), "/app/handlers.go", content, 0), rec); u != nil {
		t.Fatalf("first read must pass through untouched, got %v", u)
	}
	updated := runPost(t, readEvent(t.Name(), "/app/handlers.go", content, 0), rec)
	if updated == nil {
		t.Fatal("identical re-read must dedup")
	}
	file := updated["file"].(map[string]any)
	replaced := file["content"].(string)
	if !strings.Contains(replaced, "unchanged since your last read") {
		t.Errorf("bad marker: %q", replaced)
	}
	if got := tokens.Estimate(replaced); got >= 50 {
		t.Errorf("marker costs %d tokens, acceptance is <50", got)
	}
	if file["filePath"] != "/app/handlers.go" {
		t.Errorf("file metadata must survive: %v", file)
	}
	if len(recorded) != 1 || recorded[0].Kind != "session_dedup" {
		t.Errorf("ledger event wrong: %+v", recorded)
	}
}

func TestPostReadDiffOnSmallChange(t *testing.T) {
	v1 := bigFile("v1")
	v2 := strings.Replace(v1, "Handler07", "RenamedHandler07", 1)

	runPost(t, readEvent(t.Name(), "/app/h.go", v1, 0), nil)
	updated := runPost(t, readEvent(t.Name(), "/app/h.go", v2, 0), nil)
	if updated == nil {
		t.Fatal("changed re-read must produce a diff")
	}
	content := updated["file"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "- func Handler07") || !strings.Contains(content, "+ func RenamedHandler07") {
		t.Errorf("diff missing change:\n%s", content)
	}
	if strings.Contains(content, "Handler05") {
		t.Errorf("diff leaked unchanged lines:\n%s", content)
	}
}

func TestPostReadFullContentOnBigChange(t *testing.T) {
	runPost(t, readEvent(t.Name(), "/app/h.go", bigFile("v1"), 0), nil)
	// a completely rewritten file: diff would exceed the 40% budget
	if u := runPost(t, readEvent(t.Name(), "/app/h.go", bigFile("totally-different"), 0), nil); u != nil {
		t.Errorf("large change must pass full content through, got %v", u)
	}
}

func TestPostReadPartialReadBypasses(t *testing.T) {
	content := bigFile("v1")
	if u := runPost(t, readEventID(t.Name(), "toolu_01", "/app/h.go", content, 0), nil); u != nil {
		t.Fatalf("first full read must pass through, got %v", u)
	}
	// The offset event carries the full content in its payload; the bypass
	// must neither rewrite the output nor touch the cached entry.
	if u := runPost(t, readEventID(t.Name(), "toolu_02", "/app/h.go", content, 10), nil); u != nil {
		t.Errorf("offset read must bypass dedup, got %v", u)
	}
	// Strongest observable proof the offset read neither poisoned nor reset
	// the entry: a full re-read under a fresh tool_use_id still suppresses
	// with an explicit unchanged marker against the toolu_01 referent.
	u := runPost(t, readEventID(t.Name(), "toolu_03", "/app/h.go", content, 0), nil)
	if u == nil {
		t.Fatal("full re-read after partial read must still dedup")
	}
	replaced := u["file"].(map[string]any)["content"].(string)
	if !strings.Contains(replaced, "unchanged since your last read") {
		t.Errorf("bad marker after partial-read bypass: %q", replaced)
	}
}

func TestPostReadCrossSessionIsolated(t *testing.T) {
	content := bigFile("v1")
	runPost(t, readEvent(t.Name()+"-A", "/app/h.go", content, 0), nil)
	if u := runPost(t, readEvent(t.Name()+"-B", "/app/h.go", content, 0), nil); u != nil {
		t.Errorf("different session must not dedup, got %v", u)
	}
}

func TestPostReadCrossAgentIsolated(t *testing.T) {
	content := bigFile("v1")
	sid := t.Name()

	// Parent reads the file; a subagent's first read of the same file under
	// the shared session_id must pass through untouched — the content was
	// never in the subagent's context.
	runPost(t, readEventAgent(sid, "", "toolu_01", "/app/h.go", content, 0), nil)
	if u := runPost(t, readEventAgent(sid, "a877c50488d25a006", "toolu_02", "/app/h.go", content, 0), nil); u != nil {
		t.Fatalf("subagent's first read must not dedup against the parent, got %v", u)
	}

	// Within its own context the subagent dedups normally.
	u := runPost(t, readEventAgent(sid, "a877c50488d25a006", "toolu_03", "/app/h.go", content, 0), nil)
	if u == nil {
		t.Fatal("subagent re-read of its own earlier read must dedup")
	}
	if replaced := u["file"].(map[string]any)["content"].(string); !strings.Contains(replaced, "unchanged since your last read") {
		t.Errorf("bad marker: %q", replaced)
	}

	// A second subagent is a third context: no dedup against parent or sibling.
	if u := runPost(t, readEventAgent(sid, "ac0377d08cbbb380a", "toolu_04", "/app/h.go", content, 0), nil); u != nil {
		t.Errorf("sibling subagent must not dedup against other contexts, got %v", u)
	}

	// And the parent's own re-read still dedups against its own first read,
	// untouched by the subagent activity in between.
	if u := runPost(t, readEventAgent(sid, "", "toolu_05", "/app/h.go", content, 0), nil); u == nil {
		t.Error("parent re-read must still dedup within the main context")
	}
}

func TestPostBashCrossAgentIsolated(t *testing.T) {
	out := uniqueProse()
	sid := t.Name()

	// Parent runs a command; a subagent running the identical command under
	// the shared session_id must get the real output, not a rerun marker
	// pointing at a result only the parent's context holds.
	runPost(t, bashEventAgent(sid, "", "toolu_01", "sensor-sweep --site charlie", out), nil)
	if u := runPost(t, bashEventAgent(sid, "a877c50488d25a006", "toolu_02", "sensor-sweep --site charlie", out), nil); u != nil {
		t.Fatalf("subagent's first run must not dedup against the parent, got %v", u)
	}

	// Within its own context the subagent's rerun dedups normally.
	u := runPost(t, bashEventAgent(sid, "a877c50488d25a006", "toolu_03", "sensor-sweep --site charlie", out), nil)
	if u == nil {
		t.Fatal("subagent rerun of its own earlier command must dedup")
	}
	if stdout := u["stdout"].(string); !strings.Contains(stdout, "identical to the previous run") {
		t.Errorf("bad marker: %q", stdout)
	}
}

// uniqueProse is ~600B that no filter path touches: unique lines defeat
// repeat-dedup, no sniffer pattern matches, and it is not JSON — so the
// Bash hook passes it through verbatim.
func uniqueProse() string {
	var sb strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&sb, "observation %02d: the tide gauge at buoy site charlie recorded a distinct reading today\n", i)
	}
	return sb.String()
}

func TestPostBashFilteredRerunFiltersAgain(t *testing.T) {
	// The first run entered context filtered, so a rerun marker claiming the
	// full output is "above" would lie. The rerun must filter again instead.
	out := verboseGoTest()
	first := runPost(t, bashEventID(t.Name(), "toolu_01", "go test -v ./...", out), nil)
	if first == nil {
		t.Fatal("first run should compress via sniffer")
	}
	second := runPost(t, bashEventID(t.Name(), "toolu_02", "go test -v ./...", out), nil)
	if second == nil {
		t.Fatal("rerun of a filtered command must filter again")
	}
	stdout := second["stdout"].(string)
	if strings.Contains(stdout, "identical to the previous run") {
		t.Errorf("dishonest marker against a filtered referent: %q", stdout)
	}
	if stdout != first["stdout"].(string) {
		t.Errorf("second output must be the filtered form again:\n%s", stdout)
	}
}

func TestPostBashRerunAfterVerbatimDedups(t *testing.T) {
	rawDir := t.TempDir()
	t.Setenv("JULIUS_RAW_DIR", rawDir)
	stdout := uniqueProse()
	var recorded []ledger.HookEvent
	rec := func(ev ledger.HookEvent) { recorded = append(recorded, ev) }

	if u := runPost(t, bashEventID(t.Name(), "toolu_01", "./survey.sh --deep", stdout), rec); u != nil {
		t.Fatalf("unique prose must pass through untouched, got %v", u)
	}
	u := runPost(t, bashEventID(t.Name(), "toolu_02", "./survey.sh --deep", stdout), rec)
	if u == nil {
		t.Fatal("rerun after a verbatim pass must dedup")
	}
	lines := strings.SplitN(u["stdout"].(string), "\n", 2)
	if !strings.Contains(lines[0], "identical to the previous run") {
		t.Errorf("bad rerun marker: %q", lines[0])
	}
	if got := tokens.Estimate(lines[0]); got >= 50 {
		t.Errorf("marker line costs %d tokens, want <50", got)
	}
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "[julius] raw output: ") {
		t.Fatalf("missing raw-output pointer line: %q", u["stdout"])
	}
	path := strings.TrimPrefix(lines[1], "[julius] raw output: ")
	if !strings.HasPrefix(path, rawDir) {
		t.Errorf("stash landed outside the seam dir: %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("stash file unreadable: %v", err)
	}
	if string(data) != stdout {
		t.Errorf("stash content differs from raw stdout (%d vs %d bytes)", len(data), len(stdout))
	}
	if len(recorded) != 1 || recorded[0].Kind != "session_dedup" || recorded[0].RawPath != path {
		t.Errorf("ledger event wrong: %+v", recorded)
	}
	// TokensAfter must account for the full emitted string (marker AND
	// pointer line), not just the marker.
	if want := tokens.Estimate(u["stdout"].(string)); recorded[0].TokensAfter != want {
		t.Errorf("TokensAfter = %d, want %d (estimate of the emitted stdout)", recorded[0].TokensAfter, want)
	}

	// Re-delivery of the marker-emitting event itself must stay silent: the
	// suppress commit recorded toolu_02, so this is SameEvent, and the entry
	// must NOT have been downgraded from verbatim by the marker emission.
	if u := runPost(t, bashEventID(t.Name(), "toolu_02", "./survey.sh --deep", stdout), rec); u != nil {
		t.Fatalf("re-delivery of the marker-emitting event must stay silent, got %v", u)
	}
	if len(recorded) != 1 {
		t.Fatalf("silent re-delivery must not record a ledger event: %+v", recorded)
	}

	// Third genuine rerun: the dedup chain must survive its own marker — the
	// suppress commit keeps the verbatim form, so toolu_03 suppresses again
	// with a fresh, valid raw-output pointer.
	u3 := runPost(t, bashEventID(t.Name(), "toolu_03", "./survey.sh --deep", stdout), rec)
	if u3 == nil {
		t.Fatal("third rerun must dedup again — the marker commit must not downgrade the verbatim form")
	}
	lines3 := strings.SplitN(u3["stdout"].(string), "\n", 2)
	if !strings.Contains(lines3[0], "identical to the previous run") {
		t.Errorf("bad second rerun marker: %q", lines3[0])
	}
	if len(lines3) != 2 || !strings.HasPrefix(lines3[1], "[julius] raw output: ") {
		t.Fatalf("second marker missing raw-output pointer line: %q", u3["stdout"])
	}
	path3 := strings.TrimPrefix(lines3[1], "[julius] raw output: ")
	if !strings.HasPrefix(path3, rawDir) {
		t.Errorf("second stash landed outside the seam dir: %q", path3)
	}
	data3, err := os.ReadFile(path3)
	if err != nil {
		t.Fatalf("second stash file unreadable: %v", err)
	}
	if string(data3) != stdout {
		t.Errorf("second stash content differs from raw stdout (%d vs %d bytes)", len(data3), len(stdout))
	}
	if len(recorded) != 2 || recorded[1].Kind != "session_dedup" || recorded[1].RawPath != path3 {
		t.Errorf("second ledger event wrong: %+v", recorded)
	}
	if want := tokens.Estimate(u3["stdout"].(string)); recorded[1].TokensAfter != want {
		t.Errorf("second TokensAfter = %d, want %d (estimate of the emitted stdout)", recorded[1].TokensAfter, want)
	}
}

func TestPostBashSameEventDoubleInvocationSilent(t *testing.T) {
	t.Setenv("JULIUS_RAW_DIR", t.TempDir())
	stdout := uniqueProse()
	ev := bashEventID(t.Name(), "toolu_01", "./survey.sh", stdout)
	if u := runPost(t, ev, nil); u != nil {
		t.Fatalf("first invocation must pass through, got %v", u)
	}
	if u := runPost(t, ev, nil); u != nil {
		t.Fatalf("duplicate invocation of the same event must stay silent, got %v", u)
	}
	// a genuine rerun (new tool_use_id, same output) must still dedup
	u := runPost(t, bashEventID(t.Name(), "toolu_02", "./survey.sh", stdout), nil)
	if u == nil {
		t.Fatal("genuine rerun after the double invocation must dedup")
	}
	if !strings.Contains(u["stdout"].(string), "identical to the previous run") {
		t.Errorf("bad rerun marker: %q", u["stdout"])
	}
}

func TestPostReadSameEventDoubleInvocationSilent(t *testing.T) {
	content := bigFile("v1")
	ev := readEventID(t.Name(), "toolu_01", "/app/h.go", content, 0)
	if u := runPost(t, ev, nil); u != nil {
		t.Fatalf("first invocation must pass through, got %v", u)
	}
	if u := runPost(t, ev, nil); u != nil {
		t.Fatalf("duplicate invocation of the same event must stay silent, got %v", u)
	}
	// a genuine re-read (new tool_use_id, same content) must still dedup
	u := runPost(t, readEventID(t.Name(), "toolu_02", "/app/h.go", content, 0), nil)
	if u == nil {
		t.Fatal("genuine re-read after the double invocation must dedup")
	}
	replaced := u["file"].(map[string]any)["content"].(string)
	if !strings.Contains(replaced, "unchanged since your last read") {
		t.Errorf("bad marker: %q", replaced)
	}
}

func TestPostReadChangedFileUnderDoubleInvocationNeverUnchanged(t *testing.T) {
	v1 := bigFile("v1")
	v2 := strings.Replace(v1, "Handler07", "RenamedHandler07", 1)
	e1 := readEventID(t.Name(), "toolu_e1", "/app/h.go", v1, 0)
	e2 := readEventID(t.Name(), "toolu_e2", "/app/h.go", v2, 0)

	runPost(t, e1, nil)
	runPost(t, e1, nil)
	// first invocation of the changed-file event must deliver the diff
	u := runPost(t, e2, nil)
	if u == nil {
		t.Fatal("first invocation of the changed-file event must diff")
	}
	content := u["file"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "changed since your last read") ||
		strings.Contains(content, "unchanged since your last read") {
		t.Errorf("first invocation must deliver a diff, not an unchanged marker:\n%s", content)
	}
	// second invocation of the same event must stay silent — never a marker
	// claiming the changed content is unchanged
	if u := runPost(t, e2, nil); u != nil {
		t.Errorf("duplicate invocation of the changed-file event must stay silent, got %v", u)
	}
}

func TestPostBashDedupStashFailureFallsThrough(t *testing.T) {
	// A path under a regular file makes every stash MkdirAll fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JULIUS_RAW_DIR", filepath.Join(blocker, "raw"))

	stdout := uniqueProse()
	runPost(t, bashEventID(t.Name(), "toolu_01", "./survey.sh", stdout), nil)
	u := runPost(t, bashEventID(t.Name(), "toolu_02", "./survey.sh", stdout), nil)
	if u == nil {
		return // fell through to the filter path, which had nothing to do
	}
	if s := u["stdout"].(string); strings.Contains(s, "identical to the previous run") {
		t.Errorf("suppression without a stash is a dead end: %q", s)
	}
}

func TestPostBashLegacyEntryNeverSuppresses(t *testing.T) {
	t.Setenv("JULIUS_RAW_DIR", t.TempDir())
	stdout := uniqueProse()
	// pre-seed a pre-migration raw entry
	session.Open(t.Name()).Store("bash:./survey.sh", []byte(stdout))

	if u := runPost(t, bashEventID(t.Name(), "toolu_01", "./survey.sh", stdout), nil); u != nil {
		t.Fatalf("legacy entry must never justify a marker, got %v", u)
	}
	// the delivery above upgraded the entry; a rerun now dedups honestly
	u := runPost(t, bashEventID(t.Name(), "toolu_02", "./survey.sh", stdout), nil)
	if u == nil || !strings.Contains(u["stdout"].(string), "identical to the previous run") {
		t.Fatalf("upgraded entry must dedup on the next rerun, got %v", u)
	}
}

func TestPostReadLegacyEntryNeverSuppresses(t *testing.T) {
	content := bigFile("v1")
	session.Open(t.Name()).Store("read:/app/h.go", []byte(content))

	if u := runPost(t, readEventID(t.Name(), "toolu_01", "/app/h.go", content, 0), nil); u != nil {
		t.Fatalf("legacy entry must never justify a marker, got %v", u)
	}
	u := runPost(t, readEventID(t.Name(), "toolu_02", "/app/h.go", content, 0), nil)
	if u == nil {
		t.Fatal("upgraded entry must dedup on the next re-read")
	}
	replaced := u["file"].(map[string]any)["content"].(string)
	if !strings.Contains(replaced, "unchanged since your last read") {
		t.Errorf("bad marker: %q", replaced)
	}
}

func TestPostBashLegacyEntryChangedContentPassesThrough(t *testing.T) {
	t.Setenv("JULIUS_RAW_DIR", t.TempDir())
	oldOut := uniqueProse()
	newOut := strings.ReplaceAll(oldOut, "buoy site charlie", "buoy site delta")
	// pre-seed a pre-migration raw entry with DIFFERENT content
	session.Open(t.Name()).Store("bash:./survey.sh", []byte(oldOut))

	// unknown provenance + changed content: no diff, no marker — the fresh
	// output must reach the agent whole
	if u := runPost(t, bashEventID(t.Name(), "toolu_01", "./survey.sh", newOut), nil); u != nil {
		t.Fatalf("changed content against a legacy entry must pass through whole, got %v", u)
	}
	// the delivery upgraded the entry to verbatim newOut; a rerun dedups
	u := runPost(t, bashEventID(t.Name(), "toolu_02", "./survey.sh", newOut), nil)
	if u == nil || !strings.Contains(u["stdout"].(string), "identical to the previous run") {
		t.Fatalf("upgraded entry must dedup on the next rerun, got %v", u)
	}
}

func TestPostReadLegacyEntryChangedContentPassesThrough(t *testing.T) {
	v1 := bigFile("v1")
	// a small change that WOULD be a profitable diff against a trusted referent
	v2 := strings.Replace(v1, "Handler07", "RenamedHandler07", 1)
	session.Open(t.Name()).Store("read:/app/h.go", []byte(v1))

	if u := runPost(t, readEventID(t.Name(), "toolu_01", "/app/h.go", v2, 0), nil); u != nil {
		t.Fatalf("changed content against a legacy entry must pass through whole (no diff, no marker), got %v", u)
	}
	// the delivery upgraded the entry to verbatim v2; a re-read dedups
	u := runPost(t, readEventID(t.Name(), "toolu_02", "/app/h.go", v2, 0), nil)
	if u == nil {
		t.Fatal("upgraded entry must dedup on the next re-read")
	}
	replaced := u["file"].(map[string]any)["content"].(string)
	if !strings.Contains(replaced, "unchanged since your last read") {
		t.Errorf("bad marker: %q", replaced)
	}
}

func TestPostBashSameEventDifferentContentSilent(t *testing.T) {
	t.Setenv("JULIUS_RAW_DIR", t.TempDir())
	if u := runPost(t, bashEventID(t.Name(), "toolu_01", "go test -v ./...", uniqueProse()), nil); u != nil {
		t.Fatalf("first invocation must pass through, got %v", u)
	}
	// A duplicate invocation of the SAME event must stay silent even if the
	// payload differs — SameEvent precedes any content comparison. The second
	// payload is deliberately filterable: if the SameEvent check were content-
	// gated, this would fall through to the sniffer and emit.
	if u := runPost(t, bashEventID(t.Name(), "toolu_01", "go test -v ./...", verboseGoTest()), nil); u != nil {
		t.Fatalf("duplicate invocation with different payload must stay silent, got %v", u)
	}
}

func TestPostReadNoMarkerAfterDiff(t *testing.T) {
	v1 := bigFile("v1")
	v2 := strings.Replace(v1, "Handler07", "RenamedHandler07", 1)

	if u := runPost(t, readEventID(t.Name(), "toolu_01", "/app/h.go", v1, 0), nil); u != nil {
		t.Fatalf("first read must pass through, got %v", u)
	}
	u := runPost(t, readEventID(t.Name(), "toolu_02", "/app/h.go", v2, 0), nil)
	if u == nil || !strings.Contains(u["file"].(map[string]any)["content"].(string), "changed since your last read") {
		t.Fatalf("changed re-read must diff, got %v", u)
	}
	// only a diff of v2 is in context, so the next identical read re-anchors
	// with full content instead of claiming it is above
	if u := runPost(t, readEventID(t.Name(), "toolu_03", "/app/h.go", v2, 0), nil); u != nil {
		t.Fatalf("read after a diff must re-emit full content, got %v", u)
	}
	// now v2 is in context verbatim; the fourth read may suppress
	u = runPost(t, readEventID(t.Name(), "toolu_04", "/app/h.go", v2, 0), nil)
	if u == nil {
		t.Fatal("read after the re-anchor must dedup")
	}
	replaced := u["file"].(map[string]any)["content"].(string)
	if !strings.Contains(replaced, "unchanged since your last read") {
		t.Errorf("bad marker: %q", replaced)
	}
	// and the marker commit must not downgrade the verbatim form: a fifth
	// identical read suppresses again
	u = runPost(t, readEventID(t.Name(), "toolu_05", "/app/h.go", v2, 0), nil)
	if u == nil {
		t.Fatal("fifth read must dedup again — the marker commit must not downgrade the verbatim form")
	}
	replaced = u["file"].(map[string]any)["content"].(string)
	if !strings.Contains(replaced, "unchanged since your last read") {
		t.Errorf("bad second marker: %q", replaced)
	}
}
