package proxy

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/tokens"
)

// compressMinBytes: tool results smaller than this stay untouched — short
// results are dense with signal and the savings would be noise.
const compressMinBytes = 500

// CompressSaving is the estimated effect of compressing one request body.
// Estimates, not provider-reported counts: they must never be recorded
// alongside exact api_calls usage.
type CompressSaving struct {
	Provider     string
	TokensBefore int
	TokensAfter  int
}

// SavingsRecorder persists one compression record; nil disables recording.
type SavingsRecorder func(appTag string, s CompressSaving)

// Compressor is the proxy's only payload mutation, strictly opt-in per
// app tag. Agents resend accumulated tool results on every turn, so
// shrinking them in the request path reduces provider-billed input
// tokens. Mutating third-party traffic demands hard boundaries — exactly
// two shapes are ever rewritten:
//
//	anthropic: {type:"tool_result"} blocks in user messages — string
//	           content and nested {type:"text"} blocks
//	openai:    {role:"tool"} messages with string content
//
// System prompts, user text, tool_use arguments, is_error results, and
// image/document blocks are never touched. A body that fails to parse as
// JSON is forwarded verbatim.
type Compressor struct {
	all    bool
	apps   map[string]bool
	list   []string
	reg    *filter.Registry
	record SavingsRecorder
}

// NewCompressor builds a Compressor covering the given app tags; the tag
// "*" covers every app.
func NewCompressor(apps []string, reg *filter.Registry, record SavingsRecorder) *Compressor {
	c := &Compressor{apps: map[string]bool{}, list: apps, reg: reg, record: record}
	for _, a := range apps {
		if a == "*" {
			c.all = true
		}
		c.apps[a] = true
	}
	return c
}

// CompressApps parses JULIUS_COMPRESS_APPS: a comma-separated list of app
// tags to compress, or "*" for all apps. Empty means disabled.
func CompressApps() []string {
	var out []string
	for _, p := range strings.Split(os.Getenv("JULIUS_COMPRESS_APPS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Scope describes which apps are covered, for startup output.
func (c *Compressor) Scope() string {
	if c.all {
		return "all apps"
	}
	return "apps: " + strings.Join(c.list, ", ")
}

func (c *Compressor) enabled(appTag string) bool {
	return c != nil && (c.all || c.apps[appTag])
}

// Request compresses tool-result content in a request body. It returns the
// rewritten body and true only when something was compressed; a parse
// failure or a body with nothing to compress returns the input verbatim.
func (c *Compressor) Request(provider, appTag string, body []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // numbers must round-trip losslessly through re-serialization
	var root map[string]any
	if err := dec.Decode(&root); err != nil || dec.More() {
		return body, false
	}

	s := CompressSaving{Provider: provider}
	msgs, _ := root["messages"].([]any)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		switch provider {
		case "anthropic":
			c.anthropicToolResults(msg, &s)
		case "openai":
			c.openaiToolMessage(msg, &s)
		}
	}
	if s.TokensBefore == 0 {
		return body, false
	}

	out, err := marshalBody(root)
	if err != nil {
		return body, false
	}
	if c.record != nil {
		c.record(appTag, s)
	}
	return out, true
}

// anthropicToolResults rewrites tool_result content inside one user
// message. Only string content and nested text blocks are candidates;
// is_error results and non-text blocks pass through.
func (c *Compressor) anthropicToolResults(msg map[string]any, s *CompressSaving) {
	if msg["role"] != "user" {
		return
	}
	blocks, _ := msg["content"].([]any)
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok || block["type"] != "tool_result" || block["is_error"] == true {
			continue
		}
		switch content := block["content"].(type) {
		case string:
			if out, ok := c.compressText(content, s); ok {
				block["content"] = out
			}
		case []any:
			for _, ib := range content {
				inner, ok := ib.(map[string]any)
				if !ok || inner["type"] != "text" {
					continue
				}
				if text, ok := inner["text"].(string); ok {
					if out, ok := c.compressText(text, s); ok {
						inner["text"] = out
					}
				}
			}
		}
	}
}

// openaiToolMessage rewrites the string content of one role:"tool"
// message. Array-form content is left untouched.
func (c *Compressor) openaiToolMessage(msg map[string]any, s *CompressSaving) {
	if msg["role"] != "tool" {
		return
	}
	if content, ok := msg["content"].(string); ok {
		if out, ok := c.compressText(content, s); ok {
			msg["content"] = out
		}
	}
}

// compressText runs one tool-result text through the filter engine:
// format sniffing first, generic line dedup otherwise. The compressed
// text is accepted only on a strict estimated-token win.
func (c *Compressor) compressText(text string, s *CompressSaving) (string, bool) {
	if len(text) < compressMinBytes {
		return "", false
	}
	var res filter.Result
	if spec := c.reg.Sniff(text); spec != nil {
		res = filter.Finalize(text, spec.Apply(text, 0))
	} else {
		res = filter.Finalize(text, filter.DedupRepeats(text))
	}
	if !res.Applied || res.Output == text {
		return "", false
	}
	before, after := tokens.Estimate(text), tokens.Estimate(res.Output)
	if after >= before {
		return "", false
	}
	s.TokensBefore += before
	s.TokensAfter += after
	return res.Output, true
}

// marshalBody re-serializes without HTML escaping — escaping would turn
// every <, >, and & in code-heavy payloads into a six-byte unicode escape
// for no wire benefit.
func marshalBody(root map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
