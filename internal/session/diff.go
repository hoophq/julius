package session

import (
	"fmt"
	"strings"
)

// diffMaxCells bounds the LCS table so pathological inputs can't blow up
// memory; beyond it Diff reports failure and callers fall back to full
// content.
const diffMaxCells = 4_000_000

// Diff returns a compact line diff from old to new with -/+/@@ notation,
// or ok=false when the inputs are too large to diff cheaply.
func Diff(oldText, newText string) (string, bool) {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")
	if len(oldLines)*len(newLines) > diffMaxCells {
		return "", false
	}

	// LCS table.
	n, m := len(oldLines), len(newLines)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	// Walk the table emitting hunks of consecutive changes.
	var out []string
	var hunk []string
	i, j := 0, 0
	hunkStart := -1
	flush := func() {
		if len(hunk) > 0 {
			out = append(out, fmt.Sprintf("@@ line %d @@", hunkStart+1))
			out = append(out, hunk...)
			hunk = nil
		}
		hunkStart = -1
	}
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			flush()
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			if hunkStart < 0 {
				hunkStart = j
			}
			hunk = append(hunk, "- "+oldLines[i])
			i++
		default:
			if hunkStart < 0 {
				hunkStart = j
			}
			hunk = append(hunk, "+ "+newLines[j])
			j++
		}
	}
	for ; i < n; i++ {
		if hunkStart < 0 {
			hunkStart = j
		}
		hunk = append(hunk, "- "+oldLines[i])
	}
	for ; j < m; j++ {
		if hunkStart < 0 {
			hunkStart = j
		}
		hunk = append(hunk, "+ "+newLines[j])
	}
	flush()

	return strings.Join(out, "\n"), true
}
