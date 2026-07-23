package filter

import (
	"fmt"
	"regexp"
	"strings"
)

// Traceback frames kept per block: the entry frame shows where execution
// started, the deepest frames show where the exception lives — the middle
// is framework plumbing the agent cannot act on.
const (
	tbKeepFirst = 1
	tbKeepLast  = 3
)

var (
	tbHeaderRe = regexp.MustCompile(`^Traceback \(most recent call last\):\s*$`)
	tbFrameRe  = regexp.MustCompile(`^  File ".*", line \d+`)
)

// CollapseTracebacks shrinks CPython traceback blocks to their actionable
// frames. Every non-frame line — the header, the exception message,
// chained-exception separators — survives verbatim; only middle frames
// collapse, behind an explicit count marker. Blocks with few frames pass
// untouched, and frame-shaped lines outside a traceback header are never
// grouped: program output that merely looks frame-ish stays intact.
func CollapseTracebacks(text string) Result {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	changed := false

	// frames of the currently open traceback block; each frame is its
	// File line plus attached continuation lines (source, carets,
	// "[Previous line repeated N more times]").
	var frames [][]string
	inBlock := false

	flush := func() {
		if len(frames) == 0 {
			return
		}
		if omitted := len(frames) - tbKeepFirst - tbKeepLast; omitted >= 2 {
			kept := make([][]string, 0, tbKeepFirst+tbKeepLast)
			kept = append(kept, frames[:tbKeepFirst]...)
			kept = append(kept, frames[len(frames)-tbKeepLast:]...)
			out = append(out, kept[0]...)
			out = append(out, fmt.Sprintf("  [julius: %d frames omitted]", omitted))
			for _, f := range kept[1:] {
				out = append(out, f...)
			}
			changed = true
		} else {
			for _, f := range frames {
				out = append(out, f...)
			}
		}
		frames = frames[:0]
	}

	for _, line := range lines {
		switch {
		case tbHeaderRe.MatchString(line):
			flush()
			inBlock = true
			out = append(out, line)
		case inBlock && tbFrameRe.MatchString(line):
			frames = append(frames, []string{line})
		case inBlock && len(frames) > 0 &&
			(strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "  [Previous line repeated")):
			frames[len(frames)-1] = append(frames[len(frames)-1], line)
		default:
			flush()
			// An unindented non-empty line after frames is the exception
			// message: the block is over. Anything frame-shaped beyond it
			// is program output until the next header.
			if inBlock && strings.TrimSpace(line) != "" && !strings.HasPrefix(line, " ") {
				inBlock = false
			}
			out = append(out, line)
		}
	}
	flush()

	if !changed {
		return Result{Output: text}
	}
	return Result{Output: strings.Join(out, "\n"), Applied: true}
}
