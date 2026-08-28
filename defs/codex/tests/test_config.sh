#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_config.sh - Entrypoint behaviour: smoke mode, preamble, model bridges

# Smoke mode: entrypoint completes setup, prints the ready marker, then parks.
TESTS_RUN=$((TESTS_RUN + 1))
SMOKE_OUTPUT=$(run_timeout 60 docker run --rm \
  -e PROVEO_SMOKE_TEST=1 \
  --entrypoint bash \
  "$IMAGE" -c "timeout 20 /entrypoint.sh; true" 2>&1 || true)
if echo "$SMOKE_OUTPUT" | grep -q "PROVEO_SMOKE_READY codex"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] smoke mode prints PROVEO_SMOKE_READY\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("smoke mode prints PROVEO_SMOKE_READY")
  printf "${RED}FAIL${NC} [%d] smoke mode (output: %.300s)\n" "$TESTS_RUN" "$SMOKE_OUTPUT"
fi

# The preamble names the paradigm, so a transcript says which loop was running.
TESTS_RUN=$((TESTS_RUN + 1))
if echo "$SMOKE_OUTPUT" | grep -q "ML blackbox algorithm"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] preamble states the paradigm\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("preamble states the paradigm")
  printf "${RED}FAIL${NC} [%d] preamble states the paradigm (output: %.300s)\n" "$TESTS_RUN" "$SMOKE_OUTPUT"
fi

TESTS_RUN=$((TESTS_RUN + 1))
if echo "$SMOKE_OUTPUT" | grep -q "Subagents available:"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] preamble lists composed subagents\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("preamble lists composed subagents")
  printf "${RED}FAIL${NC} [%d] preamble lists composed subagents (output: %.300s)\n" "$TESTS_RUN" "$SMOKE_OUTPUT"
fi

# No credential of either kind: the entrypoint must say so rather than launching
# into a login prompt nobody is there to answer.
TESTS_RUN=$((TESTS_RUN + 1))
if echo "$SMOKE_OUTPUT" | grep -q "No credential found"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] warns when neither OPENAI_API_KEY nor a login exists\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("warns when neither OPENAI_API_KEY nor a login exists")
  printf "${RED}FAIL${NC} [%d] credential warning (output: %.300s)\n" "$TESTS_RUN" "$SMOKE_OUTPUT"
fi

# --- Launch posture and model bridging, through a fake codex binary ---
# A fake binary keeps this on the ENTRYPOINT's logic: no network, no credential,
# and the argv it built is printed back rather than interpreted.
FIXTURE_DIR=$(mktemp -d)
trap 'rm -rf "$FIXTURE_DIR"' RETURN

mkdir -p "$FIXTURE_DIR/fake-bin"
cat >"$FIXTURE_DIR/fake-bin/codex" <<'FAKE'
#!/usr/bin/env bash
case "${1:-}" in
  --version|-V) echo "codex-cli 0.0.0-fake"; exit 0 ;;
esac
echo "PASSED_ARGV=$*"
model=""
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "--model" ]]; then shift; model="$1"; break; fi
  shift
done
echo "PASSED_MODEL=${model}"
FAKE
chmod +x "$FIXTURE_DIR/fake-bin/codex"

run_fake() {
  run_timeout 60 docker run --rm \
    -v "$FIXTURE_DIR:/app" \
    "$@" \
    --entrypoint bash \
    "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh "do the thing"' 2>&1 || true
}

assert_fake() {
  local desc="$1" expected="$2"; shift 2
  TESTS_RUN=$((TESTS_RUN + 1))
  local out
  out=$(run_fake "$@")
  if echo "$out" | grep -qF "$expected"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("$desc")
    printf "${RED}FAIL${NC} [%d] %s\n" "$TESTS_RUN" "$desc"
    printf "     Expected to contain: %s\n" "$expected"
    printf "     Actual: %.400s\n" "$out"
  fi
}

: >"$FIXTURE_DIR/.env"
assert_fake "default launch bypasses codex's own approvals and sandbox" \
  "PASSED_ARGV=--dangerously-bypass-approvals-and-sandbox"

assert_fake "PROVEO_CODEX_SANDBOX opts back into codex's own sandbox" \
  "PASSED_ARGV=--sandbox workspace-write --ask-for-approval never" \
  -e PROVEO_CODEX_SANDBOX=workspace-write

cat >"$FIXTURE_DIR/.env" <<'ENVF'
ARCHITECT_MODEL=anthropic/claude-opus-5
EDITOR_MODEL=gpt-5.6
ENVF
# The `bare` transform strips the provider prefix: codex takes a bare model id.
assert_fake "entrypoint bridges ARCHITECT_MODEL to CODEX_MODEL (bare)" \
  "PASSED_MODEL=claude-opus-5"

cat >"$FIXTURE_DIR/.env" <<'ENVF'
ARCHITECT_MODEL=anthropic/claude-opus-5
CODEX_MODEL=explicit-model
ENVF
assert_fake "entrypoint preserves an explicit CODEX_MODEL over ARCHITECT_MODEL" \
  "PASSED_MODEL=explicit-model"

cat >"$FIXTURE_DIR/.env" <<'ENVF'
EDITOR_MODEL=gpt-5.6
ENVF
assert_fake "entrypoint bridges EDITOR_MODEL when ARCHITECT_MODEL is unset" \
  "PASSED_MODEL=gpt-5.6"

# A caller's own posture flags must not be doubled: passing both is a clap
# conflict, which fails the run outright instead of degrading to one of the two.
TESTS_RUN=$((TESTS_RUN + 1))
: >"$FIXTURE_DIR/.env"
RESULT=$(run_timeout 60 docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh --full-auto "do the thing"' 2>&1 || true)
if echo "$RESULT" | grep -q "PASSED_ARGV=--full-auto" \
   && ! echo "$RESULT" | grep -q "dangerously-bypass"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] a caller's own posture flag is left alone\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("a caller's own posture flag is left alone")
  printf "${RED}FAIL${NC} [%d] posture flag passthrough (output: %.400s)\n" "$TESTS_RUN" "$RESULT"
fi

# Utility subcommands do agent-free work and must not be handed a sandbox posture.
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh login' 2>&1 || true)
if echo "$RESULT" | grep -q "PASSED_ARGV=login$"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] utility subcommands pass through unmodified\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("utility subcommands pass through unmodified")
  printf "${RED}FAIL${NC} [%d] utility passthrough (output: %.400s)\n" "$TESTS_RUN" "$RESULT"
fi

# --json is an exec flag, not a global one — it must follow the subcommand, or
# clap rejects it and the run never starts.
TESTS_RUN=$((TESTS_RUN + 1))
: >"$FIXTURE_DIR/.env"
RESULT=$(run_timeout 60 docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh exec "do the thing"' 2>&1 || true)
if echo "$RESULT" | grep -q "PASSED_ARGV=--dangerously-bypass-approvals-and-sandbox exec --json do the thing"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] verbose evidence puts --json after the exec subcommand\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("verbose evidence puts --json after the exec subcommand")
  printf "${RED}FAIL${NC} [%d] --json placement (output: %.400s)\n" "$TESTS_RUN" "$RESULT"
fi

# A caller's own --json is a parse contract: it is not doubled.
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh exec --json "do the thing"' 2>&1 || true)
if [[ "$(echo "$RESULT" | grep -c -- "--json" || true)" == "1" ]]; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] a caller's own --json is not doubled\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("a caller's own --json is not doubled")
  printf "${RED}FAIL${NC} [%d] --json doubling (output: %.400s)\n" "$TESTS_RUN" "$RESULT"
fi

# PROVEO_AGENT_EVIDENCE=default buys no extra narration at all.
TESTS_RUN=$((TESTS_RUN + 1))
RESULT=$(run_timeout 60 docker run --rm \
  -v "$FIXTURE_DIR:/app" \
  -e PROVEO_AGENT_EVIDENCE=default \
  --entrypoint bash \
  "$IMAGE" -c 'PATH="/app/fake-bin:$PATH" /entrypoint.sh exec "do the thing"' 2>&1 || true)
if ! echo "$RESULT" | grep -q -- "--json"; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] PROVEO_AGENT_EVIDENCE=default adds no flags\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("PROVEO_AGENT_EVIDENCE=default adds no flags")
  printf "${RED}FAIL${NC} [%d] evidence opt-out (output: %.400s)\n" "$TESTS_RUN" "$RESULT"
fi
