# Writing filters

A filter tells julius how to compress one command's output. Most filters are
declarative TOML — no code required. Filters live in three tiers, first match
wins:

1. `.julius/filters.toml` in your project
2. `~/.config/julius/filters.toml` (user-global)
3. the built-in catalog compiled into the binary

## Format

```toml
[filters.my-tool]
description = "my-tool build: errors and outcome only"
command = '^my-tool\s+build\b'     # regex matched against the command line
strip_ansi = true                   # remove ANSI color codes first
merge_stderr = true                 # filter stderr together with stdout
drop_lines = [                      # drop lines matching any of these
  '^Downloading ',
  '^\s*$',
]
if_empty = "my-tool build: ok"      # emitted when everything was dropped

[[filters.my-tool.tests]]           # inline tests (see "Testing filters" below)
name = "clean build collapses"
input = """
Downloading dependency graph
Downloading artifacts
"""
want = "my-tool build: ok"

[[filters.my-tool.tests]]
name = "failure survives"
exit_code = 1                       # the wrapped command's exit code
input = "error: linker failed on module core"
want = "error: linker failed on module core"
```

## Pipeline stages

Stages run in this order; every field is optional except `command`:

| Field | Effect |
|---|---|
| `strip_ansi` | Remove ANSI escape sequences |
| `merge_stderr` | Include stderr in the filtered text (default: stderr passes through untouched) |
| `compact_json` | Structurally compact a JSON body — arrays capped, long values trimmed, null fields dropped — with a disclosure marker. When the body is JSON it short-circuits the line stages below (`replace` onward); non-JSON output falls through to them. Note: `merge_stderr` runs first, and merged stderr text usually makes the body non-JSON |
| `py_traceback` | Collapse CPython traceback blocks to the entry frame, the deepest frames, and every non-frame line, behind a `[julius: N frames omitted]` marker. Rewrites blocks in place and falls through to the stages below; short tracebacks and frame-shaped lines outside a `Traceback` header are never touched |
| `replace` | Line-by-line regex substitutions: `[{ pattern = '...', with = '...' }]` |
| `respond` | Short-circuit: if `pattern` matches anywhere, the whole output becomes `message` (guard with `unless`) |
| `keep_lines` | Keep only lines matching at least one regex |
| `drop_lines` | Drop lines matching any regex |
| `max_line_length` | Truncate long lines (adds `…`) |
| `head` / `tail` | Keep the first/last N lines, with an honest omission marker |
| `if_empty` | Message emitted when filtering left nothing |
| `detect_output` | Regexes matched against raw *output* — lets julius apply this filter when the format is recognized even if the command wasn't (native tool results, unwrapped commands) |

## Guarantees the engine adds

You don't need to be careful — the engine is:

- If your filter produces **more** tokens than the raw output, the raw output wins.
- If your filter empties non-empty output, the raw output wins.
- When a wrapped command fails, the full raw output is stashed to disk and the
  filtered output carries a `[julius] raw output: <path>` pointer.

## Testing filters

Write `[[filters.X.tests]]` cases next to the filter (see the example
above) and run them with:

```sh
julius filters test                     # project + user files, whichever exist
julius filters test path/to/filters.toml
```

Each case runs through the same Apply+Finalize path the wrapper uses and
reports pass/fail with the got/want output on mismatch. A file that fails
to parse or compile fails the run — the same problem that at runtime only
appears as a skip-warning on stderr. The command exits non-zero on any
failure, so teams versioning `.julius/filters.toml` can run it in CI.

Spot-checking against live output also works:

```sh
julius my-tool build        # filtered
julius raw my-tool build    # unfiltered, for comparison
```

A broken pattern can't hurt you either way — the engine guarantees above
catch filters that inflate or empty the output, and a filter file that
fails to parse is skipped with a warning rather than breaking your
commands.

For **built-in filters** (contributions to this repo), the same inline
tests are executed by `go test` and every filter must ship at least one.

## Guidelines

- **Keep errors.** Drop progress, keep diagnostics. When in doubt, keep the line —
  the never-larger guard means an overly conservative filter just saves less.
- **Never reformat values** for cloud/infra tools — drop noise lines only, so the
  filter can't surface anything the command didn't already print.
- **Write tests for both directions**: a noisy success collapsing, and a failure
  surviving intact.
- The savings gate in `internal/filter/savings_test.go` keeps the headline
  claim honest; add a corpus case if your built-in filter targets a heavyweight.
