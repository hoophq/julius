# julius

Cut LLM token usage where it actually burns: dev-command output entering your coding agent's context, and API traffic from your scripts and apps.

[![ci](https://github.com/hoophq/julius/actions/workflows/ci.yml/badge.svg)](https://github.com/hoophq/julius/actions/workflows/ci.yml)
[![license](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Coding agents spend most of their context window on low-signal output — test runners echoing every passing test, package managers narrating downloads, the same file read three times in one session. You pay for those tokens on every subsequent request, because the whole conversation is resent each turn.

julius sits between the noise and the model:

- **Command-output compression** — dev commands (git, test runners, linters, docker, package managers) return a compressed, high-signal version of their output. **60–90% savings on supported commands**, measured, with the full raw output always recoverable from disk.
- **Native tool interception** — file re-reads collapse to a marker when nothing changed (or a diff when something did), repeated command outputs dedupe, search results get bounded. This works on your agent's built-in tools, not just shell commands.
- **API usage metering** — point any script at the julius local proxy (`ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL`) and get exact, provider-reported token usage per app and model. No code changes, no TLS tricks, payloads forwarded byte-for-byte.

## Install

```sh
# Homebrew
brew install hoophq/tap/julius

# or curl
curl -fsSL https://raw.githubusercontent.com/hoophq/julius/main/install.sh | sh

# or from source
go install github.com/hoophq/julius/cmd/julius@latest
```

## Quickstart (Claude Code)

```sh
julius init -g      # registers the hooks in ~/.claude/settings.json
julius doctor       # verifies everything is wired up
```

Start a new session and work normally. Commands the agent runs are rewritten through julius transparently; native Read/Grep/Glob results are deduplicated and bounded. Then:

```sh
julius savings
```

```
Command-output savings — estimates, last 30d

  commands: 214   tokens: 182.4k → 31.1k   saved: 151.3k (83%)

  top commands by tokens saved:
    go test -v ./...             48.2k saved   93%  (12 runs)
    npm install                  22.1k saved   96%  (4 runs)
    ...
```

## How it works

julius integrates with Claude Code through two hooks:

1. **Before a command runs**, julius rewrites it to run through the julius wrapper (`git status` → `julius git status`) — the wrapper executes the real command and filters its output. Your permission rules are respected: denied commands are never touched, ask-rules still prompt.
2. **After a native tool runs** (Read, Grep, Glob, or an unwrapped command), julius compresses the result before it enters context: format-aware filtering for recognized outputs, repeated-line dedup for logs, session-level dedup for repeated reads.

Three guarantees hold everywhere:

- **Never larger** — if filtering doesn't shrink the output, you get the original.
- **Never lossy where it matters** — errors, failures, and warnings are kept; fresh file content is never rewritten; the raw output of failing commands is stashed to disk and linked (`[julius] raw output: <path>`).
- **Honest accounting** — command-surface numbers are estimates and labeled as such; proxy-surface numbers are exact and provider-reported. The two are never blended.

## API usage metering

```sh
julius proxy serve                 # localhost:4141

export ANTHROPIC_BASE_URL=http://127.0.0.1:4141/anthropic
export OPENAI_BASE_URL=http://127.0.0.1:4141/openai/v1
python my_pipeline.py              # unchanged
```

Every call is forwarded verbatim — streaming included — and the provider-reported usage lands in `julius savings`, broken down per app (`X-Julius-App` header) and model. Today the proxy meters; request-path compression is on the roadmap.

## More commands

```sh
julius scan               # replay filters on session history: measured missed savings
julius <any command>      # run anything through the filter engine manually
julius raw <command>      # escape hatch: run with no filtering
```

## Custom filters

Drop project-specific filters in `.julius/filters.toml` — same declarative format as the [built-in catalog](internal/filter/builtin/), documented in [docs/filters.md](docs/filters.md). Filters are regex pipelines with inline tests; the engine enforces the never-larger guarantee on top of whatever you write.

## Scope, honestly

- Savings on command output depend on the command: verbose output (tests, installs, builds) compresses 90%+; already-terse output has little to save, and julius won't pretend otherwise.
- The proxy meters exactly; it does not (yet) reduce API-side token usage. That work — request-path compression of resent tool results, cache-hint injection — is next on the roadmap, and it will be opt-in.
- v1 integrates with Claude Code. More agents are planned.

## License

MIT © [hoop.dev](https://hoop.dev) — built by the team behind [hoop.dev](https://hoop.dev).
