package cli

import (
	"os"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/hook"
	"github.com/hoophq/julius/internal/ledger"
	"github.com/spf13/cobra"
)

func newHookCmd() *cobra.Command {
	hookCmd := &cobra.Command{
		Use:    "hook",
		Short:  "Agent hook processors (installed by julius init)",
		Hidden: true,
	}
	hookCmd.AddCommand(&cobra.Command{
		Use:   "claude-pre",
		Short: "Claude Code PreToolUse processor: rewrites Bash commands to run through julius",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cwd, _ := os.Getwd()
			reg := filter.Load(cwd)
			// Never fails: a broken hook must not block the agent.
			hook.ProcessPreToolUse(os.Stdin, os.Stdout, func(c string) bool {
				return reg.Pick(c) != nil
			})
		},
	})
	hookCmd.AddCommand(&cobra.Command{
		Use:   "claude-post",
		Short: "Claude Code PostToolUse processor: compresses native tool outputs",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cwd, _ := os.Getwd()
			reg := filter.Load(cwd)
			// Never fails: a broken hook must not block the agent.
			hook.ProcessPostToolUse(os.Stdin, os.Stdout, reg, func(ev ledger.HookEvent) {
				record(ev)
			})
		},
	})
	return hookCmd
}
