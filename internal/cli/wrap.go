package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hoophq/julius/internal/execx"
	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/ledger"
	"github.com/hoophq/julius/internal/router"
	"github.com/hoophq/julius/internal/tokens"
)

// wrap executes argv and prints the filtered output, preserving the
// child's exit code. This is the core command-surface path.
func wrap(argv []string) int {
	cwd, _ := os.Getwd()
	reg := filter.Load(cwd)
	cmdline := strings.Join(argv, " ")
	// Match on the canonical form (`sudo /usr/bin/git status` → `git
	// status`) so filter selection agrees with what the router routed;
	// execution below uses argv untouched.
	f := reg.Pick(router.MatchTarget(cmdline))

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
		raw = mergeStreams(outc.Stdout, outc.Stderr)
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

	// Commands with no output (git commit -q, silent git add) have nothing
	// to save; recording them would only pollute the savings statistics
	// with the one-token acks the filter emits.
	if before := tokens.Estimate(raw); before > 0 {
		record(ledger.HookEvent{
			Kind:         "command",
			Tool:         "cli",
			Command:      cmdline,
			TokensBefore: before,
			TokensAfter:  tokens.Estimate(res.Output),
			RawPath:      rawPath,
		})
	}

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

// mergeStreams joins stdout and stderr for filters that opt into
// merge_stderr. When stdout is non-empty but not newline-terminated (a
// `wget -O-` body, a minified JSON payload), a separator is inserted so the
// first stderr line can't glue onto the last stdout line — which would both
// corrupt the output and defeat the line-based drop/keep patterns.
func mergeStreams(stdout, stderr string) string {
	if stdout != "" && stderr != "" && !strings.HasSuffix(stdout, "\n") {
		return stdout + "\n" + stderr
	}
	return stdout + stderr
}

func slugOf(argv []string) string {
	if len(argv) == 1 {
		return argv[0]
	}
	return argv[0] + "-" + argv[1]
}
