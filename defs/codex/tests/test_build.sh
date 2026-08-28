#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_build.sh - Image availability verification

TESTS_RUN=$((TESTS_RUN + 1))
printf "Verifying image %s is available... " "$IMAGE"
if docker image inspect "$IMAGE" >/dev/null 2>&1 || docker pull "$IMAGE" >/dev/null 2>&1; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] image is available\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("image is available")
  printf "${RED}FAIL${NC} [%d] image is available\n" "$TESTS_RUN"
  echo "FATAL: Cannot continue without image."
  print_summary
  exit 1
fi

assert_inspect \
  "has security.non-root=true label" \
  "$IMAGE" \
  '{{index .Config.Labels "security.non-root"}}' \
  "true"

assert_inspect \
  "has security.hardened=true label" \
  "$IMAGE" \
  '{{index .Config.Labels "security.hardened"}}' \
  "true"

# Without this label sbx records dind:false at container creation and never starts
# a daemon, which surfaces to the agent as "Cannot connect to the Docker daemon"
# beside Compose files it can read.
assert_inspect \
  "declares the sbx start-docker label" \
  "$IMAGE" \
  '{{index .Config.Labels "com.docker.sandboxes.start-docker"}}' \
  "true"

assert_inspect \
  "Docker USER is non-root (codex)" \
  "$IMAGE" \
  '{{.Config.User}}' \
  "codex"

assert_inspect \
  "entrypoint uses dumb-init" \
  "$IMAGE" \
  '{{json .Config.Entrypoint}}' \
  "dumb-init"

# CODEX_HOME must NOT be baked: it defaults to $HOME/.codex, and $HOME is what the
# proveo home mount redirects. A frozen absolute path would send every session,
# login and config write back into the image's home, past the mount.
TESTS_RUN=$((TESTS_RUN + 1))
BAKED_ENV=$(docker inspect --format '{{json .Config.Env}}' "$IMAGE" 2>&1 || true)
if echo "$BAKED_ENV" | grep -q "CODEX_HOME"; then
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("CODEX_HOME is not baked into the image")
  printf "${RED}FAIL${NC} [%d] CODEX_HOME must not be baked (found: %.200s)\n" "$TESTS_RUN" "$BAKED_ENV"
else
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] CODEX_HOME is not baked (it must follow \$HOME)\n" "$TESTS_RUN"
fi
