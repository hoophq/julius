package cli

import (
	"fmt"
	"os"

	"github.com/hoophq/julius/internal/execx"
	"github.com/spf13/cobra"
)

func newRawCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "raw <command> [args...]",
		Short:              "Run a command with no filtering (debugging escape hatch)",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			outc, err := execx.Run(args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "julius: %v\n", err)
			}
			os.Stdout.WriteString(outc.Stdout)
			os.Stderr.WriteString(outc.Stderr)
			if outc.ExitCode != 0 {
				return exitCodeError(outc.ExitCode)
			}
			return nil
		},
	}
}
