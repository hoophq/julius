// Package tokens estimates LLM token counts for text.
//
// The command-output surface reports *estimates* (a model-agnostic
// chars/4 heuristic); exact counts only exist on the API surface where
// providers report usage. Keep the two separate in any user-facing output.
package tokens

// Estimate returns the approximate token count for s using the
// ~4-chars-per-token heuristic: ceil(len(s)/4).
func Estimate(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len(s) + 3) / 4
}

// SavedPercent returns the percentage of tokens saved going from raw to
// filtered output, in [0,100]. Returns 0 when raw is empty or filtered
// is not smaller.
func SavedPercent(raw, filtered string) float64 {
	before := Estimate(raw)
	after := Estimate(filtered)
	if before == 0 || after >= before {
		return 0
	}
	return float64(before-after) / float64(before) * 100
}
