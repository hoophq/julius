package proxy

import (
	"bytes"
	"encoding/json"
)

// Usage is exact, provider-reported token consumption for one API call.
// Never estimated: if the provider didn't report it, it isn't recorded.
type Usage struct {
	Provider   string
	Model      string
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
}

// anthropic non-streaming response / message_start payload
type anthropicMessage struct {
	Model string `json:"model"`
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

type openaiResponse struct {
	Model string `json:"model"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// ParseJSONBody extracts usage from a non-streaming response body.
func ParseJSONBody(provider string, body []byte) (Usage, bool) {
	switch provider {
	case "anthropic":
		var m anthropicMessage
		if err := json.Unmarshal(body, &m); err != nil || (m.Usage.InputTokens == 0 && m.Usage.OutputTokens == 0) {
			return Usage{}, false
		}
		return Usage{
			Provider: provider, Model: m.Model,
			Input: m.Usage.InputTokens, Output: m.Usage.OutputTokens,
			CacheRead: m.Usage.CacheReadInputTokens, CacheWrite: m.Usage.CacheCreationInputTokens,
		}, true
	case "openai":
		var r openaiResponse
		if err := json.Unmarshal(body, &r); err != nil || r.Usage == nil {
			return Usage{}, false
		}
		u := Usage{
			Provider: provider, Model: r.Model,
			Input: r.Usage.PromptTokens, Output: r.Usage.CompletionTokens,
		}
		if r.Usage.PromptTokensDetails != nil {
			u.CacheRead = r.Usage.PromptTokensDetails.CachedTokens
		}
		return u, u.Input > 0 || u.Output > 0
	}
	return Usage{}, false
}

// sseAccumulator ingests a Server-Sent-Events byte stream (while it is being
// passed through verbatim) and accumulates provider-reported usage:
//
//	anthropic: message_start carries model + input/cache tokens;
//	           message_delta events carry cumulative output tokens
//	openai:    the final chunk carries usage when the caller opted in via
//	           stream_options.include_usage; model rides on every chunk
type sseAccumulator struct {
	provider string
	buf      bytes.Buffer
	usage    Usage
	seen     bool
}

func newSSEAccumulator(provider string) *sseAccumulator {
	return &sseAccumulator{provider: provider, usage: Usage{Provider: provider}}
}

// Feed consumes the next chunk of the response stream.
func (a *sseAccumulator) Feed(p []byte) {
	a.buf.Write(p)
	for {
		line, err := a.buf.ReadBytes('\n')
		if err != nil {
			// incomplete line: keep for the next chunk
			a.buf.Write(line)
			return
		}
		a.consumeLine(bytes.TrimRight(line, "\r\n"))
	}
}

func (a *sseAccumulator) consumeLine(line []byte) {
	data, ok := bytes.CutPrefix(line, []byte("data: "))
	if !ok || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	switch a.provider {
	case "anthropic":
		var ev struct {
			Type    string           `json:"type"`
			Message anthropicMessage `json:"message"`
			Usage   struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return
		}
		switch ev.Type {
		case "message_start":
			a.usage.Model = ev.Message.Model
			a.usage.Input = ev.Message.Usage.InputTokens
			a.usage.CacheRead = ev.Message.Usage.CacheReadInputTokens
			a.usage.CacheWrite = ev.Message.Usage.CacheCreationInputTokens
			a.usage.Output = ev.Message.Usage.OutputTokens
			a.seen = true
		case "message_delta":
			a.usage.Output = ev.Usage.OutputTokens // cumulative: last wins
			a.seen = true
		}
	case "openai":
		if u, ok := ParseJSONBody("openai", data); ok {
			u.Provider = a.provider
			a.usage = u
			a.seen = true
		} else {
			// keep the model name even from usage-less chunks
			var r openaiResponse
			if json.Unmarshal(data, &r) == nil && r.Model != "" && a.usage.Model == "" {
				a.usage.Model = r.Model
			}
		}
	}
}

// Result returns the accumulated usage, if any was reported.
func (a *sseAccumulator) Result() (Usage, bool) {
	return a.usage, a.seen && (a.usage.Input > 0 || a.usage.Output > 0)
}
