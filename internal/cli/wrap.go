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

	// stderr: a spec can opt into merging it into the filtered text; otherwise
	// (including every unrecognized command, which has no spec) it passes
	// through untouched.
	raw := outc.Stdout
	if s, ok := f.(*filter.Spec); ok && s.MergeStderr {
		raw = mergeStreams(outc.Stdout, outc.Stderr)
	} else if outc.Stderr != "" {
		os.Stderr.WriteString(outc.Stderr)
	}

	// On failure, stash the full raw output before filtering: the
	// errors-only trim below is only safe when what it drops stays
	// recoverable on disk, and Stash declines tiny outputs and can fail.
	var stashHint string
	if outc.ExitCode != 0 {
		full := outc.Stdout
		if outc.Stderr != "" {
			full += "\n--- stderr ---\n" + outc.Stderr
		}
		stashHint = execx.Stash(full, slugOf(argv), time.Now())
	}

	// Unrecognized commands fall back to the generic engine: keep the
	// diagnostic signal when they fail, compact a JSON payload when they
	// succeed. A recognized command uses its own filter.
	var res filter.Result
	switch {
	case f != nil:
		res = filter.Finalize(raw, f.Apply(raw, outc.ExitCode))
	case outc.ExitCode != 0 && stashHint != "":
		res = filter.Finalize(raw, filter.ErrorsOnly(raw))
	case outc.ExitCode != 0:
		// No stash means the trim would lose lines unrecoverably.
		res = filter.Result{Output: raw}
	default:
		res = filter.Finalize(raw, filter.CompactJSON(raw))
	}

	// No filter matched and the generic engine found nothing to compress:
	// pure passthrough, and don't log a no-op to the savings ledger.
	if f == nil && !res.Applied {
		os.Stdout.WriteString(outc.Stdout)
		return outc.ExitCode
	}

	output := strings.TrimRight(res.Output, "\n")

	var rawPath string
	if stashHint != "" {
		rawPath = strings.TrimPrefix(stashHint, "[julius] raw output: ")
		if output != "" {
			output += "\n"
		}
		output += stashHint
	}

	if output != "" {
		fmt.Println(output)
	}

	// Commands with no output (git commit -q, silent git add) have nothing
	// to save; recording them would only pollute the savings statistics
	// with the one-token acks the filter emits.
	if before := tokens.Estimate(raw); before > 0 {
		record(ledger.HookEvent{
			// Claude Code exports the session id into the Bash tool's
			// environment; empty outside a session, and per-session views
			// exclude (and disclose) unattributed rows rather than guess.
			SessionID:    os.Getenv("CLAUDE_CODE_SESSION_ID"),
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
