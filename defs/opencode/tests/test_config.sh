#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml

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

mkdir -p "$FIXTURE_DIR/fake-bin"
cat >"$FIXTURE_DIR/fake-bin/opencode" <<'EOF'
#!/usr/bin/env bash
printf 'SAW OPENCODE_MODEL=[%s]\n' "${OPENCODE_MODEL:-}"
printf 'SAW OPENCODE_SMALL_MODEL=[%s]\n' "${OPENCODE_SMALL_MODEL:-}"
printf 'SAW OPENCODE_BUILD_MODEL=[%s]\n' "${OPENCODE_BUILD_MODEL:-}"
printf 'SAW SMALL_MODEL=[%s]\n' "${SMALL_MODEL:-}"
if [[ "${1:-}" == "--version" ]]; then
  echo "9.9.9"
fi
EOF
chmod +x "$FIXTURE_DIR/fake-bin/opencode"

# THE RETIREMENT, ASSERTED FROM THE OUTSIDE. proveo no longer chooses an agent's
# model, so a role name in the project .env must reach opencode as nothing at
# all. This file used to assert the OPPOSITE — that ARCHITECT_MODEL became
# OPENCODE_MODEL — and that is the test the deletion had to replace rather than
# leave failing. SPEC: _spec/_plans/retire-model-bridging.puml
cat >"$FIXTURE_DIR/.env" <<'EOF'
ARCHITECT_MODEL=gpt-5.5
EDITOR_MODEL=xai/grok-4.3
SMALL_MODEL=xai/grok-small
EOF

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 30s docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh --version' 2>&1 || true)
if echo "$RESULT" | grep -q "SAW OPENCODE_MODEL=\[\]" \
   && echo "$RESULT" | grep -q "SAW OPENCODE_SMALL_MODEL=\[\]" \
   && echo "$RESULT" | grep -q "SAW OPENCODE_BUILD_MODEL=\[\]"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] a role name in .env does NOT become an opencode model var\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("a role name in .env does NOT become an opencode model var")
  printf "${RED}FAIL${NC} [%d] model bridging has grown back (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# The REVERSE bridge is gone too: opencode's own var must not be published back
# under a role name. Asserted on the exact value, because the old test grepped
# for "SMALL_MODEL=xai/grok-4.3" and OPENCODE_SMALL_MODEL matched it as a
# SUBSTRING — it would have passed with the bridge already deleted.
cat >"$FIXTURE_DIR/.env" <<'EOF'
OPENCODE_SMALL_MODEL=xai/grok-4.3
EOF
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 30s docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh --version' 2>&1 || true)
if echo "$RESULT" | grep -q "SAW OPENCODE_SMALL_MODEL=\[xai/grok-4.3\]" \
   && echo "$RESULT" | grep -q "SAW SMALL_MODEL=\[\]"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] opencode's own OPENCODE_SMALL_MODEL survives and is not published as SMALL_MODEL\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("opencode's own OPENCODE_SMALL_MODEL survives and is not published as SMALL_MODEL")
  printf "${RED}FAIL${NC} [%d] reverse bridge or own-var loss (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
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
