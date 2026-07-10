// Package proxy is the app/API interception surface: a local pass-through
// proxy for LLM provider traffic that records exact, provider-reported
// token usage. By default it never mutates payloads — requests and
// responses are forwarded verbatim, streaming included. Apps that opt in
// additionally get tool-result content compressed (see Compressor) and
// Anthropic prompt-cache hints injected (see CacheHinter) before the
// request reaches the provider.
//
// Apps opt in with zero code changes:
//
//	ANTHROPIC_BASE_URL=http://localhost:4141/anthropic
//	OPENAI_BASE_URL=http://localhost:4141/openai/v1
package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// appTagHeader lets callers label traffic per application; it is consumed
// by the proxy and never forwarded upstream.
const appTagHeader = "X-Julius-App"

// maxBufferedBody caps how much of a non-streaming response is buffered
// for usage parsing.
const maxBufferedBody = 32 << 20 // 32MB

// Recorder persists one usage record; nil disables recording.
type Recorder func(appTag string, u Usage)

// Server proxies provider traffic and meters it.
type Server struct {
	record    Recorder
	compress  *Compressor
	hints     *CacheHinter
	client    *http.Client
	upstreams map[string]string
}

// New builds a Server. Upstreams honor JULIUS_<PROVIDER>_UPSTREAM env
// overrides (integration tests point them at local fakes).
func New(record Recorder) *Server {
	up := map[string]string{
		"anthropic": "https://api.anthropic.com",
		"openai":    "https://api.openai.com",
	}
	if v := os.Getenv("JULIUS_ANTHROPIC_UPSTREAM"); v != "" {
		up["anthropic"] = v
	}
	if v := os.Getenv("JULIUS_OPENAI_UPSTREAM"); v != "" {
		up["openai"] = v
	}
	return &Server{
		record: record,
		// No global timeout: streaming responses legitimately run for
		// minutes. Dial/TLS failures still surface promptly.
		client:    &http.Client{Timeout: 0},
		upstreams: up,
	}
}

// ServeHTTP implements the /{provider}/{rest...} routing.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	provider, rest, ok := splitRoute(r.URL.Path)
	upstream, known := s.upstreams[provider]
	if !ok || !known {
		http.Error(w, "julius proxy routes: /anthropic/... and /openai/...", http.StatusNotFound)
		return
	}

	appTag := r.Header.Get(appTagHeader)
	if appTag == "" {
		appTag = "default"
	}

	url := upstream + rest
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	body, length := s.requestBody(r, provider, rest, appTag)
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, body)
	if err != nil {
		http.Error(w, "julius proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	// Preserve the declared body length: without it Go falls back to
	// chunked transfer encoding, which some upstreams and middleboxes
	// mishandle (a Content-Length reader sees an empty body).
	if length >= 0 {
		req.ContentLength = length
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Del(appTagHeader)

	resp, err := s.client.Do(req)
	if err != nil {
		http.Error(w, "julius proxy: upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		s.relayStream(w, resp.Body, provider, appTag)
		return
	}
	s.relayBuffered(w, resp.Body, provider, appTag, resp.StatusCode)
}

// EnableCompression turns on opt-in request compression (see Compressor);
// nil leaves the proxy fully pass-through.
func (s *Server) EnableCompression(c *Compressor) { s.compress = c }

// EnableCacheHints turns on opt-in cache-hint injection (see CacheHinter);
// nil leaves request bodies untouched.
func (s *Server) EnableCacheHints(h *CacheHinter) { s.hints = h }

// requestBody returns the body to forward upstream and its length (-1 for
// unknown). For apps opted into compression or cache hints, POST bodies
// are buffered, rewritten, and re-measured; everything else streams
// through untouched. A body too large to buffer is forwarded unmodified.
func (s *Server) requestBody(r *http.Request, provider, rest, appTag string) (io.Reader, int64) {
	compress, hint := s.compress.enabled(appTag), s.hints.enabled(appTag)
	if (!compress && !hint) || r.Method != http.MethodPost {
		return r.Body, r.ContentLength
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBufferedBody+1))
	if err != nil || len(data) > maxBufferedBody {
		return io.MultiReader(bytes.NewReader(data), r.Body), r.ContentLength
	}
	out := data
	if compress {
		out, _ = s.compress.Request(provider, appTag, out)
	}
	if hint {
		out, _ = s.hints.Request(provider, rest, out)
	}
	return bytes.NewReader(out), int64(len(out))
}

// relayStream forwards SSE bytes verbatim, flushing per chunk so streaming
// latency is preserved, while the accumulator watches for usage events.
func (s *Server) relayStream(w http.ResponseWriter, body io.Reader, provider, appTag string) {
	acc := newSSEAccumulator(provider)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			acc.Feed(chunk)
			if _, werr := w.Write(chunk); werr != nil {
				return // client went away; nothing to record reliably
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	if u, ok := acc.Result(); ok && s.record != nil {
		s.record(appTag, u)
	}
}

// relayBuffered forwards a regular response and parses usage from the body.
// A read error mid-body must not discard what already arrived: forward it,
// and if it parses as a complete usage-bearing payload, meter it — abrupt
// connection teardowns after a full body are common with close-delimited
// upstreams.
func (s *Server) relayBuffered(w http.ResponseWriter, body io.Reader, provider, appTag string, status int) {
	data, _ := io.ReadAll(io.LimitReader(body, maxBufferedBody))
	if len(data) > 0 {
		_, _ = w.Write(data)
	}
	if status < 200 || status >= 300 {
		return // errors carry no usage
	}
	if u, ok := ParseJSONBody(provider, data); ok && s.record != nil {
		s.record(appTag, u)
	}
}

// Serve runs the proxy on the given port until the listener fails.
// compress and hints may be nil (metering only).
func Serve(port int, record Recorder, compress *Compressor, hints *CacheHinter) error {
	handler := New(record)
	handler.EnableCompression(compress)
	handler.EnableCacheHints(hints)
	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("julius proxy listening on http://127.0.0.1:%d\n\n", port)
	fmt.Printf("point your apps at it (no code changes):\n")
	fmt.Printf("  export ANTHROPIC_BASE_URL=http://127.0.0.1:%d/anthropic\n", port)
	fmt.Printf("  export OPENAI_BASE_URL=http://127.0.0.1:%d/openai/v1\n\n", port)
	if compress != nil {
		fmt.Printf("tool-result compression: on for %s (JULIUS_COMPRESS_APPS)\n", compress.Scope())
	}
	if hints != nil {
		fmt.Printf("prompt-cache hints (anthropic): on for %s (JULIUS_CACHE_APPS)\n", hints.Scope())
	}
	fmt.Printf("tag traffic per app with the %s header; view usage with `julius savings`\n", appTagHeader)
	return srv.ListenAndServe()
}

func splitRoute(path string) (provider, rest string, ok bool) {
	trimmed := strings.TrimPrefix(path, "/")
	provider, rest, found := strings.Cut(trimmed, "/")
	if !found || provider == "" {
		return "", "", false
	}
	return provider, "/" + rest, true
}

// hop-by-hop headers must not be forwarded.
var hopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		if hopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
