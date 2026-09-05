#!/bin/sh
# SPEC: _spec/tests/testing-strategy.puml
set -eu

printf 'echo-agent ready > '   # WaitFor marker for the driver
IFS= read -r PROMPT

base="${OPENAI_BASE_URL:-http://ollama:11434/v1}"
model="${PROVEO_LOCAL_MODEL:-gemma4}"
key="${OPENAI_API_KEY:-ollama}"

i=0; while [ "$i" -lt 60 ]; do
  curl -fsS -m 5 "$base/models" >/dev/null 2>&1 && break
  i=$((i + 1)); sleep 2
done

req="$(jq -n --arg m "$model" --arg p "$PROMPT" \
  '{model:$m, messages:[{role:"user", content:$p}], max_tokens:128}')"
resp="$(curl -fsS -m 300 "$base/chat/completions" \
  -H "authorization: Bearer $key" -H 'content-type: application/json' \
  -d "$req" 2>/dev/null || echo '{}')"
content="$(printf '%s' "$resp" | jq -r '.choices[0].message.content // ""')"

mkdir -p /app
printf '%s' "$content" > /app/DONE.txt
printf '\nAGENT_DONE (%s bytes)\n' "$(printf '%s' "$content" | wc -c | tr -d ' ')"
sleep 2
