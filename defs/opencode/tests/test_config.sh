#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_config.sh - Entrypoint configuration detection

FIXTURE_DIR=$(mktemp -d)
trap 'rm -rf "$FIXTURE_DIR"' RETURN

cat >"$FIXTURE_DIR/opencode.json" <<'EOF'
{
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5"
}
EOF
cat >"$FIXTURE_DIR/AGENTS.md" <<'EOF'
# Test fixture agents file.
EOF
cat >"$FIXTURE_DIR/.env" <<'EOF'
OPENCODE_TEST_MARKER=loaded_from_env
EOF

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 30s docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint /entrypoint.sh \
  "$IMAGE" --version 2>&1 || true)
if echo "$RESULT" | grep -q "Found opencode.json" \
   && echo "$RESULT" | grep -q "Found AGENTS.md" \
   && echo "$RESULT" | grep -q "Loaded environment variables from .env"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint detects opencode.json + AGENTS.md + .env\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint detects opencode.json + AGENTS.md + .env")
  printf "${RED}FAIL${NC} [%d] entrypoint detects config (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# Entrypoint bridges common .env model aliases to opencode env vars.
# Use a fake opencode binary so this checks the entrypoint logic without a real model call.
mkdir -p "$FIXTURE_DIR/fake-bin"
cat >"$FIXTURE_DIR/fake-bin/opencode" <<'EOF'
#!/usr/bin/env bash
printf 'OPENCODE_MODEL=%s\n' "${OPENCODE_MODEL:-}"
printf 'OPENCODE_SMALL_MODEL=%s\n' "${OPENCODE_SMALL_MODEL:-}"
printf 'OPENCODE_BUILD_MODEL=%s\n' "${OPENCODE_BUILD_MODEL:-}"
if [[ "${1:-}" == "--version" ]]; then
  echo "9.9.9"
fi
EOF
chmod +x "$FIXTURE_DIR/fake-bin/opencode"
cat >"$FIXTURE_DIR/.env" <<'EOF'
ARCHITECT_MODEL=gpt-5.5
EDITOR_MODEL=xai/grok-4.3
SMALL_MODEL=xai/grok-small
EOF
# Also test bridging OPENCODE_SMALL_MODEL -> SMALL_MODEL when SMALL_MODEL not set
cat >"$FIXTURE_DIR/.env2" <<'EOF'
ARCHITECT_MODEL=gpt-5.5
EDITOR_MODEL=xai/grok-4.3
OPENCODE_SMALL_MODEL=xai/grok-4.3
EOF

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 30s docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh --version' 2>&1 || true)
if echo "$RESULT" | grep -q "OPENCODE_MODEL=openai/gpt-5.5" \
   && echo "$RESULT" | grep -q "OPENCODE_SMALL_MODEL=xai/grok-4.3" \
   && echo "$RESULT" | grep -q "OPENCODE_BUILD_MODEL=xai/grok-4.3"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint bridges .env model aliases to opencode env vars\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint bridges .env model aliases to opencode env vars")
  printf "${RED}FAIL${NC} [%d] opencode model bridge (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# Entrypoint bridges OPENCODE_SMALL_MODEL into SMALL_MODEL when SMALL_MODEL is unset
cat >"$FIXTURE_DIR/.env" <<'EOF'
ARCHITECT_MODEL=gpt-5.5
EDITOR_MODEL=xai/grok-4.3
OPENCODE_SMALL_MODEL=xai/grok-4.3
EOF
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 30s docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh --version' 2>&1 || true)
if echo "$RESULT" | grep -q "SMALL_MODEL=xai/grok-4.3"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint bridges OPENCODE_SMALL_MODEL into SMALL_MODEL\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint bridges OPENCODE_SMALL_MODEL into SMALL_MODEL")
  printf "${RED}FAIL${NC} [%d] SMALL_MODEL bridge (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

TESTS_RUN=$((TESTS_RUN + 1))
RUN_WRAPPER="$PROJECT_ROOT/run.sh"
if grep -q 'TARGET="opencode"' "$RUN_WRAPPER" \
   && grep -q 'exec "$PROVEO_BIN" run' "$RUN_WRAPPER" \
   && grep -q -- '--repo-root' "$RUN_WRAPPER"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] run.sh shims to proveo run with monorepo flag parity\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("run.sh shims to proveo run with monorepo flag parity")
  printf "${RED}FAIL${NC} [%d] opencode run.sh shim contract\n" "$TESTS_RUN"
fi

# Entrypoint forwards args to opencode
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 30s docker run --rm "$IMAGE" --version 2>&1 || true)
if echo "$RESULT" | grep -qE "[0-9]+\.[0-9]+"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint forwards args to opencode (--version)\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint forwards args to opencode (--version)")
  printf "${RED}FAIL${NC} [%d] entrypoint forwards args (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi
