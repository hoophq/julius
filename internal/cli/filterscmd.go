package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/ui"
	"github.com/spf13/cobra"
)

func newFiltersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "filters",
		Short: "Work with custom filter files",
	}
	cmd.AddCommand(newFiltersTestCmd())
	return cmd
}

func newFiltersTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test [file ...]",
		Short: "Run the inline tests in custom filter files",
		Long: "Run every [[filters.X.tests]] case in the given filter files through the\n" +
			"same Apply+Finalize path the wrapper uses. Without arguments, tests the\n" +
			"project file (.julius/filters.toml) and the user file\n" +
			"(<user config dir>/julius/filters.toml), whichever exist.\n\n" +
			"A file that fails to parse or compile is a failure here — the same\n" +
			"problem that at runtime only surfaces as a skip-warning on stderr.\n" +
			"Exits non-zero on any failure, so it works as a CI gate for teams\n" +
			"versioning project filters.",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := args
			if len(paths) == 0 {
				paths = defaultFilterFiles()
				if len(paths) == 0 {
					fmt.Println(ui.Dim("no custom filter files found — looked for .julius/filters.toml and " + userFilterFile()))
					return nil
				}
			}
			reports := testFilterFiles(paths)
			failed := printFilterReports(reports)
			if failed > 0 {
				return exitCodeError(1)
			}
			return nil
		},
	}
}

// defaultFilterFiles returns the project and user filter files that
// exist — the same two tiers the registry loads.
func defaultFilterFiles() []string {
	var paths []string
	for _, p := range []string{filepath.Join(".julius", "filters.toml"), userFilterFile()} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}

func userFilterFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "julius", "filters.toml")
}

// filterFileReport is the outcome of testing one filter file.
type filterFileReport struct {
	Path     string
	ReadErr  error               // file could not be read
	ParseErr error               // file read but failed to parse/compile
	Results  []filter.TestResult // inline test outcomes, in filter order
	NoTests  []string            // filters that declare no tests
}

// Failures counts everything that must fail the run: unreadable files,
// parse/compile errors, and failing test cases.
func (r filterFileReport) Failures() int {
	if r.ReadErr != nil || r.ParseErr != nil {
		return 1
	}
	n := 0
	for _, res := range r.Results {
		if !res.Pass {
			n++
		}
	}
	return n
}

// testFilterFiles parses each file and runs every inline test.
func testFilterFiles(paths []string) []filterFileReport {
	reports := make([]filterFileReport, 0, len(paths))
	for _, path := range paths {
		rep := filterFileReport{Path: path}
		data, err := os.ReadFile(path)
		if err != nil {
			rep.ReadErr = err
			reports = append(reports, rep)
			continue
		}
		specs, err := filter.ParseFile(data)
		if err != nil {
			rep.ParseErr = err
			reports = append(reports, rep)
			continue
		}
		for _, s := range specs {
			if len(s.Tests) == 0 {
				rep.NoTests = append(rep.NoTests, s.Name())
				continue
			}
			rep.Results = append(rep.Results, s.RunTests()...)
		}
		reports = append(reports, rep)
	}
	return reports
}

// printFilterReports renders the reports and returns the failure count.
func printFilterReports(reports []filterFileReport) int {
	failed, passed, untested := 0, 0, 0
	for _, rep := range reports {
		fmt.Printf("%s\n", ui.Title(rep.Path))
		switch {
		case rep.ReadErr != nil:
			fmt.Printf("  %s  %v\n", ui.Bad("FAIL"), rep.ReadErr)
		case rep.ParseErr != nil:
			fmt.Printf("  %s  %v\n", ui.Bad("FAIL"), rep.ParseErr)
		case len(rep.Results) == 0 && len(rep.NoTests) == 0:
			fmt.Printf("  %s\n", ui.Dim("no filters defined"))
		default:
			for _, res := range rep.Results {
				if res.Pass {
					fmt.Printf("  %s  %s/%s\n", ui.Good("PASS"), res.Filter, res.Test)
				} else {
					fmt.Printf("  %s  %s/%s\n", ui.Bad("FAIL"), res.Filter, res.Test)
					fmt.Printf("        --- got ---\n%s\n        --- want ---\n%s\n", indent(res.Got), indent(res.Want))
				}
			}
			for _, name := range rep.NoTests {
				fmt.Printf("  %s  %s has no tests\n", ui.Warn("NOTE"), name)
			}
		}
		failed += rep.Failures()
		for _, res := range rep.Results {
			if res.Pass {
				passed++
			}
		}
		untested += len(rep.NoTests)
		fmt.Println()
	}
	summary := fmt.Sprintf("%d passed", passed)
	if failed > 0 {
		summary += fmt.Sprintf(" · %d failed", failed)
	}
	if untested > 0 {
		summary += fmt.Sprintf(" · %d filter(s) without tests", untested)
	}
	if failed > 0 {
		fmt.Println(ui.Bad(summary))
	} else {
		fmt.Println(ui.Good(summary))
	}
	return failed
}

func indent(s string) string {
	if s == "" {
		return "        (empty)"
	}
	return "        " + strings.ReplaceAll(s, "\n", "\n        ")
}
