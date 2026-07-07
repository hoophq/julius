package cli

import (
	"fmt"
	"os"

	"github.com/hoophq/julius/internal/claude"
	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/router"
	"github.com/spf13/cobra"
)

// Exit-code contract for `julius route` (consumed by hook scripts for
// agents without native input rewriting):
//
//	0  rewritten, user's rules allow auto-approval (stdout: routed command)
//	1  no julius equivalent — pass through unchanged
//	2  a deny rule matches — do not touch the command
//	3  rewritten, but the host tool must still prompt (stdout: routed command)
func newRouteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "route <command>",
		Short: "Rewrite a shell command to run through julius (prints result, exit code carries the verdict)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			reg := filter.Load(cwd)
			routable := func(c string) bool { return reg.Pick(c) != nil }

			routed, changed := router.Route(args[0], routable)
			if !changed {
				return exitCodeError(1)
			}

			var segments []string
			for _, p := range router.SplitChain(args[0]) {
				if p.Text != "" {
					segments = append(segments, p.Text)
				}
			}
			switch claude.LoadRules(cwd).EvaluateChain(segments) {
			case claude.VerdictDeny:
				return exitCodeError(2)
			case claude.VerdictAllow:
				fmt.Println(routed)
				return nil
			default:
				fmt.Println(routed)
				return exitCodeError(3)
			}
		},
	}
}
