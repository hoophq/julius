package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type recorded struct {
	mu   sync.Mutex
	tag  string
	last Usage
	n    int
}

func (r *recorded) rec(tag string, u Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tag, r.last, r.n = tag, u, r.n+1
}

func TestProxyBufferedForwardsAndMeters(t *testing.T) {
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"claude-opus-4-8","usage":{"input_tokens":1000,"output_tokens":250}}`)
	}))
	defer upstream.Close()

	rec := &recorded{}
	srv := New(rec.rec)
	srv.upstreams["anthropic"] = upstream.URL
	front := httptest.NewServer(srv)
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/anthropic/v1/messages", strings.NewReader(`{"model":"claude-opus-4-8"}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set(appTagHeader, "billing-svc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !strings.Contains(string(body), "claude-opus-4-8") {
		t.Errorf("body not forwarded verbatim: %s", body)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("upstream path = %q, want /v1/messages", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth header not forwarded: %q", gotAuth)
	}
	if rec.n != 1 || rec.tag != "billing-svc" || rec.last.Input != 1000 || rec.last.Output != 250 {
		t.Errorf("metering wrong: n=%d tag=%s usage=%+v", rec.n, rec.tag, rec.last)
	}
}

func TestProxyStreamingPreservedAndMetered(t *testing.T) {
	chunks := []string{
		`data: {"type":"message_start","message":{"model":"claude-opus-4-8","usage":{"input_tokens":700,"output_tokens":1}}}` + "\n\n",
		`data: {"type":"message_delta","usage":{"output_tokens":55}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		for _, c := range chunks {
			_, _ = io.WriteString(w, c)
			fl.Flush()
		}
	}))
	defer upstream.Close()

	rec := &recorded{}
	srv := New(rec.rec)
	srv.upstreams["anthropic"] = upstream.URL
	front := httptest.NewServer(srv)
	defer front.Close()

	resp, err := http.Get(front.URL + "/anthropic/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type not preserved: %q", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), "[DONE]") || !strings.Contains(string(body), "message_start") {
		t.Errorf("stream body not forwarded verbatim: %s", body)
	}
	if rec.n != 1 || rec.last.Input != 700 || rec.last.Output != 55 {
		t.Errorf("streaming metering wrong: n=%d usage=%+v", rec.n, rec.last)
	}
}

func TestProxySequentialCallsAllMetered(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":10}}`)
	}))
	defer upstream.Close()

	rec := &recorded{}
	srv := New(rec.rec)
	srv.upstreams["anthropic"] = upstream.URL
	front := httptest.NewServer(srv)
	defer front.Close()

	for i := 0; i < 3; i++ {
		resp, err := http.Post(front.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.n != 3 {
		t.Errorf("3 sequential calls recorded %d times, want 3", rec.n)
	}
}

func TestProxyForwardsContentLength(t *testing.T) {
	var gotLen string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLen = r.Header.Get("Content-Length")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()

	srv := New(nil)
	srv.upstreams["anthropic"] = upstream.URL
	front := httptest.NewServer(srv)
	defer front.Close()

	payload := `{"model":"claude-opus-4-8","stream":true}`
	resp, err := http.Post(front.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotLen != fmt.Sprintf("%d", len(payload)) {
		t.Errorf("upstream Content-Length = %q, want %d (chunked forwarding breaks length-framed readers)", gotLen, len(payload))
	}
	if string(gotBody) != payload {
		t.Errorf("body not forwarded intact: %q", gotBody)
	}
}

func TestProxyDefaultAppTag(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"gpt-5","usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	defer upstream.Close()

	rec := &recorded{}
	srv := New(rec.rec)
	srv.upstreams["openai"] = upstream.URL
	front := httptest.NewServer(srv)
	defer front.Close()

	resp, err := http.Get(front.URL + "/openai/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if rec.tag != "default" {
		t.Errorf("untagged traffic must record as 'default', got %q", rec.tag)
	}
}

func TestProxyUnknownProvider(t *testing.T) {
	srv := New(nil)
	front := httptest.NewServer(srv)
	defer front.Close()
	resp, err := http.Get(front.URL + "/googlegemini/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown provider = %d, want 404", resp.StatusCode)
	}
}

func TestProxyErrorNotMetered(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error"}}`)
	}))
	defer upstream.Close()

	rec := &recorded{}
	srv := New(rec.rec)
	srv.upstreams["anthropic"] = upstream.URL
	front := httptest.NewServer(srv)
	defer front.Close()

	resp, err := http.Get(front.URL + "/anthropic/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 429 {
		t.Errorf("status not preserved: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if rec.n != 0 {
		t.Errorf("error responses must not be metered, got n=%d", rec.n)
	}
}
