// Package cli wires the julius command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "julius",
		Short:         "julius — cut LLM token usage on command output and API traffic",
		Long:          "julius filters and compresses dev-command output before it reaches an AI agent's context,\nand meters LLM API usage for scripts and applications.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute(version string) int {
	root := newRootCmd(version)
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "julius: %v\n", err)
		return 1
	}
	return 0
}
