package cli

import (
	"fmt"
	"os"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/ledger"
	"github.com/hoophq/julius/internal/proxy"
	"github.com/spf13/cobra"
)

func newProxyCmd() *cobra.Command {
	proxyCmd := &cobra.Command{
		Use:   "proxy",
		Short: "Local LLM API proxy with exact usage metering",
	}
	var port int
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the proxy (pass-through + metering; tool-result compression via JULIUS_COMPRESS_APPS)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// One ledger handle for the server's lifetime — a long-running
			// process must not open/close SQLite per request.
			l, err := ledger.Open(ledger.DefaultPath())
			if err != nil {
				return err
			}
			defer l.Close()
			var compress *proxy.Compressor
			if apps := proxy.CompressApps(); len(apps) > 0 {
				wd, _ := os.Getwd()
				compress = proxy.NewCompressor(apps, filter.Load(wd), func(appTag string, s proxy.CompressSaving) {
					err := l.RecordProxySaving(ledger.ProxySaving{
						AppTag: appTag, Provider: s.Provider,
						TokensBefore: s.TokensBefore, TokensAfter: s.TokensAfter,
					})
					if err != nil {
						fmt.Fprintf(os.Stderr, "[julius] savings record failed (%s): %v\n", appTag, err)
					}
				})
			}
			return proxy.Serve(port, func(appTag string, u proxy.Usage) {
				err := l.RecordAPICall(ledger.APICall{
					AppTag: appTag, Provider: u.Provider, Model: u.Model,
					Input: u.Input, Output: u.Output,
					CacheRead: u.CacheRead, CacheWrite: u.CacheWrite,
				})
				if err != nil {
					// Metering never blocks traffic, but dropped usage rows
					// must be visible to the operator.
					fmt.Fprintf(os.Stderr, "[julius] usage record failed (%s %s): %v\n", appTag, u.Model, err)
				}
			}, compress)
		},
	}
	serve.Flags().IntVar(&port, "port", 4141, "port to listen on (localhost only)")
	proxyCmd.AddCommand(serve)
	return proxyCmd
}
