#!/usr/bin/env bash
# SPEC: _spec/cmd/proveo/provision-and-targets.puml, _spec/_devops/image-lineage-and-publish.puml
# Maintainer target registry, sourced from the Go `proveo targets` command — the
# single source of truth. Replaces the Bash manifest parser (manifest-enum.sh)
# and the target/image/dir maps (runners.sh): Go now owns "what targets exist",
# this file just consumes them. Populates TARGETS + parallel REG_* arrays and
# offers reg_image / reg_dir lookups. Requires REPO_ROOT.

# _resolve_proveo picks the proveo binary for maintainer tooling and exports
# PROVEO_BIN. Precedence: an explicit PROVEO_BIN (the `debug` task sets it to a
# freshly-built binary) → an installed `proveo` on PATH, but ONLY if it is current
# enough to know `targets` (a stale pre-migration binary would silently break the
# registry) → otherwise leave it unset so proveo_maint builds from the repo via
# `go run`. The `targets` probe runs from REPO_ROOT so it sees the defs/ tree.
_resolve_proveo() {
  [[ -n "${PROVEO_BIN:-}" && -x "${PROVEO_BIN}" ]] && return
  local p
  if p="$(command -v proveo 2>/dev/null)" && ( cd "$REPO_ROOT" && "$p" targets >/dev/null 2>&1 ); then
    export PROVEO_BIN="$p"
  fi
}

# proveo_maint runs the proveo CLI for maintainer tooling: PROVEO_BIN (resolved
# above) when set, else `go run` from the repo — always the current source of
# truth, never a possibly-stale binary chosen blindly off PATH.
proveo_maint() {
  if [[ -n "${PROVEO_BIN:-}" && -x "${PROVEO_BIN}" ]]; then
    "$PROVEO_BIN" "$@"
    return
  fi
  ( cd "$REPO_ROOT" && go run ./cmd/proveo "$@" )
}

# proveo_load_registry fills TARGETS + REG_NAMES/REG_IMAGES/REG_DIRS from
# `proveo targets` (name<TAB>image<TAB>defDir). Bash 3.2-safe (parallel arrays,
# no associative arrays).
proveo_load_registry() {
  TARGETS=()
  REG_NAMES=()
  REG_IMAGES=()
  REG_DIRS=()
  local name image dir
  while IFS=$'\t' read -r name image dir; do
    [[ -n "$name" ]] || continue
    TARGETS+=("$name")
    REG_NAMES+=("$name")
    REG_IMAGES+=("$image")
    REG_DIRS+=("$dir")
  done < <(proveo_maint targets)
  if [[ ${#TARGETS[@]} -eq 0 ]]; then
    echo "❌ no targets from 'proveo targets' (is the defs/ tree present, or go available?)" >&2
    exit 1
  fi
}

# reg_image / reg_dir look up a target by name (linear scan — bash 3.2).
reg_image() {
  local want="$1" i
  for i in "${!REG_NAMES[@]}"; do
    if [[ "${REG_NAMES[$i]}" == "$want" ]]; then
      printf '%s\n' "${REG_IMAGES[$i]}"
      return 0
    fi
  done
  print_error "No image mapping for target: $want"
  exit 1
}

reg_dir() {
  local want="$1" i
  for i in "${!REG_NAMES[@]}"; do
    if [[ "${REG_NAMES[$i]}" == "$want" ]]; then
      printf '%s\n' "${REG_DIRS[$i]}"
      return 0
    fi
  done
  print_error "No directory mapping for target: $want"
  exit 1
}

# debug_target opens a shell in the harness via the Go CLI (unchanged behavior).
debug_target() {
  local target="$1"
  local tag="$2"
  shift 2
  local -a extra_args=("$@")
  local run_t="$target"

  local -a args=(run "$run_t" --shell)
  if [[ -n "$tag" && "$tag" != "latest" ]]; then
    args+=(--image "$(reg_image "$run_t"):$tag")
  fi
  proveo_maint "${args[@]}" ${extra_args[@]+"${extra_args[@]}"}
}

_resolve_proveo
proveo_load_registry
