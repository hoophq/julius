package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/install"
	"github.com/hoophq/julius/internal/scan"
	"github.com/hoophq/julius/internal/ui"
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

// coverageHint warns when the install is healthy but most routable
// commands still bypass the hook — the realized-savings leak doctor's
// pass/fail checks can't see. Informational only: small samples and scan
// errors stay silent, and the hint never affects the exit code.
func coverageHint(cwd string) {
	rep, err := scan.Dir(scan.TranscriptDir(cwd), time.Now().AddDate(0, 0, -7), filter.Load(cwd))
	if err != nil {
		return
	}
	pct, wrapped, routable := rep.Coverage()
	if routable < 10 || pct >= 60 {
		return
	}
	fmt.Printf("\n%s  hook coverage last 7d: %s %s\n", ui.Warn("NOTE"), ui.Pct(pct),
		ui.Dim(fmt.Sprintf("(%d of %d routable commands went through julius)", wrapped, routable)))
	fmt.Printf("      run %s to see what's leaking\n", ui.Bold("julius scan"))
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Verify the julius installation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			ok := install.Render(install.Doctor(cwd), os.Stdout)
			coverageHint(cwd)
			if !ok {
				return exitCodeError(1)
			}
			return nil
		},
	}
}
