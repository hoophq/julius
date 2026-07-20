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

// Terminal reports whether this part ends its pipeline: no following
// segment consumes its stdout as stdin. In `a | b && c | d`, b and d are
// terminal; a and c feed pipes and must run unfiltered — a filter's
// head/tail truncation would drop data and inject marker lines into the
// next command's stdin. Terminal alone does not mean stdout reaches the
// caller: a terminal segment may still send it to a file (see
// StdoutRedirected).
func (p Part) Terminal() bool { return p.Sep != "|" }

// StdoutRedirected reports whether the segment sends its stdout to a file
// through an unquoted redirection: >, >>, >|, their 1>-prefixed forms,
// &> / &>>, and >&file. Stderr-only redirections (2>, 2>>, 2>&1) don't
// count — stdout still reaches the caller — and neither do descriptor
// duplications like >&2. Route declines to wrap redirected segments: the
// file would receive julius-filtered output instead of the raw bytes the
// command wrote.
func (p Part) StdoutRedirected() bool {
	var lex lexState
	runes := []rune(p.Text)
	for i := 0; i < len(runes); i++ {
		if !lex.step(runes[i]) || runes[i] != '>' {
			continue
		}
		// Descriptor: a standalone digit run glued to '>' names the fd;
		// anything else (or nothing) means the redirection moves stdout.
		j := i
		for j > 0 && isDigit(runes[j-1]) {
			j--
		}
		fd := ""
		if j < i && (j == 0 || runes[j-1] == ' ' || runes[j-1] == '\t') {
			fd = string(runes[j:i])
		}
		stdout := fd == "" || strings.TrimLeft(fd, "0") == "1"

		// Operator: consume the rest of >>, >|, or >&.
		k := i + 1
		switch {
		case k < len(runes) && (runes[k] == '>' || runes[k] == '|'):
			k++
		case k < len(runes) && runes[k] == '&':
			k++
			if k < len(runes) && (runes[k] == '-' || isDigit(runes[k])) {
				// >&N and >&- duplicate or close a descriptor; nothing
				// leaves the caller (e.g. 2>&1, >&2).
				i = k
				continue
			}
			// >&file sends both stdout and stderr to the file.
		}
		if stdout {
			return true
		}
		i = k - 1 // skip the consumed operator; none of it affects lex state
	}
	return false
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// lexState is the quote-tracking scanner shared by SplitChain and
// StdoutRedirected, so both agree on which characters are top-level shell
// syntax versus quoted, escaped, or nested data.
type lexState struct {
	inSingle, inDouble, inBacktick, escaped bool
	depth                                   int
}

// step consumes one rune and reports whether it is top-level shell syntax
// — outside quotes, backticks, and subshells, and not escaped.
func (l *lexState) step(r rune) bool {
	if l.escaped {
		l.escaped = false
		return false
	}
	switch {
	case r == '\\' && !l.inSingle:
		l.escaped = true
		return false
	case r == '\'' && !l.inDouble && !l.inBacktick:
		l.inSingle = !l.inSingle
	case r == '"' && !l.inSingle && !l.inBacktick:
		l.inDouble = !l.inDouble
	case r == '`' && !l.inSingle:
		l.inBacktick = !l.inBacktick
	case r == '(' && !l.inSingle && !l.inDouble && !l.inBacktick:
		l.depth++
	case r == ')' && !l.inSingle && !l.inDouble && !l.inBacktick && l.depth > 0:
		l.depth--
	}
	return !l.inSingle && !l.inDouble && !l.inBacktick && l.depth == 0
}

// SplitChain splits a command line on top-level shell separators
// (&&, ||, ;, |, newline) while respecting quotes, escapes, subshells,
// and backticks. It never splits inside those constructs, and a `|`
// completing the clobber redirect `>|` is part of its segment, not a pipe.
func SplitChain(cmd string) []Part {
	var parts []Part
	var buf strings.Builder
	var lex lexState
	prevGT := false // previous rune was a top-level '>' (for `>|`)

	flush := func(sep string) {
		parts = append(parts, Part{Text: strings.TrimSpace(buf.String()), Sep: sep})
		buf.Reset()
		prevGT = false
	}

	runes := []rune(cmd)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		topLevel := lex.step(r)
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
			case r == '|' && !prevGT:
				flush("|")
				continue
			case r == ';' || r == '\n':
				flush(";")
				continue
			}
		}
		prevGT = topLevel && r == '>'
		buf.WriteRune(r)
	}
	flush("")
	return parts
}

// MatchTarget reduces one chain segment to the command that filter and
// routability matching should see: leading env assignments are dropped, a
// leading sudo invocation is peeled off, and an executable referenced by
// path is reduced to its basename — `sudo -E /usr/bin/git status` matches
// like `git status`. Execution always uses the original text; only
// matching uses the reduced form. Segments it can't reduce safely are
// returned unchanged.
func MatchTarget(text string) string {
	_, rest := SplitEnvPrefix(strings.TrimSpace(text))
	rest = stripSudo(rest)
	tok, args := firstToken(rest)
	// Quoted paths can contain spaces firstToken can't see through; leave
	// them alone rather than match the wrong program.
	if strings.HasPrefix(tok, `"`) || strings.HasPrefix(tok, `'`) {
		return rest
	}
	if i := strings.LastIndex(tok, "/"); i >= 0 && i+1 < len(tok) {
		tok = tok[i+1:]
	}
	if args == "" {
		return tok
	}
	return tok + " " + args
}

// stripSudo removes a leading sudo invocation when doing so is safe for
// matching: bare sudo, passthrough flags (-E, -H, -n, -k, --preserve-env),
// a target user/group (-u NAME, -g NAME), and env assignments sudo passes
// to the command. Shell-invoking forms (-i, -s) and unrecognized flags
// leave the segment unreduced — better to miss a rewrite than to match
// the wrong command.
func stripSudo(s string) string {
	tok, rest := firstToken(s)
	if tok != "sudo" {
		return s
	}
	for {
		tok, r := firstToken(rest)
		switch {
		case tok == "-E" || tok == "-H" || tok == "-n" || tok == "-k" || tok == "--preserve-env":
			rest = r
		case tok == "-u" || tok == "-g":
			_, rest = firstToken(r)
		case strings.HasPrefix(tok, "-"):
			return s
		default:
			_, cmd := SplitEnvPrefix(rest)
			return cmd
		}
	}
}

// firstToken splits off the first whitespace-delimited token.
func firstToken(s string) (tok, rest string) {
	s = strings.TrimLeft(s, " \t")
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimLeft(s[i:], " \t")
	}
	return s, ""
}

// Matcher reports whether a filter exists for the given command line.
type Matcher func(cmd string) bool

// Route rewrites every routable segment of cmd to run through julius.
// Segments whose stdout does not reach the caller are never rewritten:
// non-terminal ones (feeding a pipe) because a filter would truncate the
// next command's stdin and inject marker lines as data, and
// stdout-redirected ones because the file would capture julius-filtered
// output instead of the raw bytes. Already-wrapped segments are left
// untouched (idempotent). It returns the (possibly unchanged) command and
// whether anything was rewritten.
func Route(cmd string, routable Matcher) (string, bool) {
	parts := SplitChain(cmd)
	changed := false
	var b strings.Builder
	for _, p := range parts {
		text := p.Text
		if text != "" && p.Terminal() && !p.StdoutRedirected() {
			env, core := SplitEnvPrefix(text)
			target := MatchTarget(core)
			if !isWrapped(target) && routable(target) {
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

// IsWrapped reports whether the segment already runs through julius, in
// any spelling MatchTarget understands (julius, ./julius, sudo julius).
func IsWrapped(text string) bool {
	return isWrapped(MatchTarget(text))
}

func isWrapped(text string) bool {
	return text == "julius" || strings.HasPrefix(text, "julius ")
}
