#!/usr/bin/env bash
# SPEC: _spec/tests/testing-strategy.puml
# tests/test_security.sh - Security hardening verification

for image in $(images_to_test); do
  tag=$(image_tag "$image")

  # Running user
  assert_output_contains \
    "[$tag] runs as user claude" \
    "$image" \
    "whoami" \
    "claude"

  # The image bakes uid/gid 1000 as "claude" (see Dockerfile ARG USER_ID). Keep
  # this in sync with that build arg; override via EXPECTED_UID.
  EXPECTED_UID="${EXPECTED_UID:-1000}"

  assert_output_contains \
    "[$tag] UID is $EXPECTED_UID" \
    "$image" \
    "id -u" \
    "$EXPECTED_UID"

  # No setuid/setgid binaries
  assert_failure \
    "[$tag] no setuid binaries" \
    "$image" \
    "find / -xdev -perm -4000 -type f 2>/dev/null | grep -q ."

  assert_failure \
    "[$tag] no setgid binaries" \
    "$image" \
    "find / -xdev -perm -2000 -type f 2>/dev/null | grep -q ."

  # Network recon tools removed
  assert_failure "[$tag] nc not available" "$image" "which nc"
  assert_failure "[$tag] netcat not available" "$image" "which netcat"
  assert_failure "[$tag] netstat not available" "$image" "which netstat"
  assert_failure "[$tag] ss not available" "$image" "which ss"

  # Cannot write to system dirs
  assert_failure \
    "[$tag] cannot write to /usr/bin" \
    "$image" \
    "touch /usr/bin/testfile 2>/dev/null"

  assert_failure \
    "[$tag] cannot write to /etc" \
    "$image" \
    "touch /etc/testfile 2>/dev/null"

  # Environment variables
  # NODE_ENV must stay unbaked: setting it to production makes the agent's own
  # `npm`/`pnpm install` skip devDependencies (tsc, eslint, test runners). The
  # project's .env is the only place allowed to set it.
  assert_output_contains \
    "[$tag] NODE_ENV is not baked into the image" \
    "$image" \
    'echo "${NODE_ENV:-unset}"' \
    "unset"

  assert_output_contains \
    "[$tag] RLIMIT_CORE is 0" \
    "$image" \
    'echo $RLIMIT_CORE' \
    "0"

  # Entrypoint uses dumb-init
  assert_inspect \
    "[$tag] entrypoint uses dumb-init" \
    "$image" \
    '{{json .Config.Entrypoint}}' \
    "dumb-init"

  # Node version is v22.x (proveo/base-node LTS floor)
  assert_output_matches \
    "[$tag] node version is v22.x" \
    "$image" \
    "node --version" \
    "^v22\."

  # Docker USER is non-root
  assert_inspect \
    "[$tag] Docker USER is non-root" \
    "$image" \
    '{{.Config.User}}' \
    "claude"

  # Run-as-host-uid contract: any `--user` uid (not just the baked 1000) must
  # get a usable identity and writable HOME via ensure_runtime_user.
  TESTS_RUN=$((TESTS_RUN + 1))
  LAST_OUTPUT=$(docker run --rm --user 4242:4242 --entrypoint bash "$image" -c \
    'source /entrypoint-lib.sh && ensure_runtime_user && echo "uid=$(id -u) home_writable=$(test -w "$HOME" && echo yes || echo no)"' 2>&1)
  if echo "$LAST_OUTPUT" | grep -qF "uid=4242 home_writable=yes"; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf "${GREEN}PASS${NC} [%d] [%s] arbitrary --user uid gets usable identity and writable HOME\n" "$TESTS_RUN" "$tag"
  else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILURES+=("[$tag] arbitrary --user uid gets usable identity and writable HOME")
    printf "${RED}FAIL${NC} [%d] [%s] arbitrary --user uid gets usable identity and writable HOME\n" "$TESTS_RUN" "$tag"
    printf "     Output: %.200s\n" "$LAST_OUTPUT"
  fi

  # Never root at runtime, even without wrapper flags.
  assert_failure \
    "[$tag] does not run as root by default" \
    "$image" \
    '[ "$(id -u)" = "0" ]'
done
