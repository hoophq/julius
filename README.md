# julius

Cut LLM token usage where it actually burns: dev-command output entering your coding agent's context, and API traffic from your scripts and apps.

> ⚠️ Under active development — not yet released.

## What it does

- **Command-output compression** — julius intercepts dev commands (git, test runners, linters, package managers) run by your AI coding agent and returns a compressed, high-signal version of their output. 60–90% token savings on supported commands. Full raw output is always recoverable.
- **API usage metering** — point your scripts at the julius local proxy (`ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL`) and get exact, per-app token usage and cost — no code changes.
- **Honest analytics** — `julius gain` reports the two surfaces separately: estimated savings on command output, exact provider-reported usage on the API path.

## Status

v1 targets Claude Code integration via hooks. More agents and request-path compression are on the roadmap.

## License

MIT © [hoop.dev](https://hoop.dev) — built by the team behind [hoop.dev](https://hoop.dev).
