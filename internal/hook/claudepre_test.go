package hook

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func routableGit(cmd string) bool { return strings.HasPrefix(cmd, "git ") }

func runHook(t *testing.T, input string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	ProcessPreToolUse(strings.NewReader(input), &out, routableGit)
	if out.Len() == 0 {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("hook emitted invalid JSON: %v\n%s", err, out.String())
	}
	return parsed
}

func hookOut(t *testing.T, parsed map[string]any) map[string]any {
	t.Helper()
	h, ok := parsed["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("missing hookSpecificOutput: %v", parsed)
	}
	return h
}

func TestRewriteEmitted(t *testing.T) {
	parsed := runHook(t, `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/nonexistent","tool_input":{"command":"git status"}}`)
	h := hookOut(t, parsed)
	if h["hookEventName"] != "PreToolUse" {
		t.Errorf("hookEventName = %v", h["hookEventName"])
	}
	ui := h["updatedInput"].(map[string]any)
	if ui["command"] != "julius git status" {
		t.Errorf("updatedInput.command = %v", ui["command"])
	}
	// no permission rules in /nonexistent → no auto-allow
	if _, has := h["permissionDecision"]; has {
		t.Errorf("unexpected permissionDecision without allow rules: %v", h)
	}
}

func TestNonRoutablePassthrough(t *testing.T) {
	if parsed := runHook(t, `{"tool_name":"Bash","cwd":"/","tool_input":{"command":"ls -la"}}`); parsed != nil {
		t.Errorf("non-routable command must produce no output, got %v", parsed)
	}
}

func TestNonBashIgnored(t *testing.T) {
	if parsed := runHook(t, `{"tool_name":"Read","cwd":"/","tool_input":{"command":"git status"}}`); parsed != nil {
		t.Errorf("non-Bash tool must be ignored, got %v", parsed)
	}
}

func TestMalformedInputSilent(t *testing.T) {
	if parsed := runHook(t, `{not json`); parsed != nil {
		t.Errorf("malformed input must be silent, got %v", parsed)
	}
	if parsed := runHook(t, ``); parsed != nil {
		t.Errorf("empty input must be silent, got %v", parsed)
	}
}

func TestAlreadyWrappedIdempotent(t *testing.T) {
	if parsed := runHook(t, `{"tool_name":"Bash","cwd":"/","tool_input":{"command":"julius git status"}}`); parsed != nil {
		t.Errorf("already-wrapped command must produce no output, got %v", parsed)
	}
}
