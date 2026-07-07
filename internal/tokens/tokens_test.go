package tokens

import "testing"

func TestEstimate(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"hello world", 3},
	}
	for _, c := range cases {
		if got := Estimate(c.in); got != c.want {
			t.Errorf("Estimate(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSavedPercent(t *testing.T) {
	if got := SavedPercent("aaaaaaaa", "aa"); got != 50 {
		t.Errorf("SavedPercent = %v, want 50", got)
	}
	if got := SavedPercent("", "x"); got != 0 {
		t.Errorf("SavedPercent on empty raw = %v, want 0", got)
	}
	if got := SavedPercent("ab", "abcd"); got != 0 {
		t.Errorf("SavedPercent when filtered bigger = %v, want 0", got)
	}
}
