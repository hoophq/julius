// Package cli wires the julius command tree.
//
// Dispatch rule: known subcommands (init, doctor, savings, route, hook,
// raw, ...) go through cobra; anything else is treated as a command to
// wrap — `julius git status` executes `git status` and filters its output.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// reserved names route to cobra instead of the wrapper.
var reserved = map[string]bool{
	"init": true, "doctor": true, "savings": true, "route": true,
	"hook": true, "raw": true, "proxy": true, "scan": true,
	"help": true, "completion": true, "__complete": true, "__completeNoDesc": true,
}

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "julius",
		Short:         "julius — cut LLM token usage on command output and API traffic",
		Long:          "julius filters and compresses dev-command output before it reaches an AI agent's context,\nand meters LLM API usage for scripts and applications.\n\nRun any command through julius by prefixing it: julius git status",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newRouteCmd())
	root.AddCommand(newHookCmd())
	root.AddCommand(newSavingsCmd())
	root.AddCommand(newRawCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newDoctorCmd())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute(version string) int {
	args := os.Args[1:]

	// Wrapper fallback: `julius <some command ...>` where <some command>
	// is not a julius subcommand or flag.
	if len(args) > 0 && !reserved[args[0]] && args[0] != "" && args[0][0] != '-' {
		return wrap(args)
	}

	root := newRootCmd(version)
	if err := root.Execute(); err != nil {
		if ec, ok := err.(exitCodeError); ok {
			return int(ec)
		}
		fmt.Fprintf(os.Stderr, "julius: %v\n", err)
		return 1
	}
	return 0
}

// exitCodeError lets subcommands communicate a specific exit code
// (the route contract uses 0/1/2/3) without printing an error message.
type exitCodeError int

func (e exitCodeError) Error() string { return fmt.Sprintf("exit code %d", int(e)) }
