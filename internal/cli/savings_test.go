package cli

import (
	"database/sql"
	"encoding/json"
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

// seedMixedKinds writes one window of rows across every kind, two
// sessions, plus pre-attribution and unknown-kind rows.
func seedMixedKinds(t *testing.T, l *ledger.Ledger, base time.Time) {
	t.Helper()
	events := []ledger.HookEvent{
		{TS: base, SessionID: "s1", Kind: "command", Tool: "cli", Command: "go test -v ./...", TokensBefore: 2000, TokensAfter: 200},
		{TS: base, SessionID: "", Kind: "command", Tool: "cli", Command: "npm install", TokensBefore: 1000, TokensAfter: 100},
		{TS: base, SessionID: "s1", Kind: "post_compress", Tool: "Bash", Command: "cargo build", TokensBefore: 800, TokensAfter: 80},
		{TS: base, SessionID: "s2", Kind: "post_compress", Tool: "Grep", Command: "grep handler", TokensBefore: 600, TokensAfter: 300},
		{TS: base, SessionID: "s1", Kind: "session_dedup", Tool: "Read", Command: "read /app/h.go", TokensBefore: 5000, TokensAfter: 40},
		{TS: base, SessionID: "s1", Kind: "hologram", Command: "???", TokensBefore: 50, TokensAfter: 10},
	}
	for _, ev := range events {
		if err := l.RecordHookEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSavingsPerKindSections(t *testing.T) {
	l := testLedger(t)
	base := time.Now().Add(-time.Hour)
	seedMixedKinds(t, l, base)

	out := captureStdout(t, func() {
		if err := renderSavings(l, base.Add(-time.Hour), 30, ""); err != nil {
			t.Error(err)
		}
	})

	for _, want := range []string{
		"Commands", "Native-tool compression", "Session dedup", "Unattributed",
		"go test -v ./...", // top table holds real commands
		"Bash", "Grep",     // native section splits per tool
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// pseudo-commands must never appear in the top-commands table
	if strings.Contains(out, "read /app/h.go") {
		t.Errorf("dedup pseudo-command leaked into output:\n%s", out)
	}
	// estimate surfaces stay token-only
	if strings.Contains(out, "$") {
		t.Errorf("dollar figure on an estimate surface:\n%s", out)
	}
}

func TestSavingsSessionViewDisclosures(t *testing.T) {
	l := testLedger(t)
	base := time.Now().Add(-time.Hour)
	seedMixedKinds(t, l, base)
	// API rows exist but are app-scoped: a session view must omit them
	if err := l.RecordAPICall(ledger.APICall{TS: base, Provider: "anthropic", Model: "m", Input: 100, Output: 10}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := renderSavings(l, base.Add(-time.Hour), 30, "s1"); err != nil {
			t.Error(err)
		}
	})

	for _, want := range []string{
		"session s1",
		"include subagent activity",
		"no session attribution and are excluded", // the SessionID:"" npm install row
		"app-scoped, not session-scoped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("session view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "provider-reported") {
		t.Errorf("API-usage section must be omitted from session views:\n%s", out)
	}
	// s2's grep row must not leak into s1 totals: Grep appears only via s2
	if strings.Contains(out, "Grep") {
		t.Errorf("other session's rows leaked into session view:\n%s", out)
	}
}

func TestSavingsJSONContract(t *testing.T) {
	l := testLedger(t)
	base := time.Now().Add(-time.Hour)
	seedMixedKinds(t, l, base)
	if err := l.RecordAPICall(ledger.APICall{TS: base, AppTag: "app", Provider: "anthropic", Model: "m", Input: 100, Output: 10, CacheRead: 5}); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordProxySaving(ledger.ProxySaving{TS: base, AppTag: "app", Provider: "anthropic", TokensBefore: 900, TokensAfter: 100}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := renderSavingsJSON(l, base.Add(-time.Hour), 30, ""); err != nil {
			t.Error(err)
		}
	})

	var doc savingsJSONDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if doc.Version != 1 {
		t.Errorf("version = %d, want 1", doc.Version)
	}
	// basis discipline: every estimate section labeled, exact section labeled
	for name, sec := range map[string]*savingsJSONEstimate{
		"commands": doc.Commands, "native_tool": doc.NativeTool,
		"session_dedup": doc.SessionDedup, "unattributed": doc.Unattributed,
		"proxy_compression": doc.ProxyCompression,
	} {
		if sec == nil {
			t.Errorf("section %s missing", name)
			continue
		}
		if sec.Basis != "estimate" {
			t.Errorf("section %s basis = %q, want estimate", name, sec.Basis)
		}
		if sec.Saved != sec.TokensBefore-sec.TokensAfter {
			t.Errorf("section %s saved math wrong: %+v", name, sec)
		}
	}
	if doc.APIUsage == nil || doc.APIUsage.Basis != "provider_exact" {
		t.Errorf("api_usage basis wrong: %+v", doc.APIUsage)
	}
	if doc.Commands.Events != 2 || doc.NativeTool.Events != 2 || doc.SessionDedup.Events != 1 {
		t.Errorf("per-kind counts wrong: commands=%+v native=%+v dedup=%+v",
			doc.Commands, doc.NativeTool, doc.SessionDedup)
	}
	for _, c := range doc.Commands.TopCommands {
		if strings.HasPrefix(c.Command, "read ") || strings.HasPrefix(c.Command, "grep ") {
			t.Errorf("pseudo-command in JSON top_commands: %q", c.Command)
		}
	}
	// dollars never appear in JSON: pricing is dated, consumers get tokens
	if strings.Contains(out, "$") {
		t.Errorf("dollar figure in JSON output:\n%s", out)
	}
}

func TestSavingsJSONSessionScoped(t *testing.T) {
	l := testLedger(t)
	base := time.Now().Add(-time.Hour)
	seedMixedKinds(t, l, base)
	if err := l.RecordAPICall(ledger.APICall{TS: base, Provider: "anthropic", Model: "m", Input: 100, Output: 10}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := renderSavingsJSON(l, base.Add(-time.Hour), 30, "s1"); err != nil {
			t.Error(err)
		}
	})

	var doc savingsJSONDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if doc.SessionID != "s1" {
		t.Errorf("session_id = %q", doc.SessionID)
	}
	if doc.APIUsage != nil || doc.ProxyCompression != nil {
		t.Error("app-scoped sections must be omitted from session views")
	}
	if doc.ExcludedNoSess == nil || doc.ExcludedNoSess.Events != 1 {
		t.Errorf("excluded_no_session_id = %+v, want the 1 unattributed row disclosed", doc.ExcludedNoSess)
	}
	if doc.Commands == nil || doc.Commands.Events != 1 {
		t.Errorf("session-scoped commands = %+v", doc.Commands)
	}
	if doc.NativeTool == nil || doc.NativeTool.Events != 1 {
		t.Errorf("session-scoped native_tool = %+v (s2 rows must not leak)", doc.NativeTool)
	}
}

// A JSON consumer cannot distinguish an omitted section from an errored
// query, so a query failure must fail the command — never ship
// valid-looking JSON with a section silently missing.
func TestSavingsJSONFailsClosedOnQueryError(t *testing.T) {
	for table, wantErr := range map[string]string{
		"api_calls":     "api usage",
		"proxy_savings": "proxy savings",
	} {
		path := filepath.Join(t.TempDir(), "ledger.db")
		l, err := ledger.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		base := time.Now().Add(-time.Hour)
		seedMixedKinds(t, l, base)

		// sabotage one app-scoped table; the estimate queries keep working
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
		db.Close()

		out := captureStdout(t, func() {
			err := renderSavingsJSON(l, base.Add(-time.Hour), 30, "")
			if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Errorf("dropped %s: err = %v, want wrapped %q error", table, err, wantErr)
			}
		})
		if out != "" {
			t.Errorf("dropped %s: partial JSON emitted despite error:\n%s", table, out)
		}
		l.Close()
	}
}

func TestSavingsCurrentFlagResolution(t *testing.T) {
	t.Setenv("JULIUS_LEDGER", filepath.Join(t.TempDir(), "ledger.db"))

	// outside a session: --current must fail loudly, not fall back to all rows
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	cmd := newSavingsCmd()
	cmd.SetArgs([]string{"--current"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "CLAUDE_CODE_SESSION_ID") {
		t.Errorf("--current outside a session: err = %v", err)
	}

	// --current and --session are mutually exclusive
	t.Setenv("CLAUDE_CODE_SESSION_ID", "live-session")
	cmd = newSavingsCmd()
	cmd.SetArgs([]string{"--current", "--session", "other"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("--current with --session: err = %v", err)
	}

	// inside a session: resolves the env id and scopes the JSON to it
	out := captureStdout(t, func() {
		cmd = newSavingsCmd()
		cmd.SetArgs([]string{"--current", "--json"})
		if err := cmd.Execute(); err != nil {
			t.Error(err)
		}
	})
	var doc savingsJSONDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if doc.SessionID != "live-session" {
		t.Errorf("session_id = %q, want the env-resolved id", doc.SessionID)
	}
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
