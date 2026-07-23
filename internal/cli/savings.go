package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/hoophq/julius/internal/ledger"
	"github.com/hoophq/julius/internal/pricing"
	"github.com/hoophq/julius/internal/ui"
	"github.com/spf13/cobra"
)

func newSavingsCmd() *cobra.Command {
	var days int
	var jsonOut, current bool
	var sessionID string
	cmd := &cobra.Command{
		Use:   "savings",
		Short: "Show token savings and usage",
		Long: `Show token savings and usage.

Estimated savings are reported per kind — commands (wrapper-filtered),
native-tool compression (PostToolUse), and session dedup — and never
blended with the exact, provider-reported API usage.

--current and --session scope the estimate sections to one Claude Code
session. Session totals include subagent activity within that session,
and rows recorded without session attribution (older julius versions,
runs outside a session) are excluded and disclosed, never guessed. The
API-usage and proxy sections are app-scoped, not session-scoped, and are
omitted from session views.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if current {
				if sessionID != "" {
					return fmt.Errorf("--current and --session are mutually exclusive")
				}
				sessionID = os.Getenv("CLAUDE_CODE_SESSION_ID")
				if sessionID == "" {
					return fmt.Errorf("not inside a Claude Code session (CLAUDE_CODE_SESSION_ID is unset)")
				}
			}
			l, err := ledger.Open(ledger.DefaultPath())
			if err != nil {
				return fmt.Errorf("open ledger: %w", err)
			}
			defer l.Close()

			since := time.Now().AddDate(0, 0, -days)
			if jsonOut {
				return renderSavingsJSON(l, since, days, sessionID)
			}
			return renderSavings(l, since, days, sessionID)
		},
	}
	cmd.Flags().IntVar(&days, "days", 30, "look-back window in days")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output (versioned, basis-labeled per section)")
	cmd.Flags().BoolVar(&current, "current", false, "scope to the running Claude Code session")
	cmd.Flags().StringVar(&sessionID, "session", "", "scope to a session id")
	return cmd
}

// estimate kinds, mapped once: everything else is reported as-is under
// "unattributed" — an unknown kind is disclosed, never folded into a
// known bucket.
const (
	kindCommands   = "commands"
	kindNativeTool = "native-tool"
	kindDedup      = "session-dedup"
)

func kindBucket(kind string) string {
	switch kind {
	case "command", "rewrite":
		return kindCommands
	case "post_compress":
		return kindNativeTool
	case "session_dedup":
		return kindDedup
	}
	return ""
}

// estBreakdown is the per-kind aggregate behind both output modes.
type estBreakdown struct {
	commands     ledger.Totals
	nativeTool   ledger.Totals
	dedup        ledger.Totals
	unattributed ledger.Totals // unknown kinds
	total        ledger.Totals
}

func loadBreakdown(l *ledger.Ledger, since time.Time, sessionID string) (estBreakdown, error) {
	var b estBreakdown
	kinds, err := l.HookKindTotals(since, sessionID)
	if err != nil {
		return b, err
	}
	for _, k := range kinds {
		switch kindBucket(k.Kind) {
		case kindCommands:
			b.commands = addTotals(b.commands, k.Totals)
		case kindNativeTool:
			b.nativeTool = addTotals(b.nativeTool, k.Totals)
		case kindDedup:
			b.dedup = addTotals(b.dedup, k.Totals)
		default:
			b.unattributed = addTotals(b.unattributed, k.Totals)
		}
		b.total = addTotals(b.total, k.Totals)
	}
	return b, nil
}

func addTotals(a, b ledger.Totals) ledger.Totals {
	return ledger.Totals{
		Events:       a.Events + b.Events,
		TokensBefore: a.TokensBefore + b.TokensBefore,
		TokensAfter:  a.TokensAfter + b.TokensAfter,
	}
}

func savedPct(t ledger.Totals) float64 {
	if t.TokensBefore == 0 {
		return 0
	}
	return float64(t.Saved()) / float64(t.TokensBefore) * 100
}

func renderSavings(l *ledger.Ledger, since time.Time, days int, sessionID string) error {
	window := fmt.Sprintf("· estimates · last %dd", days)
	if sessionID != "" {
		window += " · session " + sessionID
	}

	b, err := loadBreakdown(l, since, sessionID)
	if err != nil {
		return err
	}
	if b.total.Events == 0 {
		fmt.Printf("%s %s\n\n", ui.Title("Commands"), ui.Dim(window))
		fmt.Println("  no savings recorded yet")
		fmt.Printf("\n  %s\n", ui.Dim("run `julius init` to install the Claude Code hooks, or prefix commands manually: julius git status"))
		renderSessionFooter(l, since, sessionID)
		if sessionID == "" {
			renderAPIUsage(l, since, days)
			renderProxySavings(l, since, days)
		}
		return nil
	}

	// Commands: the wrapper surface. The top table lives here and holds
	// only command rows — pseudo-commands from other kinds never mix in.
	fmt.Printf("%s %s\n\n", ui.Title("Commands"), ui.Dim(window))
	if b.commands.Events == 0 {
		fmt.Println("  none recorded in this window")
	} else {
		renderEstTotals("commands", b.commands)
		if b.commands.TokensBefore/max(b.commands.Events, 1) < 150 {
			fmt.Printf("\n  %s\n", ui.Dim("note: mostly quiet commands in this window — savings scale with output volume"))
		}
		top, err := l.TopCommands(since, sessionID, 10)
		if err != nil {
			return err
		}
		if len(top) > 0 {
			maxSaved := top[0].Saved()
			fmt.Printf("\n  %s\n", ui.Dim(fmt.Sprintf("%-30s %5s %8s  %5s", "command", "runs", "saved", "avg%")))
			for _, c := range top {
				fmt.Printf("  %-30s %5d %8s  %s  %s\n",
					truncate(c.Command, 30), c.Events, fmtTokens(c.Saved()), ui.Pct(savedPct(c.Totals)),
					ui.Bar(c.Saved(), maxSaved, 10))
			}
		}
	}

	if b.nativeTool.Events > 0 {
		fmt.Printf("\n%s %s\n\n", ui.Title("Native-tool compression"), ui.Dim(window))
		renderEstTotals("results", b.nativeTool)
		if err := renderToolRows(l, since, "post_compress", sessionID); err != nil {
			return err
		}
	}

	if b.dedup.Events > 0 {
		fmt.Printf("\n%s %s\n\n", ui.Title("Session dedup"), ui.Dim(window))
		renderEstTotals("repeats", b.dedup)
		if err := renderToolRows(l, since, "session_dedup", sessionID); err != nil {
			return err
		}
	}

	if b.unattributed.Events > 0 {
		fmt.Printf("\n%s %s\n\n", ui.Title("Unattributed"), ui.Dim(window+" · rows from older julius versions"))
		renderEstTotals("events", b.unattributed)
	}

	renderSessionFooter(l, since, sessionID)
	if sessionID == "" {
		renderAPIUsage(l, since, days)
		renderProxySavings(l, since, days)
	}
	return nil
}

// renderEstTotals prints the two-line count/saved summary every estimate
// section shares.
func renderEstTotals(noun string, t ledger.Totals) {
	fmt.Printf("  %-10s %s   tokens %s %s %s\n",
		noun, ui.Bold(fmt.Sprintf("%d", t.Events)),
		fmtTokens(t.TokensBefore), ui.Dim("→"), fmtTokens(t.TokensAfter))
	pct := savedPct(t)
	fmt.Printf("  saved      %s %s  %s\n",
		ui.Good(fmtTokens(t.Saved())), ui.Pct(pct), ui.Meter(pct, 24))
}

func renderToolRows(l *ledger.Ledger, since time.Time, kind, sessionID string) error {
	tools, err := l.HookToolTotals(since, kind, sessionID)
	if err != nil {
		return err
	}
	if len(tools) < 2 && (len(tools) == 0 || tools[0].Tool != "") {
		return nil // a single attributed tool repeats the summary line
	}
	fmt.Printf("\n  %s\n", ui.Dim(fmt.Sprintf("%-16s %6s %8s  %5s", "tool", "count", "saved", "avg%")))
	for _, tt := range tools {
		name := tt.Tool
		if name == "" {
			name = "unattributed"
		}
		fmt.Printf("  %-16s %6d %8s  %s\n",
			truncate(name, 16), tt.Events, fmtTokens(tt.Saved()), ui.Pct(savedPct(tt.Totals)))
	}
	return nil
}

// renderSessionFooter discloses what a session view cannot see: rows with
// no session attribution (excluded, not guessed) and the subagent scope
// of session ids.
func renderSessionFooter(l *ledger.Ledger, since time.Time, sessionID string) {
	if sessionID == "" {
		return
	}
	fmt.Printf("\n  %s\n", ui.Dim("session totals include subagent activity within the session"))
	if noSess, err := l.HookNoSessionTotals(since); err == nil && noSess.Events > 0 {
		fmt.Printf("  %s\n", ui.Dim(fmt.Sprintf(
			"%d event(s) in this window carry no session attribution and are excluded", noSess.Events)))
	}
	fmt.Printf("  %s\n", ui.Dim("API usage and proxy compression are app-scoped, not session-scoped — omitted"))
}

// JSON output, version 1. Every section carries an explicit basis so
// consumers cannot blend estimated and exact numbers; dollar figures are
// deliberately absent (pricing is dated — see `julius pricing`).
type savingsJSONTotals struct {
	Events       int `json:"events"`
	TokensBefore int `json:"tokens_before"`
	TokensAfter  int `json:"tokens_after"`
	Saved        int `json:"saved"`
}

type savingsJSONToolRow struct {
	Tool string `json:"tool"`
	savingsJSONTotals
}

type savingsJSONCommandRow struct {
	Command string `json:"command"`
	savingsJSONTotals
}

type savingsJSONEstimate struct {
	Basis string `json:"basis"`
	savingsJSONTotals
	ByTool      []savingsJSONToolRow    `json:"by_tool,omitempty"`
	TopCommands []savingsJSONCommandRow `json:"top_commands,omitempty"`
}

type savingsJSONAppRow struct {
	App      string `json:"app"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Calls    int    `json:"calls"`
	Input    int    `json:"input_tokens"`
	Output   int    `json:"output_tokens"`
}

type savingsJSONAPI struct {
	Basis      string              `json:"basis"`
	Calls      int                 `json:"calls"`
	Input      int                 `json:"input_tokens"`
	Output     int                 `json:"output_tokens"`
	CacheRead  int                 `json:"cache_read_tokens"`
	CacheWrite int                 `json:"cache_write_tokens"`
	ByApp      []savingsJSONAppRow `json:"by_app,omitempty"`
}

type savingsJSONDoc struct {
	Version          int                  `json:"version"`
	Since            string               `json:"since"`
	Days             int                  `json:"days"`
	SessionID        string               `json:"session_id,omitempty"`
	ExcludedNoSess   *savingsJSONTotals   `json:"excluded_no_session_id,omitempty"`
	Commands         *savingsJSONEstimate `json:"commands,omitempty"`
	NativeTool       *savingsJSONEstimate `json:"native_tool,omitempty"`
	SessionDedup     *savingsJSONEstimate `json:"session_dedup,omitempty"`
	Unattributed     *savingsJSONEstimate `json:"unattributed,omitempty"`
	APIUsage         *savingsJSONAPI      `json:"api_usage,omitempty"`
	ProxyCompression *savingsJSONEstimate `json:"proxy_compression,omitempty"`
}

const (
	basisEstimate = "estimate"
	basisExact    = "provider_exact"
)

func jsonTotals(t ledger.Totals) savingsJSONTotals {
	return savingsJSONTotals{
		Events: t.Events, TokensBefore: t.TokensBefore, TokensAfter: t.TokensAfter, Saved: t.Saved(),
	}
}

func renderSavingsJSON(l *ledger.Ledger, since time.Time, days int, sessionID string) error {
	b, err := loadBreakdown(l, since, sessionID)
	if err != nil {
		return err
	}
	doc := savingsJSONDoc{
		Version:   1,
		Since:     since.UTC().Format(time.RFC3339),
		Days:      days,
		SessionID: sessionID,
	}

	if b.commands.Events > 0 {
		sec := &savingsJSONEstimate{Basis: basisEstimate, savingsJSONTotals: jsonTotals(b.commands)}
		top, err := l.TopCommands(since, sessionID, 10)
		if err != nil {
			return err
		}
		for _, c := range top {
			sec.TopCommands = append(sec.TopCommands, savingsJSONCommandRow{Command: c.Command, savingsJSONTotals: jsonTotals(c.Totals)})
		}
		doc.Commands = sec
	}
	for _, s := range []struct {
		kind string
		tot  ledger.Totals
		dst  **savingsJSONEstimate
	}{
		{"post_compress", b.nativeTool, &doc.NativeTool},
		{"session_dedup", b.dedup, &doc.SessionDedup},
	} {
		if s.tot.Events == 0 {
			continue
		}
		sec := &savingsJSONEstimate{Basis: basisEstimate, savingsJSONTotals: jsonTotals(s.tot)}
		tools, err := l.HookToolTotals(since, s.kind, sessionID)
		if err != nil {
			return err
		}
		for _, tt := range tools {
			name := tt.Tool
			if name == "" {
				name = "unattributed"
			}
			sec.ByTool = append(sec.ByTool, savingsJSONToolRow{Tool: name, savingsJSONTotals: jsonTotals(tt.Totals)})
		}
		*s.dst = sec
	}
	if b.unattributed.Events > 0 {
		doc.Unattributed = &savingsJSONEstimate{Basis: basisEstimate, savingsJSONTotals: jsonTotals(b.unattributed)}
	}

	// A consumer cannot tell an omitted section from an errored query, so
	// every query failure fails the command — partial JSON that looks like
	// complete data would be a silent lie.
	if sessionID != "" {
		noSess, err := l.HookNoSessionTotals(since)
		if err != nil {
			return fmt.Errorf("unattributed-session totals: %w", err)
		}
		if noSess.Events > 0 {
			t := jsonTotals(noSess)
			doc.ExcludedNoSess = &t
		}
	} else {
		api, err := l.APIUsage(since)
		if err != nil {
			return fmt.Errorf("api usage: %w", err)
		}
		if api.Calls > 0 {
			sec := &savingsJSONAPI{
				Basis: basisExact, Calls: api.Calls, Input: api.Input, Output: api.Output,
				CacheRead: api.CacheRead, CacheWrite: api.CacheWrite,
			}
			byApp, err := l.APIUsageByApp(since, 10)
			if err != nil {
				return fmt.Errorf("api usage by app: %w", err)
			}
			for _, a := range byApp {
				sec.ByApp = append(sec.ByApp, savingsJSONAppRow{
					App: a.AppTag, Provider: a.Provider, Model: a.Model,
					Calls: a.Calls, Input: a.Input, Output: a.Output,
				})
			}
			doc.APIUsage = sec
		}
		prox, err := l.ProxySavingsTotals(since)
		if err != nil {
			return fmt.Errorf("proxy savings: %w", err)
		}
		if prox.Events > 0 {
			doc.ProxyCompression = &savingsJSONEstimate{Basis: basisEstimate, savingsJSONTotals: jsonTotals(prox)}
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// renderProxySavings prints request-compression savings. Estimates on the
// proxy surface — a third category, never folded into the command-surface
// numbers or the exact API usage above. Silent until compression has
// recorded something: the feature is opt-in.
func renderProxySavings(l *ledger.Ledger, since time.Time, days int) {
	tot, err := l.ProxySavingsTotals(since)
	if err != nil || tot.Events == 0 {
		return
	}
	fmt.Printf("\n%s %s\n\n", ui.Title("Proxy compression"), ui.Dim(fmt.Sprintf("· estimates · last %dd", days)))
	renderEstTotals("requests", tot)
}

// renderAPIUsage prints the proxy surface. The two surfaces are reported
// separately by design: hook numbers are estimates, these are exact.
// Cost is the one estimated figure in this section — exact tokens priced
// through a dated rate table — so it is labeled as an estimate with the
// table's as-of date, and models missing from the table render as "—"
// rather than being guessed.
func renderAPIUsage(l *ledger.Ledger, since time.Time, days int) {
	api, err := l.APIUsage(since)
	if err != nil || api.Calls == 0 {
		fmt.Printf("\n%s %s\n  %s\n",
			ui.Title("API usage"), ui.Dim("· exact, provider-reported"),
			ui.Dim("none recorded — run `julius proxy serve` and point apps at it"))
		return
	}
	fmt.Printf("\n%s %s\n\n", ui.Title("API usage"), ui.Dim(fmt.Sprintf("· exact, provider-reported · last %dd", days)))
	fmt.Printf("  calls %s   in %s   out %s   cache %s %s / %s %s\n",
		ui.Bold(fmt.Sprintf("%d", api.Calls)),
		ui.Bold(fmtTokens(api.Input)), ui.Bold(fmtTokens(api.Output)),
		ui.Dim("read"), fmtTokens(api.CacheRead), ui.Dim("write"), fmtTokens(api.CacheWrite))

	tbl, tblErr := pricing.Load()
	costLabeled := renderAPICost(l, since, tbl)
	if tblErr != nil {
		fmt.Printf("  %s\n", ui.Warn(fmt.Sprintf("pricing override ignored: %v", tblErr)))
	}

	byApp, err := l.APIUsageByApp(since, 10)
	if err != nil || len(byApp) == 0 {
		return
	}
	// the per-row cost column only renders under the labeled cost line:
	// a dollar figure must never appear without its as-of/estimate label
	header := fmt.Sprintf("%-16s %-24s %9s %9s %7s", "app", "model", "in", "out", "calls")
	if costLabeled {
		header += fmt.Sprintf(" %9s", "cost")
	}
	fmt.Printf("\n  %s\n", ui.Dim(header))
	for _, a := range byApp {
		row := fmt.Sprintf("%-16s %-24s %9s %9s %7d",
			truncate(a.AppTag, 16), truncate(a.Model, 24), fmtTokens(a.Input), fmtTokens(a.Output), a.Calls)
		if costLabeled {
			cost := "—"
			if r, ok := tbl.Lookup(a.Model); ok {
				cost = "~" + fmtUSD(r.Cost(a.Provider, a.Input, a.Output, a.CacheRead, a.CacheWrite))
			}
			row += fmt.Sprintf(" %9s", cost)
		}
		fmt.Printf("  %s\n", row)
	}
}

// renderAPICost prints the cost line for the API-usage section: spent,
// plus the net cost effect of caching when it is visible either way.
// Totals cover every recorded model (not just the top-N shown below);
// models absent from the pricing table are disclosed, never estimated.
// Returns whether a labeled cost line was printed — callers must not
// render any other dollar figure otherwise.
func renderAPICost(l *ledger.Ledger, since time.Time, tbl pricing.Table) bool {
	byModel, err := l.APIUsageByModel(since)
	if err != nil || len(byModel) == 0 {
		return false
	}
	var spent, cacheNet float64
	priced := 0
	unpricedModels := map[string]bool{}
	for _, m := range byModel {
		r, ok := tbl.Lookup(m.Model)
		if !ok {
			unpricedModels[m.Model] = true
			continue
		}
		priced++
		spent += r.Cost(m.Provider, m.Input, m.Output, m.CacheRead, m.CacheWrite)
		cacheNet += r.CacheNet(m.CacheRead, m.CacheWrite)
	}
	if priced == 0 {
		fmt.Printf("  cost  %s\n", ui.Dim("— no recorded model is in the pricing table (see `julius pricing`)"))
		return false
	}
	line := fmt.Sprintf("  cost  %s spent", ui.Bold("~"+fmtUSD(spent)))
	switch {
	case cacheNet >= 0.005:
		line += fmt.Sprintf("   %s saved by caching", ui.Good("~"+fmtUSD(cacheNet)))
	case cacheNet <= -0.005:
		line += fmt.Sprintf("   %s", ui.Dim(fmt.Sprintf("caching net -%s (write premium exceeded read savings)", fmtUSD(-cacheNet))))
	}
	note := fmt.Sprintf("estimate · pricing as of %s", tbl.AsOf)
	if tbl.Source != "builtin" {
		note += " · custom table"
	}
	if n := len(unpricedModels); n > 0 {
		note += fmt.Sprintf(" · %d model(s) not priced", n)
	}
	fmt.Printf("%s   %s\n", line, ui.Dim("· "+note))
	return true
}

// fmtUSD thresholds sit at the rounding boundary of the next format so
// a value never renders in the wrong bucket ($99.997 must be "$100",
// not "$100.00").
func fmtUSD(v float64) string {
	switch {
	case v < 0.005:
		return "<$0.01"
	case v < 99.995:
		return fmt.Sprintf("$%.2f", v)
	case v < 9_999.5:
		return fmt.Sprintf("$%.0f", v)
	case v < 999_950:
		return fmt.Sprintf("$%.1fk", v/1_000)
	default:
		return fmt.Sprintf("$%.1fM", v/1_000_000)
	}
}

func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
