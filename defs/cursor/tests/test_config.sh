#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml

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

# THE RETIREMENT, ASSERTED FROM THE OUTSIDE. cursor's entrypoint launches with
# `--model "$CURSOR_MODEL"` and nothing else, so the fake `agent` above reports
# exactly what proveo chose. These three tests used to assert the OPPOSITE — that
# ARCHITECT_MODEL became CURSOR_MODEL, that an explicit CURSOR_MODEL outranked it,
# and that EDITOR_MODEL filled in when ARCHITECT_MODEL was unset. All three
# described the bridge table, which is deleted.
# SPEC: _spec/_plans/retire-model-bridging.puml
cat >"$FIXTURE_DIR/.env" <<'EOF'
ARCHITECT_MODEL=claude-sonnet-4
EDITOR_MODEL=gpt-4.1
SMALL_MODEL=gpt-4.1-mini
EOF

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 30s docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh -p "test"' 2>&1 || true)
if echo "$RESULT" | grep -q "PASSED_MODEL=$" \
   && ! echo "$RESULT" | grep -q "PASSED_MODEL=claude-sonnet-4" \
   && ! echo "$RESULT" | grep -q "PASSED_MODEL=gpt-4.1"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] no role name in .env reaches cursor's --model\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("no role name in .env reaches cursor's --model")
  printf "${RED}FAIL${NC} [%d] model bridging has grown back (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi

# What SURVIVES: the harness's OWN variable. An operator who exports CURSOR_MODEL
# is configuring cursor, not asking proveo to choose — and the launch flag must
# still carry it, or the retirement took a working control with it.
cat >"$FIXTURE_DIR/.env" <<'EOF'
ARCHITECT_MODEL=claude-sonnet-4
CURSOR_MODEL=explicit-model
EOF

TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 30s docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh -p "test"' 2>&1 || true)
if echo "$RESULT" | grep -q "PASSED_MODEL=explicit-model"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] cursor's own CURSOR_MODEL still reaches --model\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("cursor's own CURSOR_MODEL still reaches --model")
  printf "${RED}FAIL${NC} [%d] CURSOR_MODEL lost (output: %.300s)\n" "$TESTS_RUN" "$RESULT"
fi
