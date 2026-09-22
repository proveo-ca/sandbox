#!/usr/bin/env bash
# SPEC: _spec/_devops/image-lineage-and-publish.puml, _spec/_plans/host-shell-to-go.puml

proveo_ref_tag() {
  local last="${1##*/}"
  case "$last" in
    *:*) printf '%s' "${last##*:}" ;;
    *) printf 'latest' ;;
  esac
}

proveo_image_created() {
  local ts
  ts="$(docker image inspect "$1" --format '{{.Created}}' 2>/dev/null)" || return 1
  [[ -n "$ts" ]] || return 1
  ts="${ts%.*}"; ts="${ts%Z}"
  date -u -d "${ts}Z" +%s 2>/dev/null \
    || date -u -j -f '%Y-%m-%dT%H:%M:%S' "$ts" +%s 2>/dev/null \
    || return 1
}

# SPEC: _spec/_devops/image-lineage-and-publish.puml
proveo_resolve_image() {
  local ref="$1" repo local_ref local_at pub_at
  [[ "$(proveo_ref_tag "$ref")" == "latest" ]] || { printf '%s' "$ref"; return 0; }
  repo="${ref%:*}"
  local_ref="${repo}:local"

  if ! local_at="$(proveo_image_created "$local_ref")"; then
    printf '%s' "$ref"; return 0
  fi
  if ! pub_at="$(proveo_image_created "$ref")" || (( local_at > pub_at )); then
    printf '%s' "$local_ref"; return 0
  fi
  printf '%s' "$ref"
}

proveo_test_image() {
  local ref chosen
  ref="$1"
  chosen="$(proveo_resolve_image "$ref")"
  if [[ "$chosen" != "$ref" ]]; then
    echo "🧪 image: $chosen (local build — newer than the published tag)" >&2
  else
    echo "🧪 image: $chosen (published)" >&2
  fi
  printf '%s' "$chosen"
}
