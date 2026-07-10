package proxy

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
)

// cacheHintMinBytes: requests below the provider's minimum cacheable
// prefix (~1024 tokens) gain nothing from a breakpoint; skip them rather
// than mutate tiny one-shot payloads.
const cacheHintMinBytes = 4096

// CacheHinter opts requests into Anthropic prompt caching, strictly per
// app tag. Agents resend the whole conversation every turn; a cache
// breakpoint turns that repeated prefix into 0.1×-priced cache reads
// instead of full-priced input tokens.
//
// The mutation is a single top-level field: cache_control
// {type: ephemeral}, Anthropic's auto-caching form — the server places
// the breakpoint on the last cacheable block, so julius never rewrites
// content blocks. Hard boundaries:
//
//   - anthropic requests with a messages array only; other providers and
//     other endpoints pass through untouched
//   - a body that already uses cache_control anywhere is the app managing
//     its own caching — never touched
//   - a body that fails to parse as JSON is forwarded verbatim
//
// No estimates are recorded: the effect shows up as provider-reported
// cache_read/cache_write tokens in the exact api_calls metering.
type CacheHinter struct {
	all  bool
	apps map[string]bool
	list []string
}

// NewCacheHinter builds a CacheHinter covering the given app tags; the
// tag "*" covers every app.
func NewCacheHinter(apps []string) *CacheHinter {
	h := &CacheHinter{apps: map[string]bool{}, list: apps}
	for _, a := range apps {
		if a == "*" {
			h.all = true
		}
		h.apps[a] = true
	}
	return h
}

// CacheApps parses JULIUS_CACHE_APPS: a comma-separated list of app tags
// to inject cache hints for, or "*" for all apps. Empty means disabled.
func CacheApps() []string {
	var out []string
	for _, p := range strings.Split(os.Getenv("JULIUS_CACHE_APPS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Scope describes which apps are covered, for startup output.
func (h *CacheHinter) Scope() string {
	if h.all {
		return "all apps"
	}
	return "apps: " + strings.Join(h.list, ", ")
}

func (h *CacheHinter) enabled(appTag string) bool {
	return h != nil && (h.all || h.apps[appTag])
}

// Request adds the cache hint to an Anthropic request body. path is the
// upstream path (e.g. "/v1/messages") — only the messages endpoint takes
// cache_control; count_tokens and friends pass through. It returns the
// rewritten body and true only when the hint was added; everything
// outside the boundaries above returns the input verbatim.
func (h *CacheHinter) Request(provider, path string, body []byte) ([]byte, bool) {
	if provider != "anthropic" || path != "/v1/messages" || len(body) < cacheHintMinBytes {
		return body, false
	}
	// Any existing cache_control means the app manages its own caching;
	// a second breakpoint could waste one of its four slots.
	if bytes.Contains(body, []byte(`"cache_control"`)) {
		return body, false
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // numbers must round-trip losslessly through re-serialization
	var root map[string]any
	if err := dec.Decode(&root); err != nil || dec.More() {
		return body, false
	}
	if _, ok := root["messages"].([]any); !ok {
		return body, false
	}

	root["cache_control"] = map[string]any{"type": "ephemeral"}
	out, err := marshalBody(root)
	if err != nil {
		return body, false
	}
	return out, true
}
