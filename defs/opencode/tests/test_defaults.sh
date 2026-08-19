#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_defaults.sh - Default opencode.json + subagents are baked in and seeded.

# Defaults are baked at /opt/opencode/defaults
assert_success \
  "baked defaults: opencode.json present in /opt" \
  "$IMAGE" \
  "test -f /opt/opencode/defaults/opencode.json"

REQUIRED_AGENTS=(
  "adversarial-reviewer"
  "security-reviewer"
  "architect"
  "systems-design"
  "frontend"
  "backend"
  "sre"
  "devops"
  "monorepo-coordinator"
  "spec-keeper"
)
for a in "${REQUIRED_AGENTS[@]}"; do
  assert_success \
    "baked defaults: agents/$a.md present in /opt" \
    "$IMAGE" \
    "test -f /opt/opencode/defaults/agents/$a.md"
done

# Default config encodes the HITL permission model
assert_output_contains \
  "default opencode.json: build agent has bash:ask" \
  "$IMAGE" \
  'cat /opt/opencode/defaults/opencode.json' \
  '"bash": "ask"'

assert_output_contains \
  "default opencode.json: plan agent has bash:deny" \
  "$IMAGE" \
  'cat /opt/opencode/defaults/opencode.json' \
  '"bash": "deny"'

assert_output_contains \
  "default opencode.json: context rot enabled" \
  "$IMAGE" \
  'cat /opt/opencode/defaults/opencode.json' \
  '"rot": true'

assert_output_contains \
  "default opencode.json: plan model uses OPENCODE_MODEL" \
  "$IMAGE" \
  'cat /opt/opencode/defaults/opencode.json' \
  '"model": "{env:OPENCODE_MODEL}"'

assert_output_contains \
  "default opencode.json: build model uses OPENCODE_BUILD_MODEL" \
  "$IMAGE" \
  'cat /opt/opencode/defaults/opencode.json' \
  '"model": "{env:OPENCODE_BUILD_MODEL}"'

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(docker run --rm \
  --entrypoint /entrypoint.sh \
  "$IMAGE" --version 2>&1 || true)
if echo "$RESULT" | grep -qE "(Seeded global defaults|already-seeded|opencode version)"; then
  # Now verify the files actually got written
  CHECK=$(docker run --rm \
    --entrypoint bash \
    "$IMAGE" -c '/entrypoint.sh --version >/dev/null 2>&1; test -f "$HOME/.config/opencode/opencode.json" && test -f "$HOME/.config/opencode/agents/adversarial-reviewer.md" && echo OK' 2>&1)
  if echo "$CHECK" | grep -q "^OK$"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] entrypoint seeds ~/.config/opencode on first run\n" "$TESTS_RUN"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("entrypoint seeds ~/.config/opencode on first run")
    printf "${RED}FAIL${NC} [%d] seed check (output: %.300s)\n" "$TESTS_RUN" "$CHECK"
  fi
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint seeds ~/.config/opencode on first run")
  printf "${RED}FAIL${NC} [%d] entrypoint did not run (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# --- OPENCODE_RESEED=1 overwrites user-modified config ---
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(docker run --rm \
  -e OPENCODE_RESEED=1 \
  --entrypoint bash \
  "$IMAGE" -c '
    mkdir -p "$HOME/.config/opencode"
    echo "{ \"model\": \"DIRTY\" }" > "$HOME/.config/opencode/opencode.json"
    /entrypoint.sh --version >/dev/null 2>&1
    grep -q "DIRTY" "$HOME/.config/opencode/opencode.json" && echo NOT_RESEEDED || echo RESEEDED
  ' 2>&1)
if echo "$RESULT" | grep -q "^RESEEDED$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] OPENCODE_RESEED=1 overwrites existing config\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("OPENCODE_RESEED=1 overwrites existing config")
  printf "${RED}FAIL${NC} [%d] OPENCODE_RESEED behaviour (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# --- Without OPENCODE_RESEED, existing config is preserved ---
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(docker run --rm \
  --entrypoint bash \
  "$IMAGE" -c '
    mkdir -p "$HOME/.config/opencode"
    echo "{ \"model\": \"USER_CUSTOM\" }" > "$HOME/.config/opencode/opencode.json"
    /entrypoint.sh --version >/dev/null 2>&1
    grep -q "USER_CUSTOM" "$HOME/.config/opencode/opencode.json" && echo PRESERVED || echo CLOBBERED
  ' 2>&1)
if echo "$RESULT" | grep -q "^PRESERVED$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint preserves existing opencode.json (no reseed)\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint preserves existing opencode.json (no reseed)")
  printf "${RED}FAIL${NC} [%d] preserve behaviour (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi
