package filter

import (
	"fmt"
	"strings"
	"testing"
)

func deepTraceback(frames int) string {
	var sb strings.Builder
	sb.WriteString("Traceback (most recent call last):\n")
	for i := 0; i < frames; i++ {
		fmt.Fprintf(&sb, "  File \"/venv/lib/python3.12/site-packages/fw/layer%02d.py\", line %d, in call\n", i, i*7+10)
		sb.WriteString("    return self.next(request)\n")
	}
	sb.WriteString("ValueError: boom\n")
	return sb.String()
}

func TestCollapseTracebacksChainedExceptions(t *testing.T) {
	text := deepTraceback(8) +
		"\nDuring handling of the above exception, another exception occurred:\n\n" +
		deepTraceback(7)
	r := CollapseTracebacks(text)
	if !r.Applied {
		t.Fatal("chained deep tracebacks must collapse")
	}
	if !strings.Contains(r.Output, "During handling of the above exception") {
		t.Error("chain separator must survive verbatim")
	}
	if got := strings.Count(r.Output, "[julius: "); got != 2 {
		t.Errorf("both blocks must collapse independently, got %d markers:\n%s", got, r.Output)
	}
	if !strings.Contains(r.Output, "[julius: 4 frames omitted]") || !strings.Contains(r.Output, "[julius: 3 frames omitted]") {
		t.Errorf("markers must count each block's own frames:\n%s", r.Output)
	}
	if got := strings.Count(r.Output, "ValueError: boom"); got != 2 {
		t.Errorf("both exception messages must survive, got %d", got)
	}
}

func TestCollapseTracebacksRecursionRepeatLineAttachesToFrame(t *testing.T) {
	text := "Traceback (most recent call last):\n" +
		"  File \"/app/main.py\", line 3, in <module>\n" +
		"    recurse()\n" +
		"  File \"/app/main.py\", line 6, in recurse\n" +
		"    return recurse()\n" +
		"  [Previous line repeated 996 more times]\n" +
		"RecursionError: maximum recursion depth exceeded\n"
	r := CollapseTracebacks(text)
	// two frame units: below the collapse threshold, everything survives
	if r.Applied {
		t.Fatalf("short recursion traceback must pass untouched, got:\n%s", r.Output)
	}
}

func TestCollapseTracebacksIgnoresFramesWithoutHeader(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&sb, "  File \"/data/report%02d.py\", line %d, in export\n", i, i)
	}
	if r := CollapseTracebacks(sb.String()); r.Applied {
		t.Errorf("frame-shaped lines outside a traceback header must never collapse:\n%s", r.Output)
	}
}

func TestCollapseTracebacksStopsAtExceptionMessage(t *testing.T) {
	// Frame-shaped lines printed by the program AFTER the exception line
	// belong to program output, not the traceback.
	text := deepTraceback(6) +
		"  File \"printed-by-program\", line 1, in fake\n" +
		"  File \"printed-by-program\", line 2, in fake\n"
	r := CollapseTracebacks(text)
	if !r.Applied {
		t.Fatal("the real block must collapse")
	}
	if !strings.Contains(r.Output, "  File \"printed-by-program\", line 1, in fake") {
		t.Errorf("post-exception program output must survive verbatim:\n%s", r.Output)
	}
	if strings.Count(r.Output, "[julius: ") != 1 {
		t.Errorf("only the real block may collapse:\n%s", r.Output)
	}
}
