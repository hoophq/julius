package cli

import (
	"fmt"
	"time"

	"github.com/hoophq/julius/internal/ledger"
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

			fmt.Printf("Command-output savings — estimates, last %dd\n\n", days)
			if tot.Events == 0 {
				fmt.Println("  no filtered commands recorded yet")
				fmt.Println("\n  run `julius init` to install the Claude Code hook, or prefix commands manually: julius git status")
				return nil
			}
			pct := 0.0
			if tot.TokensBefore > 0 {
				pct = float64(tot.Saved()) / float64(tot.TokensBefore) * 100
			}
			fmt.Printf("  commands: %d   tokens: %s → %s   saved: %s (%.0f%%)\n",
				tot.Events, fmtTokens(tot.TokensBefore), fmtTokens(tot.TokensAfter), fmtTokens(tot.Saved()), pct)

			top, err := l.TopCommands(since, 10)
			if err != nil {
				return err
			}
			if len(top) > 0 {
				fmt.Println("\n  top commands by tokens saved:")
				for _, c := range top {
					cmdPct := 0.0
					if c.TokensBefore > 0 {
						cmdPct = float64(c.Saved()) / float64(c.TokensBefore) * 100
					}
					fmt.Printf("    %-28s %8s saved  %3.0f%%  (%d runs)\n", truncate(c.Command, 28), fmtTokens(c.Saved()), cmdPct, c.Events)
				}
			}
			fmt.Println("\nAPI usage — exact, provider-reported: available with `julius proxy` (coming soon)")
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 30, "look-back window in days")
	return cmd
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
