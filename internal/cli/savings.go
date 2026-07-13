package cli

import (
	"fmt"
	"time"

	"github.com/hoophq/julius/internal/ledger"
	"github.com/hoophq/julius/internal/pricing"
	"github.com/hoophq/julius/internal/ui"
	"github.com/spf13/cobra"
)

func newSavingsCmd() *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:   "savings",
		Short: "Show token savings and usage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := ledger.Open(ledger.DefaultPath())
			if err != nil {
				return fmt.Errorf("open ledger: %w", err)
			}
			defer l.Close()

			since := time.Now().AddDate(0, 0, -days)
			tot, err := l.HookTotals(since)
			if err != nil {
				return err
			}

			fmt.Printf("%s %s\n\n", ui.Title("Command-output savings"), ui.Dim(fmt.Sprintf("· estimates · last %dd", days)))
			if tot.Events == 0 {
				fmt.Println("  no filtered commands recorded yet")
				fmt.Printf("\n  %s\n", ui.Dim("run `julius init` to install the Claude Code hooks, or prefix commands manually: julius git status"))
				renderAPIUsage(l, since, days)
				renderProxySavings(l, since, days)
				return nil
			}

			pct := 0.0
			if tot.TokensBefore > 0 {
				pct = float64(tot.Saved()) / float64(tot.TokensBefore) * 100
			}
			fmt.Printf("  commands   %s   tokens %s %s %s\n",
				ui.Bold(fmt.Sprintf("%d", tot.Events)),
				fmtTokens(tot.TokensBefore), ui.Dim("→"), fmtTokens(tot.TokensAfter))
			fmt.Printf("  saved      %s %s  %s\n",
				ui.Good(fmtTokens(tot.Saved())), ui.Pct(pct), ui.Meter(pct, 24))
			if tot.TokensBefore/max(tot.Events, 1) < 150 {
				fmt.Printf("\n  %s\n", ui.Dim("note: mostly quiet commands in this window — savings scale with output volume"))
			}

			top, err := l.TopCommands(since, 10)
			if err != nil {
				return err
			}
			if len(top) > 0 {
				maxSaved := top[0].Saved()
				fmt.Printf("\n  %s\n", ui.Dim(fmt.Sprintf("%-30s %5s %8s  %5s", "command", "runs", "saved", "avg%")))
				for _, c := range top {
					cmdPct := 0.0
					if c.TokensBefore > 0 {
						cmdPct = float64(c.Saved()) / float64(c.TokensBefore) * 100
					}
					fmt.Printf("  %-30s %5d %8s  %s  %s\n",
						truncate(c.Command, 30), c.Events, fmtTokens(c.Saved()), ui.Pct(cmdPct),
						ui.Bar(c.Saved(), maxSaved, 10))
				}
			}
			renderAPIUsage(l, since, days)
			renderProxySavings(l, since, days)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 30, "look-back window in days")
	return cmd
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
	pct := 0.0
	if tot.TokensBefore > 0 {
		pct = float64(tot.Saved()) / float64(tot.TokensBefore) * 100
	}
	fmt.Printf("\n%s %s\n\n", ui.Title("Proxy compression"), ui.Dim(fmt.Sprintf("· estimates · last %dd", days)))
	fmt.Printf("  requests   %s   tokens %s %s %s\n",
		ui.Bold(fmt.Sprintf("%d", tot.Events)),
		fmtTokens(tot.TokensBefore), ui.Dim("→"), fmtTokens(tot.TokensAfter))
	fmt.Printf("  saved      %s %s  %s\n",
		ui.Good(fmtTokens(tot.Saved())), ui.Pct(pct), ui.Meter(pct, 24))
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
	renderAPICost(l, since, tbl)
	if tblErr != nil {
		fmt.Printf("  %s\n", ui.Warn(fmt.Sprintf("pricing override ignored: %v", tblErr)))
	}

	byApp, err := l.APIUsageByApp(since, 10)
	if err != nil || len(byApp) == 0 {
		return
	}
	fmt.Printf("\n  %s\n", ui.Dim(fmt.Sprintf("%-16s %-24s %9s %9s %7s %9s", "app", "model", "in", "out", "calls", "cost")))
	for _, a := range byApp {
		cost := "—"
		if r, ok := tbl.Lookup(a.Model); ok {
			cost = "~" + fmtUSD(r.Cost(a.Provider, a.Input, a.Output, a.CacheRead, a.CacheWrite))
		}
		fmt.Printf("  %-16s %-24s %9s %9s %7d %9s\n",
			truncate(a.AppTag, 16), truncate(a.Model, 24), fmtTokens(a.Input), fmtTokens(a.Output), a.Calls, cost)
	}
}

// renderAPICost prints the cost line for the API-usage section: spent,
// plus cost avoided by cache reads when there are any. Totals cover
// every recorded model (not just the top-N shown below); models absent
// from the pricing table are counted and disclosed, never estimated.
func renderAPICost(l *ledger.Ledger, since time.Time, tbl pricing.Table) {
	byModel, err := l.APIUsageByModel(since)
	if err != nil || len(byModel) == 0 {
		return
	}
	var spent, avoided float64
	priced, unpriced := 0, 0
	for _, m := range byModel {
		r, ok := tbl.Lookup(m.Model)
		if !ok {
			unpriced++
			continue
		}
		priced++
		spent += r.Cost(m.Provider, m.Input, m.Output, m.CacheRead, m.CacheWrite)
		avoided += r.CacheAvoided(m.CacheRead)
	}
	if priced == 0 {
		fmt.Printf("  cost  %s\n", ui.Dim("— no recorded model is in the pricing table (see `julius pricing`)"))
		return
	}
	line := fmt.Sprintf("  cost  %s spent", ui.Bold("~"+fmtUSD(spent)))
	if avoided >= 0.005 {
		line += fmt.Sprintf("   %s avoided via caching", ui.Good("~"+fmtUSD(avoided)))
	}
	note := fmt.Sprintf("estimate · pricing as of %s", tbl.AsOf)
	if tbl.Source != "builtin" {
		note += " · custom table"
	}
	if unpriced > 0 {
		note += fmt.Sprintf(" · %d model(s) not priced", unpriced)
	}
	fmt.Printf("%s   %s\n", line, ui.Dim("· "+note))
}

func fmtUSD(v float64) string {
	switch {
	case v < 0.005:
		return "<$0.01"
	case v < 100:
		return fmt.Sprintf("$%.2f", v)
	case v < 10_000:
		return fmt.Sprintf("$%.0f", v)
	default:
		return fmt.Sprintf("$%.1fk", v/1000)
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
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
