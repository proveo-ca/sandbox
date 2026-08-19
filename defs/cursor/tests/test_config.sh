#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_config.sh - Entrypoint behavior: smoke mode, proxy compat, preamble

# Smoke mode: entrypoint completes setup, prints the ready marker, then parks.
TESTS_RUN=$((TESTS_RUN + 1))
SMOKE_OUTPUT=$(run_timeout 60 docker run --rm \
  -e PROVEO_SMOKE_TEST=1 \
  --entrypoint bash \
  "$IMAGE" -c "timeout 10 /entrypoint.sh; true" 2>&1 || true)
if echo "$SMOKE_OUTPUT" | grep -q "PROVEO_SMOKE_READY cursor"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] smoke mode prints PROVEO_SMOKE_READY\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("smoke mode prints PROVEO_SMOKE_READY")
  printf "${RED}FAIL${NC} [%d] smoke mode (output: %.300s)\n" "$TESTS_RUN" "$SMOKE_OUTPUT"
fi

# Entrypoint preamble reports the paradigm and the policy layer.
TESTS_RUN=$((TESTS_RUN + 1))
if echo "$SMOKE_OUTPUT" | grep -q "policy-gated autonomous loop"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] preamble states the paradigm\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("preamble states the paradigm")
  printf "${RED}FAIL${NC} [%d] preamble states the paradigm (output: %.300s)\n" "$TESTS_RUN" "$SMOKE_OUTPUT"
fi

TESTS_RUN=$((TESTS_RUN + 1))
if echo "$SMOKE_OUTPUT" | grep -q "Deny rules (survive --force)"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] preamble reports the deny-rule baseline\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("preamble reports the deny-rule baseline")
  printf "${RED}FAIL${NC} [%d] preamble reports the deny-rule baseline\n" "$TESTS_RUN"
fi

TESTS_RUN=$((TESTS_RUN + 1))
if echo "$SMOKE_OUTPUT" | grep -q "Subagents available:"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] preamble lists seeded subagents\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("preamble lists seeded subagents")
  printf "${RED}FAIL${NC} [%d] preamble lists seeded subagents\n" "$TESTS_RUN"
fi

# Proxy detection flips useHttp1ForAgent in the seeded config. Drive the
# entrypoint with --version (utility passthrough) so it exits immediately.
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  -e HTTPS_PROXY=http://squid:3128 \
  --entrypoint bash \
  "$IMAGE" -c '/entrypoint.sh --version >/dev/null 2>&1; grep -c "\"useHttp1ForAgent\": true" "$HOME/.cursor/cli-config.json"' 2>&1 || true)
if echo "$RESULT" | grep -q "^1$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] proxy env sets useHttp1ForAgent=true in seeded config\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("proxy env sets useHttp1ForAgent=true in seeded config")
  printf "${RED}FAIL${NC} [%d] useHttp1ForAgent behaviour (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  --entrypoint bash \
  "$IMAGE" -c '/entrypoint.sh --version >/dev/null 2>&1; grep -c "\"useHttp1ForAgent\": true" "$HOME/.cursor/cli-config.json" || true' 2>&1 || true)
if echo "$RESULT" | grep -q "^0$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] no proxy env leaves useHttp1ForAgent disabled\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("no proxy env leaves useHttp1ForAgent disabled")
  printf "${RED}FAIL${NC} [%d] proxy-less config (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# Entrypoint bridges common .env model aliases to CURSOR_MODEL. Use a fake agent
# binary so this checks the entrypoint logic without a real model call.
FIXTURE_DIR=$(mktemp -d)
trap 'rm -rf "$FIXTURE_DIR"' RETURN

mkdir -p "$FIXTURE_DIR/fake-bin"
cat >"$FIXTURE_DIR/fake-bin/agent" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  --version|-v) echo "1.0.0"; exit 0 ;;
esac
model=""
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "--model" ]]; then
    shift
    model="$1"
    break
  fi
  shift
done
echo "PASSED_MODEL=${model}"
EOF
chmod +x "$FIXTURE_DIR/fake-bin/agent"

cat >"$FIXTURE_DIR/.env" <<'EOF'
ARCHITECT_MODEL=claude-sonnet-4
EDITOR_MODEL=gpt-4.1
EOF

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 30s docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh -p "test"' 2>&1 || true)
if echo "$RESULT" | grep -q "PASSED_MODEL=claude-sonnet-4"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint bridges ARCHITECT_MODEL to CURSOR_MODEL\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint bridges ARCHITECT_MODEL to CURSOR_MODEL")
  printf "${RED}FAIL${NC} [%d] ARCHITECT_MODEL bridge (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

cat >"$FIXTURE_DIR/.env" <<'EOF'
ARCHITECT_MODEL=claude-sonnet-4
EDITOR_MODEL=gpt-4.1
CURSOR_MODEL=explicit-model
EOF

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 30s docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh -p "test"' 2>&1 || true)
if echo "$RESULT" | grep -q "PASSED_MODEL=explicit-model"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint preserves explicit CURSOR_MODEL over ARCHITECT_MODEL\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint preserves explicit CURSOR_MODEL over ARCHITECT_MODEL")
  printf "${RED}FAIL${NC} [%d] CURSOR_MODEL precedence (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

cat >"$FIXTURE_DIR/.env" <<'EOF'
EDITOR_MODEL=gpt-4.1
EOF

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 30s docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh -p "test"' 2>&1 || true)
if echo "$RESULT" | grep -q "PASSED_MODEL=gpt-4.1"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] entrypoint bridges EDITOR_MODEL when ARCHITECT_MODEL is unset\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("entrypoint bridges EDITOR_MODEL when ARCHITECT_MODEL is unset")
  printf "${RED}FAIL${NC} [%d] EDITOR_MODEL fallback (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi
