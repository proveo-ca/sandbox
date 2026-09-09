#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml

assert_success \
  "baked defaults: opencode.json present in /opt" \
  "$IMAGE" \
  "test -f /opt/opencode/defaults/opencode.json"

# SPEC: _spec/defs/agent-definition-sharing.puml
assert_success \
  "baked subagents: every agent in the opencode roster has a body in /opt" \
  "$IMAGE" \
  'set -eu
   roster="$(jq -r ".opencode[]" /opt/proveo/subagents/_roster.json)"
   [ -n "$roster" ] || { echo "opencode roster is empty"; exit 1; }
   for a in $roster; do
     test -f "/opt/proveo/subagents/$a.md" || { echo "missing /opt/proveo/subagents/$a.md"; exit 1; }
   done
   echo "roster: $(echo $roster | tr "\n" " ")"'

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

# NO SEED MAY PIN A MODEL BY ENVIRONMENT. These two assertions were the reverse
# — that plan and build read "{env:OPENCODE_MODEL}" and "{env:OPENCODE_BUILD_MODEL}"
# — which the retired bridge tables filled. opencode substitutes an unset
# {env:...} with the EMPTY STRING, so after the retirement the seed pinned the
# model to "" in the durable home. Both seed files are checked: sample_opencode.json
# is the one seed_opencode_config actually copies.
# SPEC: _spec/_plans/retire-model-bridging.puml
for seed in /opt/opencode/defaults/opencode.json /opt/opencode/sample_opencode.json; do
  assert_success \
    "$(basename "$seed"): names no model by environment" \
    "$IMAGE" \
    "! grep -q 'env:OPENCODE' $seed"
done

# The agents must simply omit `model`: opencode gives a primary agent the
# globally configured model when its own is unset, so omitting inherits instead
# of resolving to "".
assert_success \
  "default opencode.json: plan and build omit their own model (inherit the global)" \
  "$IMAGE" \
  "jq -e '.agent.plan.model == null and .agent.build.model == null' /opt/opencode/defaults/opencode.json"

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(docker run --rm \
  --entrypoint /entrypoint.sh \
  "$IMAGE" --version 2>&1 || true)
if echo "$RESULT" | grep -qE "(Seeded global defaults|already-seeded|opencode version)"; then
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
