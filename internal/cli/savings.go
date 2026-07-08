package cli

import (
	"fmt"
	"time"

	"github.com/hoophq/julius/internal/ledger"
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
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 30, "look-back window in days")
	return cmd
}

// renderAPIUsage prints the proxy surface. The two surfaces are reported
// separately by design: hook numbers are estimates, these are exact.
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
	byApp, err := l.APIUsageByApp(since, 10)
	if err != nil || len(byApp) == 0 {
		return
	}
	fmt.Printf("\n  %s\n", ui.Dim(fmt.Sprintf("%-16s %-24s %9s %9s %7s", "app", "model", "in", "out", "calls")))
	for _, a := range byApp {
		fmt.Printf("  %-16s %-24s %9s %9s %7d\n",
			truncate(a.AppTag, 16), truncate(a.Model, 24), fmtTokens(a.Input), fmtTokens(a.Output), a.Calls)
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
