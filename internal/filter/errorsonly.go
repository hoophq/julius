package filter

import (
	"fmt"
	"regexp"
	"strings"
)

// errLineRe matches lines carrying diagnostic signal — error/warning/failure
// markers and common "can't/denied/not found" phrasings. Case-insensitive.
// The bare "err" covers npm-style "ERR!" prefixes; "exit status/code" covers
// make/go wrappers reporting a child's failure. It is deliberately selective:
// on a failed command, keeping a noise line only costs a little saving, but
// the pattern must be tight enough that the noise it doesn't match actually
// gets dropped.
var errLineRe = regexp.MustCompile(`(?i)\b(err|error|errors|warning|warn|fail|failed|failing|failure|fatal|panic|traceback|exception|denied|refused|unable|cannot|can't|not found|no such|missing|invalid|exit (status|code))\b`)

const (
	// errorsOnlyTail is the number of trailing lines always kept for
	// context — a failed command's outcome usually lands in the last
	// handful of lines.
	errorsOnlyTail = 10
	// errorsOnlyMinCut is the shortest dropped run worth an omission
	// marker — below it the marker costs about as much as the lines it
	// hides, so shorter gaps stay verbatim.
	errorsOnlyMinCut = 3
)

// ErrorsOnly keeps the diagnostic signal from a failed command's output:
// lines matching an error/warning pattern, plus a bounded tail. Like
// DedupRepeats, it collapses per run — only a run of droppable lines long
// enough to out-save its marker is cut; shorter gaps stay verbatim.
//
// This is the generic fallback for UNRECOGNIZED failing commands in the
// wrapper, where the exit code is known and the caller has already stashed
// the full raw output to disk — so trimming stdout to its errors loses
// nothing recoverable. Callers must pass the result through Finalize.
//
// Conservative by construction: when no run is long enough to cut
// profitably, it passes through (Applied=false) and the never-larger guard
// keeps the raw.
func ErrorsOnly(raw string) Result {
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	n := len(lines)

	keep := make([]bool, n)
	for i, ln := range lines {
		if i >= n-errorsOnlyTail || errLineRe.MatchString(ln) {
			keep[i] = true
		}
	}

	out := make([]string, 0, n)
	collapsed := false
	for i := 0; i < n; {
		if keep[i] {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i
		for j < n && !keep[j] {
			j++
		}
		if run := j - i; run >= errorsOnlyMinCut {
			out = append(out, fmt.Sprintf("[julius: %d lines omitted]", run))
			collapsed = true
		} else {
			out = append(out, lines[i:j]...)
		}
		i = j
	}
	if !collapsed {
		return Result{Output: raw}
	}
	return Result{Output: strings.Join(out, "\n"), Applied: true}
}
