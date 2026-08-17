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

REQUIRED_AGENTS=(
  "adversarial-reviewer"
  "security-reviewer"
)
for a in "${REQUIRED_AGENTS[@]}"; do
  assert_success \
    "baked defaults: agents/$a.md present in /opt" \
    "$IMAGE" \
    "test -f /opt/cursor/defaults/agents/$a.md"
  assert_output_contains \
    "default subagent $a is structurally readonly" \
    "$IMAGE" \
    "cat /opt/cursor/defaults/agents/$a.md" \
    "readonly: true"
done

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
