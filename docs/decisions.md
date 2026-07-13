# Engineering decisions

## Hook interception strategy (validated 2026-07-08)

julius integrates with Claude Code through two hook events, each verified
against a live session before being built on:

**PreToolUse input rewriting** (`julius hook claude-pre`) — shipped.
Bash commands with a julius filter are rewritten (`git status` →
`julius git status`) via `hookSpecificOutput.updatedInput`. Measured hook
round-trip latency: p50 5.5ms / p90 5.9ms / max 9.9ms over 50 runs — well
under the 10ms budget, imperceptible per command. Verified live: the model
issues the plain command, the transcript records the rewritten one, and the
compressed output is what enters context.

**PostToolUse output replacement** (`julius hook claude-post`) — shipped 2026-07-08.
`hookSpecificOutput.updatedToolOutput` fully replaces the tool output the
model sees; validated with a 46.9KB replacement payload delivered without
truncation or loss. Findings that constrain the design:

- The 10,000-character cap on plain hook stdout / `additionalContext` does
  NOT apply to `updatedToolOutput` content.
- Replacements larger than the harness's inline threshold are persisted to
  a file with a short inline preview (the standard large-tool-result flow).
  Compressed julius output should stay under that threshold so results are
  fully inline — which compression achieves by construction.
- Both hook events fire on the same Bash call, so the PostToolUse processor
  must skip commands already rewritten to `julius ...` (no double
  filtering), skip small outputs, and apply the engine's never-larger
  guard unconditionally.

## Cost estimates only on the exact surface (2026-07-13)

`julius savings` converts tokens to dollars only for the API-usage
surface, where token counts are exact and the model is known per call.
The hook and compression surfaces stay token-only: their counts are
estimates, and pricing an estimate would present a fabricated number as
money.

The rate table is the one estimated input, so its uncertainty is made
explicit rather than hidden:

- Every cost is labeled with the table's as-of date.
- Models absent from the table render as "—" and are counted in a
  disclosure note — never priced by guesswork. The builtin table only
  carries rates confirmed against provider pricing pages on the as-of
  date. Prefix matching exists solely for dated snapshots (a '-' or '@'
  separator followed by a date-shaped tail); a named variant like
  "-mini" is a differently-priced model and stays unpriced rather than
  inheriting its base entry's rate.
- The caching figure is net: read-side savings minus the write premium
  paid above the input rate. A window where writes outweigh reads shows
  a negative net rather than hiding it.
- A user table (`<user config dir>/julius/pricing.toml` or
  `$JULIUS_PRICING`) replaces the builtin table entirely — no merging —
  so the provenance of every rate is unambiguous. `julius pricing`
  shows the active table and its source.

Provider reporting semantics are normalized at pricing time, not in the
ledger: Anthropic's `input_tokens` exclude cache reads/writes while
OpenAI's `prompt_tokens` include cached tokens, and the ledger stores
each provider's numbers verbatim. Known imprecision that stays
documented instead of modeled: Anthropic 1-hour cache writes bill at 2×
input but the provider-reported aggregate can't be told apart from
5-minute writes (the table prices writes at the 5-minute 1.25× rate),
and time-boxed introductory pricing is baked in at the table's as-of
date.

## Read results are never rewritten (2026-07-08)

The PostToolUse processor compresses Bash, Grep (content mode), and Glob
results, but deliberately leaves Read untouched: agents build exact-match
edits from the file content they read, and any rewriting of that content
(stripped lines, shifted line numbers) risks corrupting subsequent edits.
Savings on file reads come from session-level deduplication of repeated
reads instead, where the agent already holds the identical content in
context.
