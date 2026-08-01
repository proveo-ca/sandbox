#!/usr/bin/env bash
# tests/test_build.sh - Image build verification

# --- Build the default (mcp) image ---
# Variant Dockerfiles resolve COPY paths from the repo root (same context
# build.sh uses), not the variant directory.
TESTS_RUN=$((TESTS_RUN + 1))
printf "Building claudecode image... "
if docker build -t "$STANDALONE_IMAGE" -f "$PROJECT_ROOT/mcp/Dockerfile" "$PROJECT_ROOT/../.." 2>&1; then
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf "${GREEN}PASS${NC} [%d] claudecode image builds successfully\n" "$TESTS_RUN"
else
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("claudecode image builds successfully")
  printf "${RED}FAIL${NC} [%d] claudecode image builds successfully\n" "$TESTS_RUN"
  echo "FATAL: Cannot continue without the claudecode image."
  print_summary
  exit 1
fi

# Cover the mcp variant too when its image is available locally (build.sh
# builds both); downstream phases key off this flag.
if docker image inspect "$MCP_IMAGE" >/dev/null 2>&1; then
  MCP_IMAGE_AVAILABLE=true
fi

# --- Verify Docker labels ---
assert_inspect \
  "[claudecode] has security.non-root=true label" \
  "$STANDALONE_IMAGE" \
  '{{index .Config.Labels "security.non-root"}}' \
  "true"

assert_inspect \
  "[claudecode] has security.hardened=true label" \
  "$STANDALONE_IMAGE" \
  '{{index .Config.Labels "security.hardened"}}' \
  "true"

if $MCP_IMAGE_AVAILABLE; then
  assert_inspect \
    "[mcp] has security.non-root=true label" \
    "$MCP_IMAGE" \
    '{{index .Config.Labels "security.non-root"}}' \
    "true"

  assert_inspect \
    "[mcp] has security.hardened=true label" \
    "$MCP_IMAGE" \
    '{{index .Config.Labels "security.hardened"}}' \
    "true"
fi
