#!/usr/bin/env bash
# SPEC: _spec/internal/cdn/distribution-update.puml
# Product installer: download checksum-verified Go proveo into ~/.proveo/bin.
# Usage: curl -fsSL https://proveo.ca/cli/install.sh | bash
set -euo pipefail

INSTALL_ROOT="${PROVEO_INSTALL_ROOT:-$HOME/.proveo}"
BIN_DIR="$INSTALL_ROOT/bin"
ASSET_BASE_URL="${PROVEO_ASSET_BASE_URL:-https://proveo.ca/cli}"
CLI_BASE_URL="${PROVEO_CLI_BASE_URL:-https://proveo.ca/cli}"

PATH_MARKER_START="# Added by proveo install.sh"
PATH_MARKER_END="# End added by proveo install.sh"

print_error() {
  printf 'Error: %s\n' "$*" >&2
}

print_info() {
  printf '%s\n' "$*"
}

download_file() {
  local url="$1"
  local dest="$2"

  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$dest"
    return 0
  fi

  if command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
    return 0
  fi

  print_error "curl or wget is required to install proveo."
  exit 1
}

detect_platform() {
  local os arch
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    FreeBSD) os=freebsd ;;
    MINGW*|MSYS*|CYGWIN*)
      print_error "detected a Windows shell — use the PowerShell installer instead:"
      print_error "  irm https://proveo.ca/cli/install.ps1 | iex"
      exit 1
      ;;
    *)
      print_error "unsupported OS: $(uname -s) (need Linux, Darwin, or FreeBSD)"
      exit 1
      ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *)
      print_error "unsupported architecture: $(uname -m) (need amd64 or arm64)"
      exit 1
      ;;
  esac
  printf '%s %s\n' "$os" "$arch"
}

sha256_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
    return 0
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
    return 0
  fi
  print_error "sha256sum or shasum is required to verify the download."
  exit 1
}

verify_checksum() {
  local file="$1"
  local checksums="$2"
  local base expected actual
  base="$(basename "$file")"
  expected="$(awk -v b="$base" '$2 == b { print $1; exit }' "$checksums")"
  if [[ -z "$expected" ]]; then
    print_error "no checksum entry for $base in checksums.txt"
    exit 1
  fi
  actual="$(sha256_file "$file")"
  if [[ "$actual" != "$expected" ]]; then
    print_error "checksum mismatch for $base (expected $expected, got $actual)"
    exit 1
  fi
}

# resolve_channel_version reads latest.json (preferred) or falls back to checksums-only install.
resolve_channel_version() {
  local latest="$1"
  if [[ -n "${PROVEO_VERSION:-}" ]]; then
    printf '%s\n' "${PROVEO_VERSION#v}"
    return 0
  fi
  if [[ -f "$latest" ]]; then
    local v=""
    if command -v jq >/dev/null 2>&1; then
      v="$(jq -r '.version // empty' "$latest" 2>/dev/null || true)"
    else
      v="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$latest" | head -1)"
    fi
    if [[ -n "$v" && "$v" != "null" ]]; then
      printf '%s\n' "${v#v}"
      return 0
    fi
  fi
  printf 'unknown\n'
}

shell_config_file() {
  local shell_name
  shell_name="$(basename "${SHELL:-sh}")"

  case "$shell_name" in
    zsh)
      printf '%s\n' "$HOME/.zshrc"
      ;;
    fish)
      mkdir -p "$HOME/.config/fish"
      printf '%s\n' "$HOME/.config/fish/config.fish"
      ;;
    bash)
      if [[ "$(uname -s)" == "Darwin" ]]; then
        printf '%s\n' "$HOME/.bash_profile"
      else
        printf '%s\n' "$HOME/.bashrc"
      fi
      ;;
    *)
      printf '%s\n' "$HOME/.profile"
      ;;
  esac
}

path_entry_for_shell() {
  local shell_name
  shell_name="$(basename "${SHELL:-sh}")"

  if [[ "$shell_name" == "fish" ]]; then
    printf 'set -gx PATH "%s" $PATH\n' "$BIN_DIR"
  else
    printf 'export PATH="%s:$PATH"\n' "$BIN_DIR"
  fi
}

ensure_path_markers() {
  local config_file
  config_file="$(shell_config_file)"
  touch "$config_file"

  if grep -Fq "$PATH_MARKER_START" "$config_file"; then
    return 0
  fi

  {
    printf '\n%s\n' "$PATH_MARKER_START"
    path_entry_for_shell
    printf '%s\n' "$PATH_MARKER_END"
  } >> "$config_file"

  print_info "Added $BIN_DIR to PATH in $config_file"
}

ensure_path() {
  if [[ -x "$BIN_DIR/proveo" ]]; then
    if PATH="$BIN_DIR:$PATH" "$BIN_DIR/proveo" setup >/dev/null 2>&1; then
      return 0
    fi
  fi
  ensure_path_markers
}

check_docker() {
  if command -v docker >/dev/null 2>&1; then
    return 0
  fi

  cat <<'EOF'

Docker was not found on this machine.

proveo runs published Docker images, so Docker must be installed before running containers:
  https://docs.docker.com/get-docker/

After Docker is installed, run:
  proveo --ls
EOF
}

print_post_install_message() {
  local shell_name version="$1"
  shell_name="$(basename "${SHELL:-sh}")"

  local path_cmd
  local reload_cmd
  local config_file
  config_file="$(shell_config_file)"
  local pretty_config="${config_file/#$HOME/\~}"

  if [[ "$shell_name" == "fish" ]]; then
    path_cmd="set -gx PATH \"$BIN_DIR\" \$PATH"
    reload_cmd="source $pretty_config"
  else
    path_cmd="export PATH=\"$BIN_DIR:\$PATH\""
    reload_cmd="source $pretty_config"
  fi

  cat <<EOF

proveo v$version installed to:
  $BIN_DIR/proveo

Open a new shell or run:
  $path_cmd

Or reload your current configuration:
  $reload_cmd

Then try:
  proveo --version
  proveo update --check
  proveo --ls
EOF
}

# --- main ---

read -r OS ARCH < <(detect_platform)
ASSET_NAME="proveo-${OS}-${ARCH}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$BIN_DIR"

print_info "Resolving release channel…"
if download_file "$ASSET_BASE_URL/latest.json" "$TMP_DIR/latest.json" 2>/dev/null; then
  :
else
  print_info "latest.json not available — falling back to checksums.txt only"
fi

PROVEO_VERSION="$(resolve_channel_version "$TMP_DIR/latest.json")"
print_info "Downloading $ASSET_NAME (v$PROVEO_VERSION)..."
download_file "$ASSET_BASE_URL/checksums.txt" "$TMP_DIR/checksums.txt"
download_file "$ASSET_BASE_URL/bin/$ASSET_NAME" "$TMP_DIR/$ASSET_NAME"
download_file "$CLI_BASE_URL/uninstall.sh" "$INSTALL_ROOT/uninstall.sh"

verify_checksum "$TMP_DIR/$ASSET_NAME" "$TMP_DIR/checksums.txt"

cp "$TMP_DIR/$ASSET_NAME" "$BIN_DIR/proveo"
chmod +x "$BIN_DIR/proveo" "$INSTALL_ROOT/uninstall.sh"

ensure_path
check_docker
print_post_install_message "$PROVEO_VERSION"
