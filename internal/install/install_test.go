package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer

	if err := Init(false, PatchAuto, dir, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude", "settings.json")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// second run must change nothing
	if err := Init(false, PatchAuto, dir, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("init is not idempotent:\nfirst:  %s\nsecond: %s", first, second)
	}
	if got := strings.Count(string(second), HookCommand); got != 1 {
		t.Errorf("hook registered %d times, want exactly 1", got)
	}
}

func TestInitPreservesExistingSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{
  "permissions": { "deny": ["Bash(rm *)"] },
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "other-tool check" }] }
    ]
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Init(false, PatchAuto, dir, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("patched settings are invalid JSON: %v", err)
	}
	s := string(data)
	for _, needle := range []string{"other-tool check", "Bash(rm *)", HookCommand} {
		if !strings.Contains(s, needle) {
			t.Errorf("patched settings lost %q:\n%s", needle, s)
		}
	}
	// backup created
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("no .bak backup written: %v", err)
	}
}

func TestInitSkipModePrintsInstructions(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := Init(false, PatchSkip, dir, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("skip mode must not create settings")
	}
	if !strings.Contains(out.String(), HookCommand) {
		t.Errorf("instructions missing hook command: %s", out.String())
	}
}
