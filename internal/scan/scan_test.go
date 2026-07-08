package scan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoophq/julius/internal/filter"
)

func useLine(id, command string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{
				{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": command}},
			},
		},
	})
	return string(b)
}

func resultLine(id, stdout string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []map[string]any{
				{"type": "tool_result", "tool_use_id": id},
			},
		},
		"toolUseResult": map[string]any{"stdout": stdout},
	})
	return string(b)
}

func verboseGoTest() string {
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&sb, "=== RUN   TestX%02d\n--- PASS: TestX%02d (0.00s)\n", i, i)
	}
	sb.WriteString("PASS\nok  \tpkg\t0.2s\n")
	return sb.String()
}

func TestScanTranscript(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		// supported command, ran unwrapped, verbose output → measurable miss
		useLine("t1", "go test -v ./..."),
		resultLine("t1", verboseGoTest()),
		// already wrapped
		useLine("t2", "julius git status"),
		resultLine("t2", "On branch main"),
		// unsupported family with volume
		useLine("t3", "psql -c 'select * from users'"),
		resultLine("t3", strings.Repeat("row | data | values | here\n", 50)),
		// plain user chatter line that must not confuse the parser
		`{"type":"user","message":{"content":"just a human message"}}`,
		`not even json`,
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Dir(dir, time.Now().Add(-time.Hour), filter.Load(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sessions != 1 || rep.BashCommands != 3 || rep.Wrapped != 1 {
		t.Errorf("counts wrong: %+v", rep)
	}
	if len(rep.Missed) != 1 || rep.Missed[0].Command != "go-test" || rep.Missed[0].Saved() <= 0 {
		t.Errorf("missed wrong: %+v", rep.Missed)
	}
	if len(rep.Candidates) != 1 || rep.Candidates[0].Family != "psql" || rep.Candidates[0].Tokens == 0 {
		t.Errorf("candidates wrong: %+v", rep.Candidates)
	}
}

func TestScanWindowFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.jsonl")
	if err := os.WriteFile(path, []byte(useLine("t1", "go test ./...")), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	rep, err := Dir(dir, time.Now().Add(-24*time.Hour), filter.Load(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sessions != 0 {
		t.Errorf("old session must be outside the window: %+v", rep)
	}
}
