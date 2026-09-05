#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_defaults.sh - Baked defaults exist, seed correctly, and never touch /app uninvited.

# Defaults are baked at /opt/cursor/defaults
assert_success \
  "baked defaults: cli-config.json present in /opt" \
  "$IMAGE" \
  "test -f /opt/cursor/defaults/cli-config.json"

assert_success \
  "baked defaults: loop rule present in /opt" \
  "$IMAGE" \
  "test -f /opt/cursor/defaults/rules/proveo-loop.mdc"

assert_success \
  "baked defaults: audit hook script is executable" \
  "$IMAGE" \
  "test -x /opt/cursor/defaults/hooks/audit-shell.sh"

# Subagents are NOT under /opt/<harness>/defaults/agents any more. They are one
# shared tree at /opt/proveo/subagents with a per-harness _roster.json, and this
# file went on asserting the retired path — 10 failures against an image that was
# built correctly, which reads as a broken image rather than a stale test.
#
# The roster is READ FROM THE IMAGE rather than restated here. A list copied into a
# test drifts silently the moment the roster changes; cecli's test.sh already took
# this approach ("read from the image, never restated here") and did not go stale.
# SPEC: _spec/defs/agent-definition-sharing.puml
#
# `readonly: true` moved with them, into the per-harness FRONTMATTER
# (_frontmatter/cursor/<agent>.yaml) that the seed renders onto the shared body —
# so it is asserted there rather than in the body, which never carried it.
assert_success \
  "baked subagents: every agent in the cursor roster has a body and is readonly" \
  "$IMAGE" \
  'set -eu
   roster="$(jq -r ".cursor[]" /opt/proveo/subagents/_roster.json)"
   [ -n "$roster" ] || { echo "cursor roster is empty"; exit 1; }
   for a in $roster; do
     test -f "/opt/proveo/subagents/$a.md" || { echo "missing /opt/proveo/subagents/$a.md"; exit 1; }
     grep -qx "readonly: true" "/opt/proveo/subagents/_frontmatter/cursor/$a.yaml" \
       || { echo "$a frontmatter is not readonly: true"; exit 1; }
   done
   echo "roster: $(echo $roster | tr "\n" " ")"'

# Deny baseline survives --force by product semantics; assert it exists.
assert_output_contains \
  "default cli-config.json denies privilege escalation" \
  "$IMAGE" \
  'cat /opt/cursor/defaults/cli-config.json' \
  '"Shell(sudo)"'

assert_output_contains \
  "default cli-config.json denies env-file reads" \
  "$IMAGE" \
  'cat /opt/cursor/defaults/cli-config.json' \
  '"Read(.env*)"'

# Enterprise hook layer is baked outside the agent-writable tree.
assert_output_contains \
  "enterprise hooks.json wires the shell audit hook" \
  "$IMAGE" \
  'cat /etc/cursor/hooks.json' \
  'beforeShellExecution'

# --- Runtime seeding via entrypoint (utility passthrough exits fast) ---
TESTS_RUN=$((TESTS_RUN + 1))
CHECK=$(run_timeout 60 docker run --rm \
  --entrypoint bash \
  "$IMAGE" -c '/entrypoint.sh --version >/dev/null 2>&1; test -f "$HOME/.cursor/cli-config.json" && test -f "$HOME/.cursor/agents/adversarial-reviewer.md" && echo OK' 2>&1 || true)
if echo "$CHECK" | grep -q "^OK$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint seeds ~/.cursor on first run\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint seeds ~/.cursor on first run")
  printf "${RED}FAIL${NC} [%d] seed check (output: %.300s)\n" "$TESTS_RUN" "$CHECK"
fi


# --- CURSOR_RESEED=1 overwrites user-modified config ---
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  -e CURSOR_RESEED=1 \
  -e PROVEO_SMOKE_TEST=1 \
  --entrypoint bash \
  "$IMAGE" -c '
    mkdir -p "$HOME/.cursor"
    echo "{ \"version\": 1, \"permissions\": { \"deny\": [] } }" > "$HOME/.cursor/cli-config.json"
    timeout 10 /entrypoint.sh >/dev/null 2>&1
    grep -q "Shell(sudo)" "$HOME/.cursor/cli-config.json" && echo RESEEDED || echo NOT_RESEEDED
  ' 2>&1 || true)
if echo "$RESULT" | grep -q "^RESEEDED$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] CURSOR_RESEED=1 overwrites existing config\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("CURSOR_RESEED=1 overwrites existing config")
  printf "${RED}FAIL${NC} [%d] CURSOR_RESEED behaviour (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# --- Without CURSOR_RESEED, existing config is preserved ---
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  -e PROVEO_SMOKE_TEST=1 \
  --entrypoint bash \
  "$IMAGE" -c '
    mkdir -p "$HOME/.cursor"
    echo "{ \"version\": 1, \"permissions\": { \"deny\": [\"Shell(USER_CUSTOM)\"] } }" > "$HOME/.cursor/cli-config.json"
    timeout 10 /entrypoint.sh >/dev/null 2>&1
    grep -q "USER_CUSTOM" "$HOME/.cursor/cli-config.json" && echo PRESERVED || echo CLOBBERED
  ' 2>&1 || true)
if echo "$RESULT" | grep -q "^PRESERVED$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint preserves existing cli-config.json (no reseed)\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint preserves existing cli-config.json (no reseed)")
  printf "${RED}FAIL${NC} [%d] preserve behaviour (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# --- Workspace is NEVER seeded by default (container-internal defaults only) ---
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  --entrypoint bash \
  "$IMAGE" -c '
    /entrypoint.sh --version >/dev/null 2>&1
    test -e /app/.cursor && echo MUTATED || echo UNTOUCHED
  ' 2>&1 || true)
if echo "$RESULT" | grep -q "^UNTOUCHED$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] workspace is untouched without CURSOR_SEED_RULES\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("workspace is untouched without CURSOR_SEED_RULES")
  printf "${RED}FAIL${NC} [%d] default workspace mutation (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# --- CURSOR_SEED_RULES=1 seeds the loop rule into the workspace ---
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  -e CURSOR_SEED_RULES=1 \
  --entrypoint bash \
  "$IMAGE" -c '
    /entrypoint.sh --version >/dev/null 2>&1
    test -f /app/.cursor/rules/proveo-loop.mdc && echo SEEDED || echo MISSING
  ' 2>&1 || true)
if echo "$RESULT" | grep -q "^SEEDED$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] CURSOR_SEED_RULES=1 seeds the loop rule into the workspace\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("CURSOR_SEED_RULES=1 seeds the loop rule into the workspace")
  printf "${RED}FAIL${NC} [%d] opt-in rule seed (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# --- Audit hook appends the stdin payload and allows ---
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  --entrypoint bash \
  "$IMAGE" -c '
    out=$(echo "{\"command\":\"ls\"}" | /opt/cursor/defaults/hooks/audit-shell.sh)
    grep -q "{\"command\":\"ls\"}" "$HOME/.cursor/audit-shell.ndjson" && \
      [ "$out" = "{\"permission\":\"allow\"}" ] && echo HOOK_OK
  ' 2>&1 || true)
if echo "$RESULT" | grep -q "^HOOK_OK$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] audit hook logs NDJSON and allows\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("audit hook logs NDJSON and allows")
  printf "${RED}FAIL${NC} [%d] audit hook behaviour (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi
