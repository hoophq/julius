package execx

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	out, err := Run([]string{"sh", "-c", "echo hi; echo err >&2; exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.Stdout) != "hi" || strings.TrimSpace(out.Stderr) != "err" || out.ExitCode != 3 {
		t.Errorf("got %+v", out)
	}
}

func TestRunMissingBinary(t *testing.T) {
	out, err := Run([]string{"definitely-not-a-real-binary-xyz"})
	if err == nil || out.ExitCode != 127 {
		t.Errorf("want err + 127, got %+v err=%v", out, err)
	}
}

func TestStashAndRotation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JULIUS_RAW_DIR", dir)

	// too small: skipped
	if hint := Stash("tiny", "git-status", time.Now()); hint != "" {
		t.Errorf("small output must not stash, got %q", hint)
	}

	big := strings.Repeat("x", 600)
	base := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	var lastHint string
	for i := 0; i < stashMaxFiles+5; i++ {
		lastHint = Stash(big, fmt.Sprintf("cmd-%02d", i), base.Add(time.Duration(i)*time.Second))
	}
	if !strings.HasPrefix(lastHint, "[julius] raw output: ") {
		t.Fatalf("bad hint: %q", lastHint)
	}
	path := strings.TrimPrefix(lastHint, "[julius] raw output: ")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != big {
		t.Fatalf("stash content mismatch: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != stashMaxFiles {
		t.Errorf("rotation kept %d files, want %d", len(entries), stashMaxFiles)
	}
	// oldest rotated away, newest kept
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	joined := strings.Join(names, ",")
	if strings.Contains(joined, "cmd-00") || !strings.Contains(joined, "cmd-24") {
		t.Errorf("rotation kept wrong files: %s", joined)
	}
}
