#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_defaults.sh - Baked defaults exist, seed correctly, never touch /app.

assert_success \
  "baked defaults: config.toml present in /opt" \
  "$IMAGE" \
  "test -f /opt/codex/defaults/config.toml"

assert_output_contains \
  "default config.toml never pauses an unattended loop" \
  "$IMAGE" \
  "cat /opt/codex/defaults/config.toml" \
  'approval_policy = "never"'

assert_output_contains \
  "default config.toml leaves confinement to the container" \
  "$IMAGE" \
  "cat /opt/codex/defaults/config.toml" \
  'sandbox_mode = "danger-full-access"'

# A repo that steers agents through CLAUDE.md is still read, rather than ignored.
assert_output_contains \
  "default config.toml falls back to CLAUDE.md for project docs" \
  "$IMAGE" \
  "cat /opt/codex/defaults/config.toml" \
  'project_doc_fallback_filenames'

# --- Runtime seeding via the entrypoint (utility passthrough exits fast) ---
SEED_PROBE='/entrypoint.sh --version >/dev/null 2>&1; '

TESTS_RUN=$((TESTS_RUN + 1))
CHECK=$(run_timeout 60 docker run --rm \
  --entrypoint bash \
  "$IMAGE" -c "${SEED_PROBE}"'test -f "$HOME/.codex/config.toml" && echo OK' 2>&1 || true)
if echo "$CHECK" | grep -q "^OK$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint seeds \$CODEX_HOME/config.toml on first run\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint seeds \$CODEX_HOME/config.toml on first run")
  printf "${RED}FAIL${NC} [%d] config seed (output: %.300s)\n" "$TESTS_RUN" "$CHECK"
fi

# Subagents are composed, not copied: one shared body per agent plus codex's own
# TOML frontmatter, joined at container start.
REQUIRED_AGENTS=(
  "adversarial-reviewer"
  "architect"
  "monorepo-coordinator"
  "security-reviewer"
  "spec-keeper"
)
TESTS_RUN=$((TESTS_RUN + 1))
MISSING=$(run_timeout 60 docker run --rm \
  --entrypoint bash \
  "$IMAGE" -c "${SEED_PROBE}"'for a in '"${REQUIRED_AGENTS[*]}"'; do
     test -f "$HOME/.codex/agents/$a.toml" || echo "MISSING:$a"
   done' 2>&1 || true)
if [[ -z "$(echo "$MISSING" | grep MISSING || true)" ]]; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint composes every codex subagent\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint composes every codex subagent")
  printf "${RED}FAIL${NC} [%d] subagent composition (%.300s)\n" "$TESTS_RUN" "$MISSING"
fi

# The read-only split is what stands between an advisor and the working tree. codex
# has no per-tool allowlist, so sandbox_mode is the structural control.
TESTS_RUN=$((TESTS_RUN + 1))
RO=$(run_timeout 60 docker run --rm \
  --entrypoint bash \
  "$IMAGE" -c "${SEED_PROBE}"'for a in adversarial-reviewer architect monorepo-coordinator security-reviewer; do
     grep -q "sandbox_mode = \"read-only\"" "$HOME/.codex/agents/$a.toml" || echo "WRITABLE:$a"
   done
   grep -q "sandbox_mode = \"workspace-write\"" "$HOME/.codex/agents/spec-keeper.toml" || echo "NOT_WRITER:spec-keeper"' 2>&1 || true)
if [[ -z "$(echo "$RO" | grep -E "WRITABLE|NOT_WRITER" || true)" ]]; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] advisors are read-only; spec-keeper is the single writer\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("advisors are read-only; spec-keeper is the single writer")
  printf "${RED}FAIL${NC} [%d] subagent sandbox split (%.300s)\n" "$TESTS_RUN" "$RO"
fi

# House rules reach the USER layer, where a project AGENTS.md still outranks them.
TESTS_RUN=$((TESTS_RUN + 1))
HR=$(run_timeout 60 docker run --rm \
  --entrypoint bash \
  "$IMAGE" -c "${SEED_PROBE}"'test -s "$HOME/.codex/AGENTS.md" && echo OK' 2>&1 || true)
if echo "$HR" | grep -q "^OK$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] house rules install at \$CODEX_HOME/AGENTS.md\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("house rules install at \$CODEX_HOME/AGENTS.md")
  printf "${RED}FAIL${NC} [%d] house rules (output: %.300s)\n" "$TESTS_RUN" "$HR"
fi

# --- CODEX_RESEED=1 overwrites a user-modified config ---
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  -e CODEX_RESEED=1 \
  --entrypoint bash \
  "$IMAGE" -c '
    mkdir -p "$HOME/.codex"
    echo "approval_policy = \"untrusted\"" > "$HOME/.codex/config.toml"
    /entrypoint.sh --version >/dev/null 2>&1
    grep -q "danger-full-access" "$HOME/.codex/config.toml" && echo RESEEDED || echo NOT_RESEEDED
  ' 2>&1 || true)
if echo "$RESULT" | grep -q "^RESEEDED$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] CODEX_RESEED=1 overwrites an existing config\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("CODEX_RESEED=1 overwrites an existing config")
  printf "${RED}FAIL${NC} [%d] CODEX_RESEED behaviour (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# --- Without CODEX_RESEED, an existing config is preserved ---
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  --entrypoint bash \
  "$IMAGE" -c '
    mkdir -p "$HOME/.codex"
    echo "model = \"USER_CUSTOM\"" > "$HOME/.codex/config.toml"
    /entrypoint.sh --version >/dev/null 2>&1
    grep -q "USER_CUSTOM" "$HOME/.codex/config.toml" && echo PRESERVED || echo CLOBBERED
  ' 2>&1 || true)
if echo "$RESULT" | grep -q "^PRESERVED$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint preserves an existing config.toml (no reseed)\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint preserves an existing config.toml (no reseed)")
  printf "${RED}FAIL${NC} [%d] preserve behaviour (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# --- The workspace is NEVER written to ---
# codex reads AGENTS.md natively, so unlike claudecode there is no bridge file to
# seed into the operator's checkout — and nothing else may appear there either.
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  --entrypoint bash \
  "$IMAGE" -c '
    /entrypoint.sh --version >/dev/null 2>&1
    found=$(ls -A /app 2>/dev/null | grep -v "^output$" || true)
    [ -z "$found" ] && echo UNTOUCHED || echo "MUTATED:$found"
  ' 2>&1 || true)
if echo "$RESULT" | grep -q "^UNTOUCHED$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] the workspace is left untouched\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("the workspace is left untouched")
  printf "${RED}FAIL${NC} [%d] workspace mutation (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi
