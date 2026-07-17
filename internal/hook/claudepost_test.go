package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/ledger"
	"github.com/hoophq/julius/internal/tokens"
)

// TestMain isolates the session cache from the developer's real one.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "julius-hook-sessions-*")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("JULIUS_SESSION_DIR", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
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

func bashEvent(sid, command, stdout string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": sid, "tool_name": "Bash",
		"tool_input": map[string]any{"command": command},
		"tool_response": map[string]any{
			"stdout": stdout, "stderr": "", "interrupted": false,
			"isImage": false, "noOutputExpected": false,
		},
	})
	return string(b)
}

func readEvent(sid, path, content string, offset int) string {
	ti := map[string]any{"file_path": path}
	if offset > 0 {
		ti["offset"] = offset
	}
	b, _ := json.Marshal(map[string]any{
		"session_id": sid, "tool_name": "Read", "tool_input": ti,
		"tool_response": map[string]any{
			"type": "text",
			"file": map[string]any{
				"filePath": path, "content": content,
				"numLines": len(strings.Split(content, "\n")), "startLine": 1,
				"totalLines": len(strings.Split(content, "\n")),
			},
		},
	})
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
	runPost(t, readEvent(t.Name(), "/app/h.go", content, 0), nil)
	if u := runPost(t, readEvent(t.Name(), "/app/h.go", content, 10), nil); u != nil {
		t.Errorf("offset read must bypass dedup, got %v", u)
	}
	// and the bypass must not have poisoned the cache: full re-read still dedups
	if u := runPost(t, readEvent(t.Name(), "/app/h.go", content, 0), nil); u == nil {
		t.Error("full re-read after partial read must still dedup")
	}
}

func TestPostReadCrossSessionIsolated(t *testing.T) {
	content := bigFile("v1")
	runPost(t, readEvent(t.Name()+"-A", "/app/h.go", content, 0), nil)
	if u := runPost(t, readEvent(t.Name()+"-B", "/app/h.go", content, 0), nil); u != nil {
		t.Errorf("different session must not dedup, got %v", u)
	}
}

func TestPostBashIdenticalRerunDedups(t *testing.T) {
	out := verboseGoTest()
	first := runPost(t, bashEvent(t.Name(), "go test -v ./...", out), nil)
	if first == nil {
		t.Fatal("first run should compress via sniffer")
	}
	second := runPost(t, bashEvent(t.Name(), "go test -v ./...", out), nil)
	if second == nil {
		t.Fatal("identical re-run must dedup")
	}
	stdout := second["stdout"].(string)
	if !strings.Contains(stdout, "identical to the previous run") {
		t.Errorf("bad rerun marker: %q", stdout)
	}
	if got := tokens.Estimate(stdout); got >= 50 {
		t.Errorf("rerun marker costs %d tokens, want <50", got)
	}
}
