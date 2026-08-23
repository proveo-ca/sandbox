#!/usr/bin/env bash
# SPEC: _spec/internal/cdn/distribution-update.puml
# Consumer CDN install suite: checksum-verified Go proveo via install.sh.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLI_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
INSTALL_SCRIPT="$CLI_ROOT/install.sh"
UNINSTALL_SCRIPT="$CLI_ROOT/uninstall.sh"

TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0
FAILURES=()
LAST_OUTPUT=""
TEMP_ROOT=""

RED=$'\033[0;31m'
GREEN=$'\033[0;32m'
NC=$'\033[0m'

record_pass() {
  TESTS_PASSED=$((TESTS_PASSED + 1))
  printf '%sPASS%s [%d] %s\n' "$GREEN" "$NC" "$TESTS_RUN" "$1"
}

record_fail() {
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILURES+=("$1")
  printf '%sFAIL%s [%d] %s\n' "$RED" "$NC" "$TESTS_RUN" "$1"
  if [[ -n "$LAST_OUTPUT" ]]; then
    printf ' Output: %.500s\n' "$LAST_OUTPUT"
  fi
}

run_test() {
  local desc="$1"
  shift
  TESTS_RUN=$((TESTS_RUN + 1))
  if LAST_OUTPUT="$($@ 2>&1)"; then
    record_pass "$desc"
  else
    record_fail "$desc"
  fi
}

assert_success() {
  local desc="$1"
  shift
  run_test "$desc" "$@"
}

assert_failure() {
  local desc="$1"
  shift
  TESTS_RUN=$((TESTS_RUN + 1))
  if LAST_OUTPUT="$($@ 2>&1)"; then
    record_fail "$desc"
  else
    record_pass "$desc"
  fi
}

assert_output_contains() {
  local desc="$1"
  local expected="$2"
  shift 2
  TESTS_RUN=$((TESTS_RUN + 1))
  if LAST_OUTPUT="$($@ 2>&1)" && [[ "$LAST_OUTPUT" == *"$expected"* ]]; then
    record_pass "$desc"
  else
    record_fail "$desc"
    printf ' Expected to contain: %s\n' "$expected"
  fi
}

assert_file_exists() {
  local desc="$1"
  local file="$2"
  TESTS_RUN=$((TESTS_RUN + 1))
  LAST_OUTPUT=""
  if [[ -f "$file" ]]; then
    record_pass "$desc"
  else
    LAST_OUTPUT="Missing file: $file"
    record_fail "$desc"
  fi
}

assert_file_executable() {
  local desc="$1"
  local file="$2"
  TESTS_RUN=$((TESTS_RUN + 1))
  LAST_OUTPUT=""
  if [[ -x "$file" ]]; then
    record_pass "$desc"
  else
    LAST_OUTPUT="File is not executable: $file"
    record_fail "$desc"
  fi
}

assert_no_path() {
  local desc="$1"
  local path="$2"
  TESTS_RUN=$((TESTS_RUN + 1))
  LAST_OUTPUT=""
  if [[ ! -e "$path" ]]; then
    record_pass "$desc"
  else
    LAST_OUTPUT="Path still exists: $path"
    record_fail "$desc"
  fi
}

make_fake_curl() {
  local bin_dir="$1"
  mkdir -p "$bin_dir"
  cat > "$bin_dir/curl" <<'EOF'
#!/usr/bin/env bash
src=""
dest=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) dest="$2"; shift 2 ;;
    -*) shift ;;
    *) src="$1"; shift ;;
  esac
done
[[ -n "$src" && -n "$dest" ]] || exit 1
src="${src#file://}"
cp "$src" "$dest"
EOF
  chmod +x "$bin_dir/curl"
}

platform_asset() {
  local os arch
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) echo "unsupported"; return 1 ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo "unsupported"; return 1 ;;
  esac
  printf 'proveo-%s-%s\n' "$os" "$arch"
}

cleanup() {
  if [[ -n "$TEMP_ROOT" ]]; then
    rm -rf "$TEMP_ROOT"
  fi
}

main() {
  TEMP_ROOT="$(mktemp -d)"
  trap cleanup EXIT

  echo "========================================="
  echo " proveo CDN install test suite"
  echo ""
  echo "========================================="
  echo ""

  assert_success "install script has valid Bash syntax" bash -n "$INSTALL_SCRIPT"
  assert_success "uninstall script has valid Bash syntax" bash -n "$UNINSTALL_SCRIPT"
  assert_file_exists "CDN checksums.txt present" "$CLI_ROOT/checksums.txt"
  assert_file_exists "CDN latest.json present" "$CLI_ROOT/latest.json"

  local asset
  asset="$(platform_asset)"
  assert_file_exists "platform binary staged ($asset)" "$CLI_ROOT/bin/$asset"

  local channel_version="dev"
  if command -v jq >/dev/null 2>&1; then
    channel_version="$(jq -r '.version // "dev"' "$CLI_ROOT/latest.json")"
  else
    channel_version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$CLI_ROOT/latest.json" | head -1)"
    [[ -n "$channel_version" ]] || channel_version="dev"
  fi

  local install_home="$TEMP_ROOT/home"
  local install_root="$TEMP_ROOT/install-root"
  local install_fake_bin="$TEMP_ROOT/install-bin"
  mkdir -p "$install_home" "$install_fake_bin"
  make_fake_curl "$install_fake_bin"

  assert_output_contains \
    "install.sh installs Go proveo" \
    "proveo v${channel_version} installed to:" \
    env \
    HOME="$install_home" \
    SHELL=/bin/bash \
    PATH="$install_fake_bin:$PATH" \
    PROVEO_INSTALL_ROOT="$install_root" \
    PROVEO_ASSET_BASE_URL="file://$CLI_ROOT" \
    PROVEO_CLI_BASE_URL="file://$CLI_ROOT" \
    "$INSTALL_SCRIPT"

  assert_file_exists "install writes proveo binary" "$install_root/bin/proveo"
  assert_file_executable "installed proveo is executable" "$install_root/bin/proveo"
  assert_file_exists "install writes uninstall.sh" "$install_root/uninstall.sh"
  assert_no_path "install does not ship bash lib/" "$install_root/lib"
  assert_no_path "install does not ship help.sh" "$install_root/bin/help.sh"

  # Installed binary should respond as Go proveo (--version and version alias).
  assert_output_contains \
    "installed proveo --version works" \
    "proveo version" \
    env PATH="$install_root/bin:$PATH" "$install_root/bin/proveo" --version
  assert_output_contains \
    "installed proveo version alias works" \
    "proveo version" \
    env PATH="$install_root/bin:$PATH" "$install_root/bin/proveo" version

  # Tampered checksum must fail.
  local bad_cdn="$TEMP_ROOT/bad-cdn"
  mkdir -p "$bad_cdn/bin"
  cp "$CLI_ROOT/bin/$asset" "$bad_cdn/bin/$asset"
  cp "$CLI_ROOT/uninstall.sh" "$bad_cdn/uninstall.sh"
  cp "$CLI_ROOT/latest.json" "$bad_cdn/latest.json"
  printf '0000000000000000000000000000000000000000000000000000000000000000  %s\n' "$asset" >"$bad_cdn/checksums.txt"
  assert_failure \
    "install rejects checksum mismatch" \
    env \
    HOME="$TEMP_ROOT/home-bad" \
    SHELL=/bin/bash \
    PATH="$install_fake_bin:$PATH" \
    PROVEO_INSTALL_ROOT="$TEMP_ROOT/install-bad" \
    PROVEO_ASSET_BASE_URL="file://$bad_cdn" \
    PROVEO_CLI_BASE_URL="file://$bad_cdn" \
    "$INSTALL_SCRIPT"

  assert_success \
    "uninstall.sh removes install root" \
    env \
    HOME="$install_home" \
    PROVEO_INSTALL_ROOT="$install_root" \
    PROVEO_UNINSTALL_ASSUME_YES=1 \
    "$UNINSTALL_SCRIPT"
  assert_no_path "uninstall removes install root" "$install_root"

  echo ""
  echo "========================================="
  printf ' Tests run: %d\n' "$TESTS_RUN"
  printf ' %sPassed: %d%s\n' "$GREEN" "$TESTS_PASSED" "$NC"
  printf ' %sFailed: %d%s\n' "$RED" "$TESTS_FAILED" "$NC"
  echo "========================================="

  if [[ ${#FAILURES[@]} -gt 0 ]]; then
    echo ""
    echo "Failed tests:"
    local failure
    for failure in "${FAILURES[@]}"; do
      echo " - $failure"
    done
    return 1
  fi

  echo ""
  echo "All CDN install tests passed."
}

main "$@"
