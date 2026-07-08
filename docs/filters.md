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

[[filters.my-tool.tests]]           # inline tests, run by `go test`
name = "clean build collapses"
input = """
Downloading dependency graph
Downloading artifacts
"""
want = "my-tool build: ok"
```

## Pipeline stages

Stages run in this order; every field is optional except `command`:

| Field | Effect |
|---|---|
| `strip_ansi` | Remove ANSI escape sequences |
| `merge_stderr` | Include stderr in the filtered text (default: stderr passes through untouched) |
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
- On failing commands, the full raw output is stashed to disk and the filtered
  output carries a `[julius] raw output: <path>` pointer.

## Guidelines

- **Keep errors.** Drop progress, keep diagnostics. When in doubt, keep the line —
  the never-larger guard means an overly conservative filter just saves less.
- **Never reformat values** for cloud/infra tools — drop noise lines only, so the
  filter can't surface anything the command didn't already print.
- **Write tests for both directions**: a noisy success collapsing, and a failure
  surviving intact.
- Every built-in filter must ship at least one inline test — the suite fails
  otherwise. The savings gate in `internal/filter/savings_test.go` keeps the
  headline claim honest; add a corpus case if your filter targets a heavyweight.
