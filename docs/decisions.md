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

## Savings are reported per kind (ATR-154, 2026-07-22)

The savings headline used to lump three different sources under
"commands" — wrapper-filtered commands, native-tool compression, and
session dedup — and the top-commands table mixed `read /path`
pseudo-commands in with real commands. Corrected: three labeled estimate
sections, and the top table draws only from command kinds. Unknown kinds
and pre-attribution rows render as "unattributed" — reported as
recorded, never folded into a known bucket.

`--json` (versioned, v1) carries an explicit `basis` per section —
`"estimate"` or `"provider_exact"` — so consumers cannot blend the two
accounting regimes. Dollar figures are deliberately absent from JSON:
pricing is dated, and the labeled cost line stays a human-surface
rendering. JSON mode fails closed: a consumer cannot tell an omitted
section from an errored query, so any query failure fails the command
rather than shipping valid-looking JSON with a section silently missing.

Per-session views (`--current` via `CLAUDE_CODE_SESSION_ID`, verified
present in the Bash tool environment; `--session <id>` otherwise) scope
the estimate sections only — API usage and proxy compression are
app-scoped and omitted rather than misattributed. The wrapper now
records the session id from that same variable; rows recorded without
attribution are excluded from session views and disclosed by count,
never guessed into a session. Session totals include subagent activity:
subagents run inside the session, and their savings belong to it.

## Read results are never rewritten (2026-07-08)

The PostToolUse processor compresses Bash, Grep (content mode), and Glob
results, but deliberately leaves Read untouched: agents build exact-match
edits from the file content they read, and any rewriting of that content
(stripped lines, shifted line numbers) risks corrupting subsequent edits.
Savings on file reads come from session-level deduplication of repeated
reads instead, where the agent already holds the identical content in
context.

## RTK generic-utility parity (ATR-144, 2026-07-17)

Beyond its per-command filters, RTK ships generic wrappers — `rtk err`,
`rtk summary`, `rtk json`, `rtk env -f`, `rtk deps`. julius's generic engine
already covers part of this implicitly (repeated-line dedup, bounding), so
each was a scope decision before an implementation. What julius adopts, and
what it deliberately does not:

**`json` — covered (generic).** A single JSON document on stdout is compacted
by shape — arrays capped, long leaf values trimmed, null fields dropped, ids
and urls protected — with an honest disclosure marker (`filter.CompactJSON`).
It runs on unrecognized commands in both surfaces (the wrapper's fallback and
the PostToolUse hook), and recognized JSON-emitting commands opt in per filter
via `compact_json` (curl). This also shrinks minified single-line responses a
line cap can't touch. Note the hook itself does no command recognition — it
compacts any not-already-wrapped JSON stdout, relying on the PreToolUse
rewrite to have routed recognized commands through their own filters; in a
post-hook-only install, aws/kubectl `-o json` output would reach the generic
compactor too.

- *Won't cover: aws/kubectl `-o json`.* Their filters stay on conservative,
  value-preserving line caps. Structural compaction reformats (sorts keys,
  drops nulls, trims long values), which conflicts with the standing "never
  reformat cloud/infra values" guideline — for those tools julius must add no
  exposure and alter no value the command printed, so the line cap stands.

**`err` — covered (wrapper only).** For an *unrecognized* command that exits
non-zero, the wrapper trims stdout to its diagnostic signal — lines matching
an error/warning pattern plus a bounded tail — instead of raw passthrough
(`filter.ErrorsOnly`). The trim applies only when the full raw output was
successfully stashed to disk with a pointer (tiny outputs and failed writes
pass through raw instead), so nothing is ever lost.

- *Won't cover: the PostToolUse/agent surface.* The Bash tool response carries
  no exit code (verified shape: `{stdout, stderr, interrupted, isImage,
  noOutputExpected}`), so the hook cannot tell success from failure. Inferring
  it heuristically (stderr + error patterns) would drop legitimate output from
  a command that merely printed the word "error" — a direct violation of the
  never-lossy-where-it-matters guarantee. Errors-only therefore lives only
  where the exit code is known.

**`env` — won't cover.** The motivating win was redact-by-default for secret
safety; that was descoped. Without redaction, bare `env`/`printenv` is a flat
KEY=value list the agent explicitly asked to see, and line-capping it would
hide requested variables for marginal savings. Passthrough — with the generic
never-larger guard — is the honest default. Recorded as `wont-cover` in
`catalog.toml`.

**`summary` — won't cover.** A heuristic natural-language summary of arbitrary
output is lossy by construction and can drop or distort the one line that
mattered. julius compresses by removing known noise and bounding volume, never
by paraphrasing.

**`deps` — won't cover.** A dependency view is already delivered by the
per-package-manager filters (npm/pnpm/yarn, pip, bundler, cargo, uv, …), each
tuned to its tool's output. A generic `deps` wrapper would duplicate them with
less precision.

## Session-dedup provenance and terminal-only wrapping (ATR-147/148/149, 2026-07-20)

A field report — the same content re-entering context despite a dedup
marker claiming it was "above", traced to a doubled hook registration —
forced a pass over what the session cache is allowed to promise. Five
decisions came out of it — four below, and a fifth (per-agent-context
cache scoping) that graduated to its own section further down:

**Cache entries record provenance.** Every entry stores the form the
output actually entered context in (verbatim, filtered, or diff) plus the
originating `tool_use_id`. Suppression only fires against verbatim
referents: "see above" is a lie when "above" was a filtered rendering of
the content, because the agent never held the bytes the marker claims it
did. Filtered and diff entries can never back a dedup
marker — a repeated read passes the fresh output through (possibly
filtered again) instead of being suppressed; those entries exist only to
make duplicate deliveries of the same event idempotent.

**The same event never self-dedups.** Hook events carry an id, and an
invocation that sees its own event id already recorded is a no-op rather
than a "duplicate" hit. This makes doubled hook registration — julius in
settings.json *and* via a plugin, the exact configuration found in the
field — a supported, detected state instead of a corruption source:
doctor names both sources and warns about the wasted round-trip, but the
output stays correct either way.

**Bash dedup markers carry a stash pointer.** A rerun marker now links
the stashed raw output (`[julius] raw output: <path>`), extending the
rule the ErrorsOnly trim established above: the wrapper surface may only
shorten what it can point back to. No stash, no marker — if the raw
output failed to land on disk, the agent gets the real output again.

**Only the terminal pipeline segment is wrapped.** In `a | b | c`, the
stdout of `a` and `b` is program input for the next stage, not agent
context — filtering it would hand `b` different bytes than the user's
pipeline produced. julius therefore wraps only the segment whose output
the model will actually read. Accepted cost: no savings on interior pipe
stages, and they are excluded from scan/coverage accounting rather than
counted as misses.

## Per-agent-context cache scoping (ATR-150, 2026-07-22)

Subagents share a session id with their parent but not a context window,
so a parent-side cache hit could suppress output a subagent never saw.
`session.OpenScoped` now closes that gap: events carrying an `agent_id`
get their own cache directory, events without one keep the plain session
scope.

The discriminator was validated against live PostToolUse payloads
(captured 2026-07-22, sessions with one and with two subagents):

- Main-context events carry **no `agent_id` key at all**; every subagent
  event carries a stable `agent_id` (plus `agent_type`), unique per
  subagent within the session.
- `transcript_path` does **not** discriminate: subagent events carry the
  parent's transcript path verbatim, so it takes no part in scoping.
- Payloads from Claude Code versions predating `agent_id` decode to an
  empty discriminator and keep today's session-wide scope — degraded, not
  broken.

The trade-off — scoping gives up cross-agent suppression — was measured
before accepting: across all local transcripts, 313 of 343 delivered
dedup markers (91%) landed in subagent contexts, and in every captured
case the falsely-suppressed subagent immediately re-read the file with
offset/limit. Cross-agent "savings" were therefore largely fake — the
agent paid the marker, the full content, and a wasted round-trip. Honest
scoping is the default with no opt-out.

Hardening that shipped with and after it (directory scheme revised
2026-07-22, post-merge review):

- **Derived directories live in a namespace no session id can reach.**
  An agent-context directory is `sanitize(sessionID) + "." + hash` — the
  discriminator is appended *after* sanitize, and `.` is a rune sanitize
  never emits, so no raw session id (however crafted, e.g. one shaped
  like `otherSession-<hex>`) can name another context's directory, and
  no length cap can truncate the suffix away. The first shipped scheme
  (`sessionID + "-" + hash` fed through sanitize) had both weaknesses.
- **The discriminator is 64-bit and covers the raw session id.** 16 hex
  chars of `sha256(sessionID || agentID)`: sibling collisions stay
  negligible even in thousand-agent sessions, sessions whose sanitized
  names collide (overlong ids sharing a 64-rune prefix) still get
  distinct agent scopes, and the same agent id in two sessions never
  shares a directory.
- **The entry magic bumped (julius1 → julius2).** Entries written before
  scoping may have been committed by a subagent into the shared session
  scope, so they can no longer attest same-context provenance. They now
  load as FormUnknown — one forfeited dedup per key after upgrading,
  never a marker backed by the wrong context. Directories under the
  short-lived `sid-<hex8>` scheme are simply orphaned and age out with
  the normal purge; a fresh scope means one passed-through read, never a
  wrong marker.

Purging is unaffected: each scope is one flat directory under the session
root, aged independently by the same 7-day rule. A nested layout
(`<sid>/agents/<hash>`) would have separated the namespaces too, but was
rejected: PurgeOld ages top-level directories by their own mtime, and
writes deep in a nested tree do not refresh the session root — an
actively-writing subagent could have its cache purged mid-session. Flat
dot-separated directories keep every scope's mtime its own.
