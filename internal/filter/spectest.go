package filter

import (
	"fmt"
	"strings"
)

// TestResult is the outcome of one inline [[filters.X.tests]] case, run
// through the same Apply+Finalize path the wrapper uses.
type TestResult struct {
	Filter string
	Test   string
	Got    string
	Want   string
	Pass   bool
}

// RunTests executes the spec's inline tests. Unnamed cases are labeled
// by position so failures stay addressable.
func (s *Spec) RunTests() []TestResult {
	results := make([]TestResult, 0, len(s.Tests))
	for i, tc := range s.Tests {
		name := tc.Name
		if name == "" {
			name = fmt.Sprintf("#%d", i+1)
		}
		got := strings.TrimRight(Finalize(tc.Input, s.Apply(tc.Input, tc.ExitCode)).Output, "\n")
		// Apply normalizes CRLF in the input; want gets the same
		// treatment, or a filters.toml checked out with CRLF endings
		// fails every test on an invisible trailing \r.
		want := strings.TrimRight(strings.ReplaceAll(tc.Want, "\r\n", "\n"), "\n")
		results = append(results, TestResult{
			Filter: s.name, Test: name, Got: got, Want: want, Pass: got == want,
		})
	}
	return results
}
