// Package router decides whether a shell command has a julius equivalent
// and rewrites it. It is the single source of truth consumed by the agent
// hook and the `julius route` debugging command.
package router

import (
	"strings"
)

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
		if text != "" && !isWrapped(text) && routable(text) {
			text = "julius " + text
			changed = true
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
