#!/bin/sh
# One-command demo of julius's application-level API metering.
#
# Spins up two offline stand-in providers (Anthropic + OpenAI wire formats),
# starts the julius proxy pointed at them, runs three unmodified "apps"
# through it, and prints `julius savings` — all against an isolated ledger so
# your real savings data is untouched.
#
#   ./run.sh
#
# To run against the REAL providers instead, skip this script and just:
#   julius proxy serve &
#   export ANTHROPIC_BASE_URL=http://127.0.0.1:4141/anthropic
#   export OPENAI_BASE_URL=http://127.0.0.1:4141/openai/v1
#   python your_app.py     # unchanged
set -eu

cd "$(dirname "$0")"
DIR="$(pwd)"
LEDGER="$DIR/.demo-ledger.db"          # isolated: does not touch ~/.local/share/julius
VENV="$DIR/.venv"
PIDS=""

cleanup() {
  for pid in $PIDS; do kill "$pid" 2>/dev/null || true; done
  rm -f "$LEDGER" "$LEDGER"-shm "$LEDGER"-wal
}
trap cleanup EXIT INT TERM

command -v julius >/dev/null 2>&1 || { echo "julius not on PATH — install it or run from a built binary" >&2; exit 1; }

# Real Anthropic SDK, so the apps are genuinely unmodified SDK code.
if [ ! -x "$VENV/bin/python" ]; then
  echo "== setting up venv (one-time) =="
  python3 -m venv "$VENV"
  "$VENV/bin/pip" install -q anthropic
fi

echo "== starting offline providers (:5001 anthropic, :5002 openai) =="
python3 fake_providers.py & PIDS="$PIDS $!"

echo "== starting julius proxy (:4141, isolated ledger) =="
JULIUS_LEDGER="$LEDGER" \
JULIUS_ANTHROPIC_UPSTREAM="http://127.0.0.1:5001" \
JULIUS_OPENAI_UPSTREAM="http://127.0.0.1:5002" \
  julius proxy serve --port 4141 >/dev/null 2>&1 & PIDS="$PIDS $!"

sleep 2

# THE integration: two env vars. The apps below contain no julius code.
export ANTHROPIC_BASE_URL="http://127.0.0.1:4141/anthropic"
export OPENAI_BASE_URL="http://127.0.0.1:4141/openai/v1"

echo
echo "== running three unmodified apps through julius =="
"$VENV/bin/python" apps/support_bot.py
"$VENV/bin/python" apps/rag_pipeline.py
sh apps/nightly_batch.sh
"$VENV/bin/python" apps/support_bot.py   # a second customer

echo
JULIUS_LEDGER="$LEDGER" julius savings --days 1
