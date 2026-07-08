package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/ledger"
)

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

func bashEvent(command, stdout string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": "s1", "tool_name": "Bash",
		"tool_input": map[string]any{"command": command},
		"tool_response": map[string]any{
			"stdout": stdout, "stderr": "", "interrupted": false,
			"isImage": false, "noOutputExpected": false,
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
	updated := runPost(t, bashEvent("go test -v ./...", verboseGoTest()),
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
	if len(recorded) != 1 || recorded[0].Kind != "post_compress" || recorded[0].SessionID != "s1" {
		t.Errorf("ledger event wrong: %+v", recorded)
	}
	if recorded[0].TokensAfter >= recorded[0].TokensBefore {
		t.Errorf("no savings recorded: %+v", recorded[0])
	}
}

func TestPostBashSkipsJuliusWrapped(t *testing.T) {
	if u := runPost(t, bashEvent("julius go test -v ./...", verboseGoTest()), nil); u != nil {
		t.Errorf("wrapped command must be skipped, got %v", u)
	}
	if u := runPost(t, bashEvent("cd /x && julius go test -v ./...", verboseGoTest()), nil); u != nil {
		t.Errorf("chain with wrapped segment must be skipped, got %v", u)
	}
}

func TestPostBashSkipsSmallOutput(t *testing.T) {
	if u := runPost(t, bashEvent("go test ./...", "ok  \tpkg\t0.1s\n"), nil); u != nil {
		t.Errorf("small output must be skipped, got %v", u)
	}
}

func TestPostBashDedupFallback(t *testing.T) {
	noisy := strings.Repeat("WARN connection pool exhausted, retrying\n", 40) + "done\n"
	updated := runPost(t, bashEvent("./run-worker.sh", noisy), nil)
	if updated == nil {
		t.Fatal("expected dedup compression")
	}
	stdout := updated["stdout"].(string)
	if !strings.Contains(stdout, "[julius: repeated 40×]") {
		t.Errorf("missing dedup marker:\n%s", stdout)
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
		t.Errorf("Read must be untouched, got %v", u)
	}
}
