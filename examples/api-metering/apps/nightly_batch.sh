#!/bin/sh
# nightly-batch — a cron-style job hitting OpenAI with plain curl (no SDK).
# Integration is a single env var: OPENAI_BASE_URL (set by run.sh).
for i in 1 2 3; do
  curl -s "$OPENAI_BASE_URL/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer demo-key" \
    -H "X-Julius-App: nightly-batch" \
    -d '{"model":"gpt-5","messages":[{"role":"user","content":"summarize ticket batch '"$i"'"}]}' >/dev/null
done
echo "[nightly-batch] processed 3 ticket batches"
