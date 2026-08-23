#!/usr/bin/env bash
# SPEC: _spec/internal/cdn/distribution-update.puml
# Sidecar uninstall for curl-pipe installs.
set -euo pipefail

INSTALL_ROOT="${PROVEO_INSTALL_ROOT:-$HOME/.proveo}"
BIN_DIR="$INSTALL_ROOT/bin"

# Prefer the Go command when this binary actually lists `uninstall`
# (older CDN artifacts may still be on disk during upgrade windows).
if [[ -x "$BIN_DIR/proveo" ]] && "$BIN_DIR/proveo" --help 2>/dev/null | grep -qE '^[[:space:]]+uninstall[[:space:]]'; then
  export PROVEO_INSTALL_ROOT
  if [[ "${PROVEO_UNINSTALL_ASSUME_YES:-}" == "1" ]] || [[ ! -t 0 ]]; then
    exec "$BIN_DIR/proveo" uninstall --yes
  fi
  exec "$BIN_DIR/proveo" uninstall
fi

# Fallback: strip PATH markers and remove the install root (pre-uninstall-cmd binaries).

PATH_MARKER_START="# Added by proveo install.sh"
PATH_MARKER_END="# End added by proveo install.sh"

print_info() {
  printf '%s\n' "$*"
}

print_warning() {
  printf 'Warning: %s\n' "$*" >&2
}

remove_path_block() {
  local config_file="$1"
  local tmp_file
  local status=0

  [[ -f "$config_file" ]] || return 0

  tmp_file="$(mktemp)"
  awk \
    -v start="$PATH_MARKER_START" \
    -v end="$PATH_MARKER_END" \
    -v posix_path="export PATH=\"$BIN_DIR:\$PATH\"" \
    -v fish_path="set -gx PATH \"$BIN_DIR\" \$PATH" '
    $0 == start { skip = 1; next }
    $0 == end { skip = 0; next }
    $0 == posix_path { changed = 1; next }
    $0 == fish_path { changed = 1; next }
    skip != 1 { print }
    skip == 1 { changed = 1 }
    END { if (skip == 1) changed = 1; exit changed ? 2 : 0 }
  ' "$config_file" > "$tmp_file" || status="$?"

  if [[ "${status:-0}" -eq 0 ]]; then
    rm -f "$tmp_file"
    return 0
  fi

  if [[ "${status:-0}" -ne 2 ]]; then
    rm -f "$tmp_file"
    print_warning "Could not update $config_file"
    return 0
  fi

  mv "$tmp_file" "$config_file"
  print_info "Removed proveo PATH entries from $config_file"
}

remove_install_root() {
  if [[ -z "$INSTALL_ROOT" || "$INSTALL_ROOT" == "/" || "$INSTALL_ROOT" == "$HOME" ]]; then
    print_warning "Refusing to remove unsafe install root: $INSTALL_ROOT"
    return 0
  fi
  rm -rf -- "$INSTALL_ROOT"
}

confirm_uninstall() {
  if [[ "${PROVEO_UNINSTALL_ASSUME_YES:-}" == "1" ]]; then
    return 0
  fi
  printf 'This will remove proveo from %s. Continue? [y/N] ' "$INSTALL_ROOT"
  read -r answer
  case "$answer" in
    y|Y|yes|YES) return 0 ;;
    *)
      print_info "Uninstall cancelled."
      exit 0
      ;;
  esac
}

confirm_uninstall

remove_path_block "$HOME/.zshrc"
remove_path_block "$HOME/.bashrc"
remove_path_block "$HOME/.bash_profile"
remove_path_block "$HOME/.profile"
remove_path_block "$HOME/.config/fish/config.fish"

remove_setup_markers() {
  local config_file="$1"
  local tmp_file status=0
  [[ -f "$config_file" ]] || return 0
  tmp_file="$(mktemp)"
  awk '
    $0 == "# added by `proveo setup` — proveo on PATH" { skip = 1; next }
    skip == 1 && ($0 ~ /^export PATH=/ || $0 ~ /^set -gx PATH / || $0 ~ /^setenv PATH /) { skip = 0; changed = 1; next }
    skip == 1 { changed = 1; next }
    { print }
    END { exit changed ? 2 : 0 }
  ' "$config_file" >"$tmp_file" || status="$?"
  if [[ "${status:-0}" -eq 2 ]]; then
    mv "$tmp_file" "$config_file"
  else
    rm -f "$tmp_file"
  fi
}
remove_setup_markers "$HOME/.zshrc"
remove_setup_markers "$HOME/.bashrc"
remove_setup_markers "$HOME/.bash_profile"
remove_setup_markers "$HOME/.profile"
remove_setup_markers "$HOME/.config/fish/config.fish"

remove_install_root

if command -v proveo >/dev/null 2>&1; then
  print_warning "proveo is still resolvable in this shell: $(command -v proveo)"
  print_warning "If this points at GOPATH/bin or a repo checkout, it is not the distributed install."
  print_warning "Open a new shell, or run 'hash -r' in bash / 'rehash' in zsh."
else
  print_info "proveo uninstalled."
fi
