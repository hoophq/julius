package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hoophq/julius/internal/execx"
	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/ledger"
	"github.com/hoophq/julius/internal/tokens"
)

// wrap executes argv and prints the filtered output, preserving the
// child's exit code. This is the core command-surface path.
func wrap(argv []string) int {
	cwd, _ := os.Getwd()
	reg := filter.Load(cwd)
	cmdline := strings.Join(argv, " ")
	f := reg.Pick(cmdline)

	outc, err := execx.Run(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "julius: %v\n", err)
		return outc.ExitCode
	}

	if f == nil {
		// Passthrough: julius has no filter for this command.
		os.Stdout.WriteString(outc.Stdout)
		os.Stderr.WriteString(outc.Stderr)
		return outc.ExitCode
	}

	raw := outc.Stdout
	if s, ok := f.(*filter.Spec); ok && s.MergeStderr {
		raw = outc.Stdout + outc.Stderr
	} else if outc.Stderr != "" {
		os.Stderr.WriteString(outc.Stderr)
	}

	res := filter.Finalize(raw, f.Apply(raw, outc.ExitCode))
	output := strings.TrimRight(res.Output, "\n")

	var rawPath string
	if outc.ExitCode != 0 {
		full := outc.Stdout
		if outc.Stderr != "" {
			full += "\n--- stderr ---\n" + outc.Stderr
		}
		if hint := execx.Stash(full, slugOf(argv), time.Now()); hint != "" {
			rawPath = strings.TrimPrefix(hint, "[julius] raw output: ")
			if output != "" {
				output += "\n"
			}
			output += hint
		}
	}

	if output != "" {
		fmt.Println(output)
	}

	record(ledger.HookEvent{
		Kind:         "command",
		Tool:         "cli",
		Command:      cmdline,
		TokensBefore: tokens.Estimate(raw),
		TokensAfter:  tokens.Estimate(res.Output),
		RawPath:      rawPath,
	})

	return outc.ExitCode
}

// record persists a ledger event, best-effort: analytics never break a
// command, so every failure path is silently dropped.
func record(ev ledger.HookEvent) {
	l, err := ledger.Open(ledger.DefaultPath())
	if err != nil {
		return
	}
	defer l.Close()
	_ = l.RecordHookEvent(ev)
}

func slugOf(argv []string) string {
	if len(argv) == 1 {
		return argv[0]
	}
	return argv[0] + "-" + argv[1]
}
