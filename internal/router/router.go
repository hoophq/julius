// Package router decides whether a shell command has a julius equivalent
// and rewrites it. It is the single source of truth consumed by the agent
// hook and the `julius route` debugging command.
package router

import (
	"regexp"
	"strings"
)

// envPrefixRe matches a leading run of shell environment assignments
// (FOO=bar BAZ="a b" ...) so they can be preserved ahead of the julius
// wrapper: `CGO_ENABLED=0 go build` must rewrite to
// `CGO_ENABLED=0 julius go build`, not `julius CGO_ENABLED=0 go build`.
// The assignments still reach the wrapped child, which inherits julius's
// environment.
var envPrefixRe = regexp.MustCompile(`^((?:[A-Za-z_][A-Za-z0-9_]*=(?:"[^"]*"|'[^']*'|[^\s"']*)\s+)+)(\S.*)$`)

// SplitEnvPrefix separates a leading env-assignment run from the command.
// Returns ("", text) when there is no such prefix. Exported so coverage
// analysis (scan) classifies commands exactly as the router rewrites them.
func SplitEnvPrefix(text string) (prefix, rest string) {
	m := envPrefixRe.FindStringSubmatch(text)
	if m == nil {
		return "", text
	}
	return m[1], m[2]
}

// Part is one segment of a shell command chain. Sep is the separator that
// FOLLOWS the segment ("" for the last one).
type Part struct {
	Text string
	Sep  string
}

// SplitChain splits a command line on top-level shell separators
// (&&, ||, ;, |, newline) while respecting quotes, escapes, subshells,
// and backticks. It never splits inside those constructs.
func SplitChain(cmd string) []Part {
	var parts []Part
	var buf strings.Builder
	var inSingle, inDouble, inBacktick, escaped bool
	depth := 0

	flush := func(sep string) {
		parts = append(parts, Part{Text: strings.TrimSpace(buf.String()), Sep: sep})
		buf.Reset()
	}

	runes := []rune(cmd)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			buf.WriteRune(r)
			escaped = false
			continue
		}
		switch {
		case r == '\\' && !inSingle:
			escaped = true
			buf.WriteRune(r)
			continue
		case r == '\'' && !inDouble && !inBacktick:
			inSingle = !inSingle
		case r == '"' && !inSingle && !inBacktick:
			inDouble = !inDouble
		case r == '`' && !inSingle:
			inBacktick = !inBacktick
		case (r == '(' ) && !inSingle && !inDouble && !inBacktick:
			depth++
		case (r == ')') && !inSingle && !inDouble && !inBacktick && depth > 0:
			depth--
		}

		topLevel := !inSingle && !inDouble && !inBacktick && depth == 0
		if topLevel {
			switch {
			case r == '&' && i+1 < len(runes) && runes[i+1] == '&':
				flush("&&")
				i++
				continue
			case r == '|' && i+1 < len(runes) && runes[i+1] == '|':
				flush("||")
				i++
				continue
			case r == '|':
				flush("|")
				continue
			case r == ';' || r == '\n':
				flush(";")
				continue
			}
		}
		buf.WriteRune(r)
	}
	flush("")
	return parts
}

// Matcher reports whether a filter exists for the given command line.
type Matcher func(cmd string) bool

// Route rewrites every routable segment of cmd to run through julius.
// Already-wrapped segments are left untouched (idempotent). It returns the
// (possibly unchanged) command and whether anything was rewritten.
func Route(cmd string, routable Matcher) (string, bool) {
	parts := SplitChain(cmd)
	changed := false
	var b strings.Builder
	for _, p := range parts {
		text := p.Text
		if text != "" {
			env, core := SplitEnvPrefix(text)
			if !isWrapped(core) && routable(core) {
				text = env + "julius " + core
				changed = true
			}
		}
		b.WriteString(text)
		if p.Sep != "" {
			b.WriteString(" " + p.Sep + " ")
		}
	}
	return b.String(), changed
}

func isWrapped(text string) bool {
	return text == "julius" || strings.HasPrefix(text, "julius ")
}
