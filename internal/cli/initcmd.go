package cli

import (
	"errors"
	"os"

	"github.com/hoophq/julius/internal/install"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var global, autoPatch, noPatch bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Register the julius hook with Claude Code",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if autoPatch && noPatch {
				return errors.New("--auto-patch and --no-patch are mutually exclusive")
			}
			mode := install.PatchAsk
			if autoPatch {
				mode = install.PatchAuto
			}
			if noPatch {
				mode = install.PatchSkip
			}
			cwd, _ := os.Getwd()
			return install.Init(global, mode, cwd, os.Stdin, os.Stdout)
		},
	}
	cmd.Flags().BoolVarP(&global, "global", "g", false, "install into ~/.claude/settings.json instead of the project")
	cmd.Flags().BoolVar(&autoPatch, "auto-patch", false, "write settings without prompting")
	cmd.Flags().BoolVar(&noPatch, "no-patch", false, "print manual instructions only")
	return cmd
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Verify the julius installation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			if ok := install.Render(install.Doctor(cwd), os.Stdout); !ok {
				return exitCodeError(1)
			}
			return nil
		},
	}
}
