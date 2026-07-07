package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSettings creates a project-level .claude/settings.json with the
// given permission rules so LoadRules picks them up via cwd. HOME is
// pointed at an empty directory so the developer's real global settings
// can't leak into the test.
func writeSettings(t *testing.T, dir string, perms map[string][]string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"permissions": perms})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func preInput(cwd, command string) string {
	return fmt.Sprintf(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":%q,"tool_input":{"command":%q}}`, cwd, command)
}

func TestAllowRuleAutoApproves(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, map[string][]string{"allow": {"Bash(git status)"}})

	var out bytes.Buffer
	ProcessPreToolUse(strings.NewReader(preInput(dir, "git status")), &out, routableGit)

	var parsed map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	h := parsed["hookSpecificOutput"].(map[string]any)
	if h["permissionDecision"] != "allow" {
		t.Errorf("allow rule must auto-approve, got %v", h)
	}
	if h["updatedInput"].(map[string]any)["command"] != "julius git status" {
		t.Errorf("bad rewrite: %v", h)
	}
}

func TestDenyRuleHandsOff(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, map[string][]string{"deny": {"Bash(git push:*)"}})

	var out bytes.Buffer
	ProcessPreToolUse(strings.NewReader(preInput(dir, "git push origin main")), &out, routableGit)
	if out.Len() != 0 {
		t.Errorf("deny rule must produce no output (hands off), got %s", out.String())
	}
}

func TestAskRuleRewritesWithoutApproval(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, map[string][]string{"ask": {"Bash(git commit:*)"}})

	var out bytes.Buffer
	ProcessPreToolUse(strings.NewReader(preInput(dir, "git commit -m x")), &out, routableGit)

	var parsed map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	h := parsed["hookSpecificOutput"].(map[string]any)
	if _, has := h["permissionDecision"]; has {
		t.Errorf("ask rule must not auto-approve, got %v", h)
	}
	if h["updatedInput"].(map[string]any)["command"] != "julius git commit -m x" {
		t.Errorf("bad rewrite: %v", h)
	}
}

func TestDenyOnOneChainSegmentHandsOffWholeChain(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, map[string][]string{
		"allow": {"Bash(git status)"},
		"deny":  {"Bash(git push:*)"},
	})

	var out bytes.Buffer
	ProcessPreToolUse(strings.NewReader(preInput(dir, "git status && git push")), &out, routableGit)
	if out.Len() != 0 {
		t.Errorf("chain containing denied segment must be untouched, got %s", out.String())
	}
}
