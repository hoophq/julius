package filter

import (
	"fmt"
	"strings"
	"testing"
)

func TestErrorsOnlyKeepsErrorsAndTail(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "downloading dependency %d\n", i)
	}
	b.WriteString("ERROR: cannot find module foo\n")
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&b, "cleanup step %d\n", i)
	}
	raw := b.String()

	res := Finalize(raw, ErrorsOnly(raw))
	if !res.Applied {
		t.Fatal("noisy failure output should be trimmed")
	}
	if !strings.Contains(res.Output, "ERROR: cannot find module foo") {
		t.Errorf("error line dropped:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "[julius: 20 lines omitted]") {
		t.Errorf("omission marker missing:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "downloading dependency") {
		t.Errorf("noise survived:\n%s", res.Output)
	}
}

func TestErrorsOnlyPreservesDistantError(t *testing.T) {
	var b strings.Builder
	b.WriteString("boot phase a\nboot phase b\nboot phase c\nboot phase d\n")
	b.WriteString("FATAL: disk full\n")
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&b, "progress tick %d\n", i)
	}
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "shutting down %d\n", i)
	}
	raw := b.String()

	res := Finalize(raw, ErrorsOnly(raw))
	if !res.Applied {
		t.Fatal("should be trimmed")
	}
	// An error far from the tail is kept because it matches, not by position.
	if !strings.Contains(res.Output, "FATAL: disk full") {
		t.Errorf("distant error dropped:\n%s", res.Output)
	}
	// Two dropped runs (before and after the error) → two markers.
	if n := strings.Count(res.Output, "lines omitted]"); n != 2 {
		t.Errorf("want two omission markers, got %d:\n%s", n, res.Output)
	}
}

func TestErrorsOnlyShortGapsKeptVerbatim(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "compiling unit %d\n", i)
	}
	b.WriteString("ERROR: bad type in unit 20\n")
	b.WriteString("see the declaration site\n") // 1-line gap between kept lines
	b.WriteString("ERROR: second diagnostic\n")
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&b, "tail line %d\n", i)
	}
	raw := b.String()

	res := Finalize(raw, ErrorsOnly(raw))
	if !res.Applied {
		t.Fatal("should be trimmed")
	}
	// A gap shorter than errorsOnlyMinCut is kept verbatim, never replaced
	// by a marker that would cost more than the line it hides.
	if !strings.Contains(res.Output, "see the declaration site") {
		t.Errorf("short gap replaced instead of kept:\n%s", res.Output)
	}
	if n := strings.Count(res.Output, "lines omitted]"); n != 1 {
		t.Errorf("want exactly one omission marker, got %d:\n%s", n, res.Output)
	}
	if !strings.Contains(res.Output, "[julius: 20 lines omitted]") {
		t.Errorf("long run not collapsed:\n%s", res.Output)
	}
}

func TestErrorsOnlyPassthroughWhenNothingToCut(t *testing.T) {
	raw := "line one\nline two\nline three"
	if res := ErrorsOnly(raw); res.Applied {
		t.Errorf("short output must pass through, got %q", res.Output)
	}
}

func TestErrorsOnlyPassthroughWhenFewDroppable(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&b, "info line %d\n", i)
	}
	// Only 2 lines fall outside the 10-line tail — below the marker threshold.
	if res := ErrorsOnly(b.String()); res.Applied {
		t.Errorf("too few droppable lines: must pass through, got:\n%s", res.Output)
	}
}
