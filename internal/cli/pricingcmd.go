package cli

import (
	"fmt"
	"sort"

	"github.com/hoophq/julius/internal/pricing"
	"github.com/hoophq/julius/internal/ui"
	"github.com/spf13/cobra"
)

func newPricingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pricing",
		Short: "Show the active model pricing table",
		Long: "Show the per-model rate table `julius savings` uses to estimate API cost.\n" +
			"A table in the same TOML format at <user config dir>/julius/pricing.toml\n" +
			"(or a path in $JULIUS_PRICING) replaces the builtin table entirely.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tbl, err := pricing.Load()
			if err != nil {
				fmt.Printf("%s\n\n", ui.Warn(fmt.Sprintf("override ignored: %v", err)))
			}
			src := tbl.Source
			if src == "builtin" {
				src = "builtin table"
			}
			fmt.Printf("%s %s\n\n", ui.Title("Model pricing"),
				ui.Dim(fmt.Sprintf("· USD per 1M tokens · as of %s · %s", tbl.AsOf, src)))

			names := make([]string, 0, len(tbl.Models))
			for name := range tbl.Models {
				names = append(names, name)
			}
			sort.Strings(names)
			fmt.Printf("  %s\n", ui.Dim(fmt.Sprintf("%-24s %8s %8s %11s %12s", "model", "input", "output", "cache read", "cache write")))
			for _, name := range names {
				r := tbl.Models[name]
				fmt.Printf("  %-24s %8.2f %8.2f %11.3f %12.3f\n",
					truncate(name, 24), r.Input, r.Output, r.CacheRead, r.CacheWrite)
			}
			if p := pricing.OverridePath(); tbl.Source == "builtin" && p != "" {
				fmt.Printf("\n  %s\n", ui.Dim("override path: "+p))
			}
			return nil
		},
	}
}
