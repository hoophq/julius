package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoophq/julius/internal/ledger"
)

// captureStdout runs f and returns everything it printed.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	f()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func testLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// The API-usage section's honesty contract: dollar figures appear only
// under the labeled cost line, unpriced models render as "—" and are
// disclosed, and the as-of date is always present when cost is shown.
func TestRenderAPIUsageHonestyContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.toml")
	table := `
as_of = "2030-01-01"
[models."m-priced"]
input = 1.0
output = 2.0
cache_read = 0.1
cache_write = 1.25
`
	if err := os.WriteFile(path, []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JULIUS_PRICING", path)

	l := testLedger(t)
	base := time.Now().Add(-time.Hour)
	calls := []ledger.APICall{
		{TS: base, AppTag: "a", Provider: "anthropic", Model: "m-priced", Input: 1_000_000, Output: 0},
		{TS: base, AppTag: "b", Provider: "openai", Model: "m-unknown", Input: 500, Output: 50},
	}
	for _, c := range calls {
		if err := l.RecordAPICall(c); err != nil {
			t.Fatal(err)
		}
	}

	out := captureStdout(t, func() { renderAPIUsage(l, base.Add(-time.Hour), 30) })

	for _, want := range []string{
		"pricing as of 2030-01-01", // as-of label
		"1 model(s) not priced",    // disclosure of the unknown model
		"—",                        // unpriced row renders em-dash
		"~$1.00",                   // 1M input at $1/MTok
		"custom table",             // source disclosure for overrides
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// every dollar figure lives below the labeled cost line
	if idx := strings.Index(out, "$"); idx >= 0 && idx < strings.Index(out, "cost") {
		t.Errorf("dollar figure before the labeled cost line:\n%s", out)
	}
}

// The estimate surfaces stay token-only: no dollar sign may ever appear
// in the proxy-compression section.
func TestRenderProxySavingsStaysTokenOnly(t *testing.T) {
	l := testLedger(t)
	if err := l.RecordProxySaving(ledger.ProxySaving{
		AppTag: "a", Provider: "anthropic", TokensBefore: 90_000, TokensAfter: 10_000,
	}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { renderProxySavings(l, time.Now().Add(-time.Hour), 30) })
	if out == "" {
		t.Fatal("expected proxy-compression section to render")
	}
	if strings.Contains(out, "$") {
		t.Errorf("proxy-compression surface must stay token-only:\n%s", out)
	}
}

func TestFmtUSDBoundaries(t *testing.T) {
	cases := map[float64]string{
		0:         "<$0.01",
		0.004:     "<$0.01",
		0.0051:    "$0.01",
		1.005:     "$1.00", // %.2f half-even is fine either way; pin current behavior
		99.99:     "$99.99",
		99.997:    "$100", // must not render "$100.00" in the cents bucket
		100.001:   "$100",
		9_999.4:   "$9999",
		9_999.7:   "$10.0k", // must not render "$10000"
		10_000.1:  "$10.0k",
		999_949:   "$999.9k",
		1_000_000: "$1.0M",
		5_000_000: "$5.0M",
	}
	for v, want := range cases {
		if got := fmtUSD(v); got != want {
			t.Errorf("fmtUSD(%v) = %q, want %q", v, got, want)
		}
	}
}
