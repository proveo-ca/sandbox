#!/bin/sh
# Minimal "agent" fixture for the Layer-4 promptful E2E (see _spec/tests/testing-strategy.puml).
# It behaves like a real harness for the purposes of the test: prints a ready
# prompt, reads a task from stdin (sent via tmux send-keys), calls the LOCAL
# model over the OpenAI-compatible endpoint the --local-model bridge points at
# (the Ollama sidecar), and writes the reply as a side effect. No vendor image
# needed, so Layer 4 runs in CI with only docker + tmux + Ollama.
set -eu

printf 'echo-agent ready > '   # WaitFor marker for the driver
IFS= read -r PROMPT

base="${OPENAI_BASE_URL:-http://ollama:11434/v1}"
model="${PROVEO_LOCAL_MODEL:-gemma4}"
key="${OPENAI_API_KEY:-ollama}"

# Wait for the (freshly-started) Ollama sidecar to accept connections.
i=0; while [ "$i" -lt 60 ]; do
  curl -fsS -m 5 "$base/models" >/dev/null 2>&1 && break
  i=$((i + 1)); sleep 2
done

req="$(jq -n --arg m "$model" --arg p "$PROMPT" \
  '{model:$m, messages:[{role:"user", content:$p}], max_tokens:128}')"
# Long timeout: the first call cold-loads the model into the sidecar's memory.
resp="$(curl -fsS -m 300 "$base/chat/completions" \
  -H "authorization: Bearer $key" -H 'content-type: application/json' \
  -d "$req" 2>/dev/null || echo '{}')"
content="$(printf '%s' "$resp" | jq -r '.choices[0].message.content // ""')"

mkdir -p /app
printf '%s' "$content" > /app/DONE.txt
printf '\nAGENT_DONE (%s bytes)\n' "$(printf '%s' "$content" | wc -c | tr -d ' ')"
sleep 2
