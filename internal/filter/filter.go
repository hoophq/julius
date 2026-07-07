// Package filter is the compression engine shared by every julius surface:
// the CLI wrapper, the agent hooks, and (later) the API proxy.
//
// Two kinds of filters exist: declarative specs loaded from TOML (most
// filters) and code filters implementing Filter directly (formats that need
// real parsing). Both honor the same safety invariants, enforced centrally
// in Finalize: output is never larger than the raw input, and a filter that
// empties non-empty input falls back to the raw output.
package filter

import (
	"github.com/hoophq/julius/internal/tokens"
)

// Result is the outcome of applying a filter.
type Result struct {
	Output  string
	Applied bool   // false means passthrough: Output == raw input
	Note    string // optional human-visible marker, e.g. line counts
}

// Filter compresses the output of commands it recognizes.
type Filter interface {
	Name() string
	// MatchCommand reports whether this filter handles the given command line.
	MatchCommand(cmd string) bool
	// Apply compresses raw output. exitCode is the wrapped command's exit code;
	// filters must stay conservative on failure (errors matter, keep them).
	Apply(raw string, exitCode int) Result
}

// Finalize enforces the engine-wide safety invariants on a filter's output:
//
//   - never larger: if the filtered output estimates more tokens than the
//     raw input, the raw input wins;
//   - never silently empty: if filtering emptied non-empty input, the raw
//     input wins.
//
// Exception: when the raw input itself is empty, a filter may emit a terse
// ack (an if_empty message like "ok") — confirming success to the agent is
// worth those few tokens.
//
// Every call path that applies a Filter must route its result through here.
func Finalize(raw string, r Result) Result {
	if !r.Applied {
		return Result{Output: raw}
	}
	if trimmed(raw) == "" {
		return r
	}
	if trimmed(r.Output) == "" {
		return Result{Output: raw}
	}
	if tokens.Estimate(r.Output) > tokens.Estimate(raw) {
		return Result{Output: raw}
	}
	return r
}

func trimmed(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
