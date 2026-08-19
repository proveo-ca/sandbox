#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_llm.sh - Critical: direct LLM API connection works.
#
# Verifies opencode can authenticate to each provider via env-var alone and
# complete a round-trip. Skips per-provider when the key is absent.

# provider:model:envvar triplets — keep models small/cheap when possible.
PROVIDERS=(
  "anthropic:anthropic/claude-haiku-4-5:ANTHROPIC_API_KEY"
  "openai:openai/gpt-4.1-mini:OPENAI_API_KEY"
  "openrouter:openrouter/anthropic/claude-3.5-haiku:OPENROUTER_API_KEY"
  "xai:xai/grok-2-1212:XAI_API_KEY"
  "google:google/gemini-2.0-flash:GEMINI_API_KEY"
  "groq:groq/llama-3.1-8b-instant:GROQ_API_KEY"
  "deepseek:deepseek/deepseek-chat:DEEPSEEK_API_KEY"
)

for entry in "${PROVIDERS[@]}"; do
  IFS=':' read -r provider model envvar <<< "$entry"
  key_value="${!envvar:-}"

  if [[ -z "$key_value" ]]; then
    skip_test "[$provider] opencode run completes via $envvar" "no $envvar"
    continue
  fi

  TESTS_RUN=$((TESTS_RUN + 1))
  RESULT=$(docker run --rm \
    -e "${envvar}=${key_value}" \
    --entrypoint bash \
    "$IMAGE" -c "timeout 120 opencode run -m '$model' 'Respond with only the word PONG.' 2>&1")
  if echo "$RESULT" | grep -qi "PONG"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] [$provider] opencode run completes via $envvar\n" "$TESTS_RUN"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("[$provider] opencode run completes via $envvar")
    printf "${RED}FAIL${NC} [%d] [$provider] opencode run (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
  fi
done
