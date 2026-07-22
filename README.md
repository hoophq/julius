# julius

Cut LLM token usage where it actually burns: dev-command output entering your coding agent's context, and API traffic from your scripts and apps.

[![ci](https://github.com/hoophq/julius/actions/workflows/ci.yml/badge.svg)](https://github.com/hoophq/julius/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/hoophq/julius)](https://github.com/hoophq/julius/releases/latest)
[![license](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Coding agents spend most of their context window on low-signal output — test runners echoing every passing test, package managers narrating downloads, the same file read three times in one session. You pay for those tokens on every subsequent request, because the whole conversation is resent each turn.

julius sits between the noise and the model:

- **Command-output compression** — dev commands return a compressed, high-signal version of their output: 60+ built-in filters covering git, test runners, linters, package managers, docker/kubectl, cloud and infra CLIs (aws, gh, terraform), build tools, and shell utilities. **Typically 60–90% savings on supported commands**, measured, with the raw output of failing commands and deduplicated reruns stashed to disk and linked.
- **Native tool interception** — file re-reads collapse to a marker when nothing changed (or a diff when something did), repeated command outputs dedupe, search results get bounded, and (opt-in) MCP tool outputs are compacted. This works on your agent's built-in tools, not just shell commands.
- **API usage metering** — point any script at the julius local proxy (`ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL`) and get exact, provider-reported token usage per app and model. No code changes, no TLS tricks, payloads forwarded byte-for-byte — unless an app opts into tool-result compression (below).

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
julius init -g      # global: hooks in ~/.claude/settings.json, active in every project
julius doctor       # verifies everything is wired up
```

To scope julius to a single project instead, run `julius init` (no flag) inside it — the hooks land in that project's `.claude/settings.json` only. Add `--mcp` to either form to also compress MCP tool outputs (opt-in — see below); re-running init upgrades an existing install in place.

Start a new session and work normally. Commands the agent runs are rewritten through julius transparently; native Read/Grep/Glob results are deduplicated and bounded. Then:

```sh
julius savings
```

```
Command-output savings · estimates · last 30d

  commands   214   tokens 182.4k → 31.1k
  saved      151.3k  83%  ████████████████████░░░░

  command                         runs    saved   avg%
  go test -v ./...                  12    48.2k   93%  ▪▪▪▪▪▪▪▪▪▪
  npm install                        4    22.1k   96%  ▪▪▪▪
  ...
```

## How it works

julius integrates with Claude Code through two hooks:

1. **Before a command runs**, julius rewrites it to run through the julius wrapper (`git status` → `julius git status`) — the wrapper executes the real command and filters its output. Your permission rules are respected: denied commands are never touched, ask-rules still prompt. Only the terminal segment of a pipeline is wrapped — data flowing between pipe stages is never filtered.
2. **After a native tool runs** (Read, Grep, Glob, or an unwrapped command), julius compresses the result before it enters context: format-aware filtering for recognized outputs, repeated-line dedup for logs, and dedup of repeated reads scoped per agent context — a subagent never gets a marker pointing at content only its parent saw. Duplicate deliveries of the same hook event (as reported by Claude Code's `tool_use_id`) are handled idempotently: if two integrations (settings.json and a plugin) both invoke julius on that event, the second invocation is a no-op.

Opt-in, the same post hook can also compress **MCP tool outputs** (`julius init --mcp`): JSON results are compacted — null fields dropped, long lists capped, embedded documents truncated — with every removal disclosed in a marker line. Ids and urls always survive intact, error results and non-JSON text are never touched.

Three guarantees hold everywhere:

- **Never larger** — if filtering doesn't shrink the output, you get the original.
- **Never lossy where it matters** — errors, failures, and warnings are kept; fresh file content is never rewritten; the raw output of failing commands is stashed to disk and linked (`[julius] raw output: <path>`); and a dedup marker is only ever issued when the referenced output actually entered context verbatim — a Bash rerun marker links the stashed raw output (`[julius] raw output: <path>`); when the referent wasn't emitted verbatim, julius passes the fresh output through (possibly filtered) instead of suppressing it.
- **Honest accounting** — command-surface numbers are estimates and labeled as such; proxy-surface numbers are exact and provider-reported. The two are never blended.

## API usage metering

```sh
julius proxy serve                 # localhost:4141

export ANTHROPIC_BASE_URL=http://127.0.0.1:4141/anthropic
export OPENAI_BASE_URL=http://127.0.0.1:4141/openai/v1
python my_pipeline.py              # unchanged
```

Every call is forwarded verbatim — streaming included — and the provider-reported usage lands in `julius savings`, broken down per app (`X-Julius-App` header) and model.

### Cost estimates

`julius savings` prices the exact usage through a per-model rate table and shows what the traffic cost — and the net effect of caching (read savings minus write premiums):

```
  calls 6   in 30.4k   out 1.1k   cache read 24.6k / write 1.0k
  cost  ~$0.11 spent   ~$0.06 saved by caching   · estimate · pricing as of 2026-07-13
```

The tokens are exact; the dollar figures are estimates because prices change — every cost is labeled with the rate table's as-of date, and models missing from the table render as `—` rather than being guessed. Cost applies only to this surface: the command and compression numbers are token estimates, and pricing an estimate would be a made-up number.

`julius pricing` shows the active table. To use your own rates (negotiated pricing, new models, batch discounts), write a table in the same TOML format to `<user config dir>/julius/pricing.toml` or point `JULIUS_PRICING` at one — it replaces the builtin table entirely, so there is never a question of which rate applied.

### Tool-result compression (opt-in)

Agents resend their accumulated tool results with every request. Opt an app in and the proxy runs the same filter engine over that resent content before it reaches the provider:

```sh
JULIUS_COMPRESS_APPS=my-agent julius proxy serve   # comma-separated tags, or "*" for all apps
```

Only tool results are touched — Anthropic `tool_result` blocks and OpenAI `role:"tool"` messages. System prompts, user text, tool-call arguments, error results, and image/document blocks always pass through untouched, and any body that doesn't parse as JSON is forwarded verbatim. The savings are estimates and get their own section in `julius savings`, never mixed into the exact metering numbers.

### Prompt-cache hints (opt-in)

Anthropic prompt caching bills repeated request prefixes at ~10% of the input price — but only when the request opts in. Many apps never do. Opt an app in and the proxy adds the hint for it:

```sh
JULIUS_CACHE_APPS=my-agent julius proxy serve      # comma-separated tags, or "*" for all apps
```

The mutation is a single top-level `cache_control: {type: ephemeral}` field on Anthropic `/v1/messages` requests — Anthropic's auto-caching form, where the server itself places the breakpoint. Requests that already use `cache_control` anywhere are never touched (the app is managing its own caching), other providers and endpoints pass through, and anything that doesn't parse as JSON is forwarded verbatim. Nothing is estimated: the effect shows up as provider-reported cache read/write tokens in the exact metering. Note the tradeoff caching itself carries — cache writes bill at 1.25×, reads at ~0.1×, so it pays off from the second request on a stable prefix (which is exactly what agent loops resend).

## More commands

```sh
julius scan               # replay filters on session history: measured missed savings
julius pricing            # show the model rate table behind cost estimates
julius filters test       # run the inline tests in your custom filter files
julius <any command>      # run anything through the filter engine manually
julius raw <command>      # escape hatch: run with no filtering
```

## Custom filters

Drop project-specific filters in `.julius/filters.toml`, or user-wide ones in `<user config dir>/julius/filters.toml` — same declarative format as the [built-in catalog](internal/filter/builtin/), documented in [docs/filters.md](docs/filters.md). Filters are regex pipelines with inline tests; run them with `julius filters test` (non-zero exit on failure, CI-friendly), and the engine enforces the never-larger guarantee on top of whatever you write.

## Scope, honestly

- Savings on command output depend on the command: verbose output (tests, installs, builds) compresses 90%+; already-terse output has little to save, and julius won't pretend otherwise.
- The proxy meters exactly, and — opt-in per app — compresses resent tool results and injects Anthropic prompt-cache hints in the request path. Compression savings are estimates and reported separately; cache effects are provider-reported and appear in the exact usage numbers.
- v1 integrates with Claude Code. More agents are planned.

## License

MIT © [hoop.dev](https://hoop.dev/start?utm_source=julius&utm_medium=github&utm_campaign=att-launch-072026) — built by the team behind [hoop.dev](https://hoop.dev/start?utm_source=julius&utm_medium=github&utm_campaign=att-launch-072026).
