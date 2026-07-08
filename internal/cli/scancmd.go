package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/scan"
	"github.com/hoophq/julius/internal/ui"
	"github.com/spf13/cobra"
)

func newScanCmd() *cobra.Command {
	var days int
	var dir string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan Claude Code transcripts for savings julius missed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			if dir == "" {
				dir = scan.TranscriptDir(cwd)
			}
			rep, err := scan.Dir(dir, time.Now().AddDate(0, 0, -days), filter.Load(cwd))
			if err != nil {
				return fmt.Errorf("scan %s: %w", dir, err)
			}

			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(rep)
			}

			fmt.Printf("%s %s\n", ui.Title("Scan"),
				ui.Dim(fmt.Sprintf("· %d sessions · last %dd · %d bash commands, %d already wrapped",
					rep.Sessions, days, rep.BashCommands, rep.Wrapped)))

			if len(rep.Missed) == 0 && len(rep.Candidates) == 0 {
				fmt.Printf("\n  %s\n", ui.Good("nothing missed — julius covered everything it could"))
				return nil
			}
			if len(rep.Missed) > 0 {
				fmt.Printf("\n  %s\n", ui.Bold("ran unwrapped — measured savings julius would have delivered:"))
				maxSaved := rep.Missed[0].Saved()
				for i, m := range rep.Missed {
					if i >= 10 {
						break
					}
					fmt.Printf("    %-24s %s %s  %s\n", m.Command, ui.Good(fmt.Sprintf("%8s", fmtTokens(m.Saved()))),
						ui.Dim(fmt.Sprintf("(%d runs)", m.Runs)), ui.Bar(m.Saved(), maxSaved, 10))
				}
			}
			if len(rep.Candidates) > 0 {
				fmt.Printf("\n  %s\n", ui.Bold("no filter yet — top candidates by output volume:"))
				maxTokens := rep.Candidates[0].Tokens
				for i, c := range rep.Candidates {
					if i >= 10 {
						break
					}
					fmt.Printf("    %-24s %s %s  %s\n", truncate(c.Family, 24), ui.Warn(fmt.Sprintf("%8s", fmtTokens(c.Tokens))),
						ui.Dim(fmt.Sprintf("(%d runs)", c.Runs)), ui.Bar(c.Tokens, maxTokens, 10))
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "look-back window in days")
	cmd.Flags().StringVar(&dir, "dir", "", "transcript directory (default: this project's Claude Code transcripts)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the full report as JSON")
	return cmd
}
