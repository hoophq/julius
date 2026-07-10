package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoophq/julius/internal/filter"
)

func testCompressor(t *testing.T, record SavingsRecorder) *Compressor {
	t.Helper()
	return NewCompressor([]string{"agent"}, filter.Load(t.TempDir()), record)
}

// repetitive builds a log-like blob the generic dedup can collapse.
func repetitive(n int) string {
	return strings.Repeat("building module cache entry\n", n) + "done"
}

func decodeBody(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	return root
}

func TestCompressAnthropicToolResultString(t *testing.T) {
	var gotTag string
	var got CompressSaving
	c := testCompressor(t, func(tag string, s CompressSaving) { gotTag, got = tag, s })

	noisy := repetitive(40)
	body := fmt.Sprintf(`{
		"model":"claude-opus-4-8","max_tokens":4096,"temperature":0.7,
		"system":"be terse",
		"messages":[
			{"role":"user","content":"run the tests"},
			{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"bash","input":{"command":"make build"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":%q}]}
		]}`, noisy)

	out, ok := c.Request("anthropic", "agent", []byte(body))
	if !ok {
		t.Fatal("expected compression to apply")
	}

	root := decodeBody(t, out)
	msgs := root["messages"].([]any)
	result := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	content := result["content"].(string)
	if !strings.Contains(content, "[julius: repeated 40×]") || len(content) >= len(noisy) {
		t.Errorf("tool_result not compressed: %q", content)
	}
	if root["system"] != "be terse" {
		t.Errorf("system mutated: %v", root["system"])
	}
	if msgs[0].(map[string]any)["content"] != "run the tests" {
		t.Errorf("user text mutated: %v", msgs[0])
	}
	toolUse := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if toolUse["input"].(map[string]any)["command"] != "make build" {
		t.Errorf("tool_use input mutated: %v", toolUse)
	}
	// numbers must survive re-serialization textually
	if !bytes.Contains(out, []byte(`"max_tokens":4096`)) || !bytes.Contains(out, []byte(`"temperature":0.7`)) {
		t.Errorf("numbers mangled in output: %s", out)
	}
	if gotTag != "agent" || got.Provider != "anthropic" || got.TokensBefore <= got.TokensAfter {
		t.Errorf("saving record wrong: tag=%q %+v", gotTag, got)
	}
}

func TestCompressAnthropicNeverTouchedShapes(t *testing.T) {
	c := testCompressor(t, nil)
	noisy := repetitive(40)
	// every compressible-looking text sits in a shape that must not be touched
	body := fmt.Sprintf(`{
		"model":"m","system":%[1]q,
		"messages":[
			{"role":"user","content":%[1]q},
			{"role":"user","content":[{"type":"text","text":%[1]q}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"bash","input":{"log":%[1]q}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","is_error":true,"content":%[1]q}]}
		]}`, noisy)

	out, ok := c.Request("anthropic", "agent", []byte(body))
	if ok || !bytes.Equal(out, []byte(body)) {
		t.Errorf("protected shapes were mutated (ok=%v)", ok)
	}
}

func TestCompressAnthropicNestedBlocks(t *testing.T) {
	c := testCompressor(t, nil)
	noisy := repetitive(40)
	imgData := strings.Repeat("A", 600)
	body := fmt.Sprintf(`{"model":"m","messages":[
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":[
			{"type":"text","text":%q},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}
		]}]}]}`, noisy, imgData)

	out, ok := c.Request("anthropic", "agent", []byte(body))
	if !ok {
		t.Fatal("expected compression to apply")
	}
	root := decodeBody(t, out)
	blocks := root["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].([]any)
	text := blocks[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "[julius: repeated 40×]") {
		t.Errorf("nested text block not compressed: %q", text)
	}
	img := blocks[1].(map[string]any)["source"].(map[string]any)
	if img["data"] != imgData {
		t.Error("image block mutated")
	}
}

func TestCompressOpenAIToolMessage(t *testing.T) {
	var got CompressSaving
	c := testCompressor(t, func(tag string, s CompressSaving) { got = s })
	noisy := repetitive(40)
	body := fmt.Sprintf(`{"model":"gpt-x","messages":[
		{"role":"system","content":"be terse"},
		{"role":"user","content":%[1]q},
		{"role":"tool","tool_call_id":"c1","content":%[1]q}
	]}`, noisy)

	out, ok := c.Request("openai", "agent", []byte(body))
	if !ok {
		t.Fatal("expected compression to apply")
	}
	root := decodeBody(t, out)
	msgs := root["messages"].([]any)
	if msgs[0].(map[string]any)["content"] != "be terse" {
		t.Error("system message mutated")
	}
	if msgs[1].(map[string]any)["content"] != noisy {
		t.Error("user message mutated")
	}
	tool := msgs[2].(map[string]any)["content"].(string)
	if !strings.Contains(tool, "[julius: repeated 40×]") {
		t.Errorf("tool message not compressed: %q", tool)
	}
	if got.Provider != "openai" || got.TokensBefore <= got.TokensAfter {
		t.Errorf("saving record wrong: %+v", got)
	}
}

func TestCompressMinSizeFloor(t *testing.T) {
	c := testCompressor(t, nil)
	small := repetitive(10) // collapsible, but under the floor
	if len(small) >= compressMinBytes {
		t.Fatalf("test input too large: %d bytes", len(small))
	}
	body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":%q}]}]}`, small)
	out, ok := c.Request("anthropic", "agent", []byte(body))
	if ok || !bytes.Equal(out, []byte(body)) {
		t.Errorf("content under the floor was mutated (ok=%v)", ok)
	}
}

func TestCompressPassthroughOnBadOrIdleBodies(t *testing.T) {
	c := testCompressor(t, nil)
	for name, body := range map[string]string{
		"invalid json":     `{"model":`,
		"trailing garbage": `{"model":"m"} {"x":1}`,
		"non-object":       `[1,2,3]`,
		"no messages":      `{"model":"m","messages":[]}`,
	} {
		out, ok := c.Request("anthropic", "agent", []byte(body))
		if ok || !bytes.Equal(out, []byte(body)) {
			t.Errorf("%s: body was not passed through verbatim (ok=%v)", name, ok)
		}
	}
}

func TestCompressSniffsFormatFilters(t *testing.T) {
	dir := t.TempDir()
	spec := `[filters.fakelog]
description = "test format"
command = '^fakelog$'
detect_output = ['^FAKELOG v1$']
drop_lines = ['^DEBUG ']
`
	if err := os.MkdirAll(filepath.Join(dir, ".julius"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".julius", "filters.toml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewCompressor([]string{"agent"}, filter.Load(dir), nil)

	// every DEBUG line is distinct: only the sniffed filter can shrink this
	var b strings.Builder
	b.WriteString("FAKELOG v1\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "DEBUG step %d completed\n", i)
	}
	b.WriteString("ERROR boom")
	body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":%q}]}]}`, b.String())

	out, ok := c.Request("anthropic", "agent", []byte(body))
	if !ok {
		t.Fatal("expected sniffed filter to apply")
	}
	root := decodeBody(t, out)
	content := root["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].(string)
	if content != "FAKELOG v1\nERROR boom" {
		t.Errorf("sniffed filter output = %q", content)
	}
}

func TestCompressAppsEnvParsing(t *testing.T) {
	t.Setenv("JULIUS_COMPRESS_APPS", " agent , billing ,")
	if got := CompressApps(); len(got) != 2 || got[0] != "agent" || got[1] != "billing" {
		t.Errorf("CompressApps() = %v", got)
	}
	t.Setenv("JULIUS_COMPRESS_APPS", "")
	if got := CompressApps(); got != nil {
		t.Errorf("empty env: CompressApps() = %v", got)
	}
}

func TestCompressorEnablement(t *testing.T) {
	var nilC *Compressor
	if nilC.enabled("agent") {
		t.Error("nil compressor must be disabled")
	}
	c := testCompressor(t, nil)
	if !c.enabled("agent") || c.enabled("other") {
		t.Error("per-app opt-in not honored")
	}
	all := NewCompressor([]string{"*"}, filter.Load(t.TempDir()), nil)
	if !all.enabled("anything") {
		t.Error("wildcard must cover every app")
	}
}
