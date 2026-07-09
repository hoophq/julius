# Example: application-level API metering

julius meters exact, provider-reported token usage for any script or app that
calls an LLM provider — **with no code changes**. Apps opt in by pointing their
provider base URL at the local julius proxy; every call is forwarded verbatim
(streaming included) and the usage lands in `julius savings`, broken down per
app and model.

## Run it

```sh
./run.sh
```

That's the whole thing. It runs offline (no API keys) by standing up two
stand-in providers that speak the real Anthropic and OpenAI wire formats, then
sends three unmodified apps through the proxy:

- **support-bot** — real Anthropic SDK, non-streaming
- **rag-pipeline** — real Anthropic SDK, streaming
- **nightly-batch** — a cron-style `curl` job hitting OpenAI

It uses an isolated ledger, so your real `julius savings` data is untouched.

Expected tail:

```
API usage · exact, provider-reported · last 1d

  calls 6   in 30.4k   out 1.1k   cache read 24.6k / write 1.0k

  app              model                    in       out   calls
  nightly-batch    gpt-5                 15.6k       288       3
  rag-pipeline     claude-opus-4-8        9.1k       388       1
  support-bot      claude-opus-4-8        5.7k       428       2
```

## The only integration

Look at any app in `apps/` — there is no julius import. The entire wiring is
two environment variables:

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:4141/anthropic
export OPENAI_BASE_URL=http://127.0.0.1:4141/openai/v1
```

...plus an optional `X-Julius-App` header to label each app's traffic. Untagged
traffic shows up as `default`.

## Against real providers

Swap the offline providers for the real ones — nothing else changes:

```sh
julius proxy serve &
export ANTHROPIC_BASE_URL=http://127.0.0.1:4141/anthropic
export OPENAI_BASE_URL=http://127.0.0.1:4141/openai/v1
python your_app.py        # your real app, unchanged
julius savings
```

## What this is good for

- **Cost attribution** — which service, team, or feature is spending, per model, across providers, in one place.
- **Spotting waste** — a zero in the cache-read column on an app with large repeated prompts means an unclaimed ~90% discount; an expensive model on trivial calls stands out in the mix.
- **Per-environment metering** — tag staging vs production (or per release) and compare spend deltas.

Today the proxy meters exactly; request-path compression (turning this surface
from measurement into savings) is on the roadmap.
