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

## Read results are never rewritten (2026-07-08)

The PostToolUse processor compresses Bash, Grep (content mode), and Glob
results, but deliberately leaves Read untouched: agents build exact-match
edits from the file content they read, and any rewriting of that content
(stripped lines, shifted line numbers) risks corrupting subsequent edits.
Savings on file reads come from session-level deduplication of repeated
reads instead, where the agent already holds the identical content in
context.
