package filter

import (
	"fmt"
	"strings"
)

// dedupMinRun is the repetition threshold before collapsing identical lines.
const dedupMinRun = 3

// DedupRepeats collapses runs of identical lines into a single line with a
// repetition marker — the generic fallback for log-like output where no
// format-specific filter matches.
func DedupRepeats(raw string) Result {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	collapsed := false
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		run := j - i
		if run >= dedupMinRun && strings.TrimSpace(lines[i]) != "" {
			out = append(out, fmt.Sprintf("%s  [julius: repeated %d×]", lines[i], run))
			collapsed = true
		} else {
			for k := 0; k < run; k++ {
				out = append(out, lines[i])
			}
		}
		i = j
	}
	if !collapsed {
		return Result{Output: raw}
	}
	return Result{Output: strings.Join(out, "\n"), Applied: true}
}
