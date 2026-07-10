package scan

import "testing"

func TestCoverage(t *testing.T) {
	// 3 wrapped, 1 missed command run twice → 3 of 5 routable = 60%.
	r := Report{
		Wrapped: 3,
		Missed:  []Missed{{Command: "go-test", Runs: 2}},
	}
	pct, wrapped, routable := r.Coverage()
	if wrapped != 3 || routable != 5 {
		t.Fatalf("counts = %d/%d, want 3/5", wrapped, routable)
	}
	if pct != 60 {
		t.Errorf("coverage = %v, want 60", pct)
	}
}

func TestCoverageExcludesCandidates(t *testing.T) {
	// Candidates have no filter — julius couldn't route them, so they must
	// not drag coverage down.
	r := Report{
		Wrapped:    4,
		Candidates: []Candidate{{Family: "psql", Runs: 100}},
	}
	pct, _, routable := r.Coverage()
	if routable != 4 || pct != 100 {
		t.Errorf("candidates leaked into coverage: %.0f%% over %d routable", pct, routable)
	}
}

func TestCoverageEmpty(t *testing.T) {
	pct, _, routable := Report{}.Coverage()
	if routable != 0 || pct != 0 {
		t.Errorf("empty report = %.0f%% / %d, want 0 / 0", pct, routable)
	}
}
