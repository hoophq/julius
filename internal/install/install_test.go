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
	hermeticEnv(t) // keep the plugin scan and env seams off the real machine
	dir := t.TempDir()
	var out bytes.Buffer

	if err := Init(false, PatchAuto, false, dir, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude", "settings.json")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// second run must change nothing
	if err := Init(false, PatchAuto, false, dir, strings.NewReader(""), &out); err != nil {
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
		t.Errorf("pre hook registered %d times, want exactly 1", got)
	}
	if got := strings.Count(string(second), PostHookCommand); got != 1 {
		t.Errorf("post hook registered %d times, want exactly 1", got)
	}
}

func TestInitPreservesExistingSettings(t *testing.T) {
	hermeticEnv(t)
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
	if err := Init(false, PatchAuto, false, dir, strings.NewReader(""), &out); err != nil {
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

func TestInitMCPUpgradesMatcherInPlace(t *testing.T) {
	hermeticEnv(t)
	dir := t.TempDir()
	var out bytes.Buffer

	if err := Init(false, PatchAuto, false, dir, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude", "settings.json")

	// --mcp on a base install upgrades the matcher without duplicating hooks
	if err := Init(false, PatchAuto, true, dir, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, PostHookMatcherMCP) {
		t.Errorf("matcher not upgraded to MCP variant:\n%s", s)
	}
	if got := strings.Count(s, PostHookCommand); got != 1 {
		t.Errorf("post hook registered %d times after upgrade, want exactly 1", got)
	}

	// a plain re-run must never downgrade the MCP matcher
	if err := Init(false, PatchAuto, false, dir, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), PostHookMatcherMCP) {
		t.Errorf("plain init downgraded the MCP matcher:\n%s", after)
	}
}

func TestInitSkipModePrintsInstructions(t *testing.T) {
	hermeticEnv(t)
	dir := t.TempDir()
	var out bytes.Buffer
	if err := Init(false, PatchSkip, false, dir, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("skip mode must not create settings")
	}
	if !strings.Contains(out.String(), HookCommand) {
		t.Errorf("instructions missing hook command: %s", out.String())
	}
	if strings.Contains(out.String(), "already runs julius hooks") {
		t.Errorf("plugin note printed with no plugin installed: %s", out.String())
	}
}

func TestInitWarnsWhenPluginAlreadyRegisters(t *testing.T) {
	home := hermeticEnv(t)
	writePluginFixture(t, home, "hoop@hooplabs")
	dir := t.TempDir()
	var out bytes.Buffer

	// Auto mode: note printed, install proceeds anyway.
	if err := Init(false, PatchAuto, false, dir, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	note := "note: the plugin hoop@hooplabs already runs julius hooks — installing into settings.json will duplicate the hook invocation (julius dedups events, but each call pays two hook round-trips)."
	if !strings.Contains(out.String(), note) {
		t.Errorf("auto-patch output missing plugin note:\n%s", out.String())
	}
	if !Installed(filepath.Join(dir, ".claude", "settings.json")) {
		t.Error("auto-patch must still install after the note")
	}

	// No-patch mode: note appended to the manual instructions.
	out.Reset()
	dir2 := t.TempDir()
	if err := Init(false, PatchSkip, false, dir2, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, note) {
		t.Errorf("no-patch output missing plugin note:\n%s", s)
	}
	if strings.Index(s, note) < strings.Index(s, HookCommand) {
		t.Errorf("note must follow the manual instructions:\n%s", s)
	}

	// Already-installed early return: the duplication note still prints —
	// this path is exactly the settings+plugin state doctor warns about.
	out.Reset()
	if err := Init(false, PatchAuto, false, dir, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	s = out.String()
	if !strings.Contains(s, "already installed") {
		t.Fatalf("expected the already-installed early return:\n%s", s)
	}
	if !strings.Contains(s, note) {
		t.Errorf("already-installed path must still print the plugin note:\n%s", s)
	}
}
