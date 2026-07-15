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

func TestScanBypassForms(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		// the julius binary invoked by path or under sudo counts as wrapped
		useLine("t1", "./julius go test ./..."),
		resultLine("t1", "ok"),
		useLine("t2", "sudo julius git status"),
		resultLine("t2", "On branch main"),
		// sudo'd and path-invoked routable commands are misses, not candidates
		useLine("t3", "sudo -E go test -v ./..."),
		resultLine("t3", verboseGoTest()),
		useLine("t4", "/usr/local/go/bin/go test -v ./..."),
		resultLine("t4", verboseGoTest()),
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Dir(dir, time.Now().Add(-time.Hour), filter.Load(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Wrapped != 2 {
		t.Errorf("wrapped = %d, want 2 (path- and sudo-invoked julius): %+v", rep.Wrapped, rep)
	}
	if len(rep.Missed) != 1 || rep.Missed[0].Command != "go-test" || rep.Missed[0].Runs != 2 {
		t.Errorf("sudo/path go test must classify as go-test misses: %+v", rep.Missed)
	}
	if len(rep.Candidates) != 0 {
		t.Errorf("no candidates expected: %+v", rep.Candidates)
	}
}

func TestScanChainAttribution(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		// chain output belongs to the command that produced it, not the leading cd
		useLine("t1", "cd /tmp/proj && ./server --check"),
		resultLine("t1", strings.Repeat("listening on :8080 request served in 12ms\n", 40)),
		// a chain of only silent builtins falls back to plain attribution
		useLine("t2", "cd /tmp/proj && export FOO=1"),
		resultLine("t2", "unexpected output"),
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Dir(dir, time.Now().Add(-time.Hour), filter.Load(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want 2", rep.Candidates)
	}
	if rep.Candidates[0].Family != "server" {
		t.Errorf("chain family = %q, want %q (attributed past the cd)", rep.Candidates[0].Family, "server")
	}
	if rep.Candidates[1].Family != "cd /tmp/proj" {
		t.Errorf("all-silent chain family = %q, want %q", rep.Candidates[1].Family, "cd /tmp/proj")
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
