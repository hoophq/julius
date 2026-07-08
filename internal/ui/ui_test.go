package ui

import (
	"strings"
	"testing"
)

// Tests run without a TTY, so color must be off and everything plain.
func TestPlainModeWithoutTTY(t *testing.T) {
	if enabled {
		t.Skip("terminal attached; plain-mode assertions don't apply")
	}
	if got := Title("Savings"); got != "Savings" {
		t.Errorf("Title must be plain without TTY: %q", got)
	}
	if got := Pct(83); got != " 83%" {
		t.Errorf("Pct plain = %q", got)
	}
}

func TestMeterProportions(t *testing.T) {
	cases := []struct {
		pct    float64
		filled int
	}{
		{0, 0}, {50, 10}, {100, 20}, {93, 19}, {-5, 0}, {140, 20},
	}
	for _, c := range cases {
		m := Meter(c.pct, 20)
		if got := strings.Count(m, "█"); got != c.filled {
			t.Errorf("Meter(%v): %d filled cells, want %d", c.pct, got, c.filled)
		}
		if got := strings.Count(m, "█") + strings.Count(m, "░"); got != 20 {
			t.Errorf("Meter(%v): width %d, want 20", c.pct, got)
		}
	}
}

func TestBarScaling(t *testing.T) {
	if Bar(0, 100, 8) != "" {
		t.Error("zero value must render empty")
	}
	if got := strings.Count(Bar(100, 100, 8), "▪"); got != 8 {
		t.Errorf("full bar = %d cells, want 8", got)
	}
	if got := strings.Count(Bar(1, 1000, 8), "▪"); got != 1 {
		t.Errorf("tiny value must still render one cell, got %d", got)
	}
}
