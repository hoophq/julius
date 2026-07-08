package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/scan"
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

			fmt.Printf("Scanned %d sessions, last %dd — %d bash commands, %d already wrapped\n",
				rep.Sessions, days, rep.BashCommands, rep.Wrapped)

			if len(rep.Missed) == 0 && len(rep.Candidates) == 0 {
				fmt.Println("\n  nothing missed — julius covered everything it could")
				return nil
			}
			if len(rep.Missed) > 0 {
				fmt.Println("\n  ran unwrapped — measured savings julius would have delivered:")
				for i, m := range rep.Missed {
					if i >= 10 {
						break
					}
					fmt.Printf("    %-24s %8s tokens  (%d runs)\n", m.Command, fmtTokens(m.Saved()), m.Runs)
				}
			}
			if len(rep.Candidates) > 0 {
				fmt.Println("\n  no filter yet — top candidates by output volume:")
				for i, c := range rep.Candidates {
					if i >= 10 {
						break
					}
					fmt.Printf("    %-24s %8s tokens  (%d runs)\n", c.Family, fmtTokens(c.Tokens), c.Runs)
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
