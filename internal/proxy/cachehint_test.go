package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// hintBody builds an Anthropic messages request comfortably above the
// size floor.
func hintBody(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`{"model":"claude-x","max_tokens":123456789,"messages":[{"role":"user","content":%q}]}`,
		strings.Repeat("stable prefix content. ", 300))
}

func TestCacheHintAddsTopLevelControl(t *testing.T) {
	h := NewCacheHinter([]string{"agent"})
	in := hintBody(t)

	out, changed := h.Request("anthropic", "/v1/messages", []byte(in))
	if !changed {
		t.Fatal("expected the hint to be added")
	}

	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	cc, ok := root["cache_control"].(map[string]any)
	if !ok || cc["type"] != "ephemeral" {
		t.Errorf("missing top-level cache_control: %v", root["cache_control"])
	}
	// Everything else must survive the round trip, numbers included.
	if !strings.Contains(string(out), `"max_tokens":123456789`) {
		t.Errorf("number did not round-trip losslessly: %s", out)
	}
	if root["model"] != "claude-x" || len(root["messages"].([]any)) != 1 {
		t.Errorf("body content altered: %s", out)
	}
}

func TestCacheHintBoundaries(t *testing.T) {
	h := NewCacheHinter([]string{"*"})
	big := hintBody(t)
	cases := []struct {
		name     string
		provider string
		path     string
		body     string
	}{
		{"openai untouched", "openai", "/v1/chat/completions", big},
		{"count_tokens untouched", "anthropic", "/v1/messages/count_tokens", big},
		{"small body untouched", "anthropic", "/v1/messages", `{"model":"m","messages":[]}`},
		{"existing cache_control untouched", "anthropic", "/v1/messages",
			strings.Replace(big, `"model"`, `"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],"model"`, 1)},
		{"invalid json untouched", "anthropic", "/v1/messages", strings.Repeat("not json ", 600)},
		{"no messages array untouched", "anthropic", "/v1/messages",
			fmt.Sprintf(`{"model":"m","prompt":%q}`, strings.Repeat("x", 5000))},
	}
	for _, c := range cases {
		out, changed := h.Request(c.provider, c.path, []byte(c.body))
		if changed || string(out) != c.body {
			t.Errorf("%s: body was modified", c.name)
		}
	}
}

func TestCacheAppsEnvParsing(t *testing.T) {
	t.Setenv("JULIUS_CACHE_APPS", " agent-a, ,agent-b ")
	if got := CacheApps(); len(got) != 2 || got[0] != "agent-a" || got[1] != "agent-b" {
		t.Errorf("CacheApps() = %v", got)
	}
	t.Setenv("JULIUS_CACHE_APPS", "")
	if got := CacheApps(); got != nil {
		t.Errorf("empty env must disable: %v", got)
	}
}

func TestCacheHinterEnablement(t *testing.T) {
	var nilHinter *CacheHinter
	if nilHinter.enabled("any") {
		t.Error("nil hinter must be disabled")
	}
	if h := NewCacheHinter([]string{"a"}); !h.enabled("a") || h.enabled("b") {
		t.Error("per-app enablement wrong")
	}
	if h := NewCacheHinter([]string{"*"}); !h.enabled("anything") {
		t.Error("wildcard must cover every app")
	}
}
