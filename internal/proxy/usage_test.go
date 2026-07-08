package proxy

import "testing"

func TestParseAnthropicNonStreaming(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-8","usage":{"input_tokens":1200,"output_tokens":350,"cache_read_input_tokens":800,"cache_creation_input_tokens":100}}`)
	u, ok := ParseJSONBody("anthropic", body)
	if !ok {
		t.Fatal("expected usage")
	}
	if u.Model != "claude-opus-4-8" || u.Input != 1200 || u.Output != 350 || u.CacheRead != 800 || u.CacheWrite != 100 {
		t.Errorf("got %+v", u)
	}
}

func TestParseOpenAINonStreaming(t *testing.T) {
	body := []byte(`{"model":"gpt-5","usage":{"prompt_tokens":900,"completion_tokens":210,"prompt_tokens_details":{"cached_tokens":640}}}`)
	u, ok := ParseJSONBody("openai", body)
	if !ok {
		t.Fatal("expected usage")
	}
	if u.Input != 900 || u.Output != 210 || u.CacheRead != 640 {
		t.Errorf("got %+v", u)
	}
}

func TestParseNoUsage(t *testing.T) {
	if _, ok := ParseJSONBody("anthropic", []byte(`{"type":"error"}`)); ok {
		t.Error("error body must not yield usage")
	}
	if _, ok := ParseJSONBody("openai", []byte(`{"model":"gpt-5"}`)); ok {
		t.Error("missing usage must not yield usage")
	}
}

func TestAnthropicSSEAccumulator(t *testing.T) {
	acc := newSSEAccumulator("anthropic")
	// deliver in awkward chunk boundaries to exercise line buffering
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"model":"claude-opus-4-8","usage":{"input_tokens":500,"output_tokens":1,"cache_read_input_tokens":400}}}` + "\n\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":42}}` + "\n\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":128}}` + "\n\n" +
		"data: [DONE]\n\n"
	half := len(stream) / 2
	acc.Feed([]byte(stream[:half]))
	acc.Feed([]byte(stream[half:]))

	u, ok := acc.Result()
	if !ok {
		t.Fatal("expected usage")
	}
	if u.Model != "claude-opus-4-8" || u.Input != 500 || u.CacheRead != 400 {
		t.Errorf("prompt usage wrong: %+v", u)
	}
	if u.Output != 128 {
		t.Errorf("output must be the last cumulative value, got %d", u.Output)
	}
}

func TestOpenAISSEAccumulator(t *testing.T) {
	acc := newSSEAccumulator("openai")
	stream := `data: {"model":"gpt-5","choices":[{"delta":{"content":"hi"}}]}` + "\n\n" +
		`data: {"model":"gpt-5","choices":[],"usage":{"prompt_tokens":300,"completion_tokens":80}}` + "\n\n" +
		"data: [DONE]\n\n"
	acc.Feed([]byte(stream))
	u, ok := acc.Result()
	if !ok {
		t.Fatal("expected usage")
	}
	if u.Model != "gpt-5" || u.Input != 300 || u.Output != 80 {
		t.Errorf("got %+v", u)
	}
}
