#!/usr/bin/env bash
# Stage host proveo binaries + checksums + latest.json into apps/cli/public/cli
# for Cloudflare. Prefers goreleaser dist/ archives; falls back to cross-compiling.
# SPEC: apps/cli README — Go binary install via CDN
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CDN_ROOT="${PROVEO_CDN_ROOT:-$REPO_ROOT/apps/cli/public/cli}"
DIST_DIR="${PROVEO_DIST_DIR:-$REPO_ROOT/dist}"
OUT_BIN="$CDN_ROOT/bin"

# Every OS/arch we publish host binaries for. Linux covers Ubuntu/Fedora/Debian/…;
# windows binaries carry a .exe suffix and are consumed by install.ps1.
platforms=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "freebsd/amd64"
  "freebsd/arm64"
  "windows/amd64"
  "windows/arm64"
)

# bin_name is the on-disk binary basename inside archives / build dirs for a GOOS.
bin_name() { [[ "$1" == windows ]] && printf 'proveo.exe\n' || printf 'proveo\n'; }

# resolve_version picks the release stamp for latest.json + ldflags.
# Prefer PROVEO_VERSION, then goreleaser artifacts.json / VERSION, then git describe, else "dev".
resolve_version() {
  if [[ -n "${PROVEO_VERSION:-}" ]]; then
    printf '%s\n' "${PROVEO_VERSION#v}"
    return 0
  fi
  if [[ -f "$DIST_DIR/artifacts.json" ]] && command -v jq >/dev/null 2>&1; then
    local v
    v="$(jq -r '[.[] | .extra.Version // empty] | map(select(. != null and . != "")) | first // empty' "$DIST_DIR/artifacts.json" 2>/dev/null || true)"
    if [[ -n "$v" && "$v" != "null" ]]; then
      printf '%s\n' "${v#v}"
      return 0
    fi
  fi
  if [[ -f "$DIST_DIR/VERSION" ]]; then
    tr -d '[:space:]' <"$DIST_DIR/VERSION" | sed 's/^v//'
    return 0
  fi
  if command -v git >/dev/null 2>&1; then
    local tag
    tag="$(git -C "$REPO_ROOT" describe --tags --exact-match 2>/dev/null || true)"
    if [[ -n "$tag" ]]; then
      printf '%s\n' "${tag#v}"
      return 0
    fi
    tag="$(git -C "$REPO_ROOT" describe --tags --always 2>/dev/null || true)"
    if [[ -n "$tag" ]]; then
      printf '%s\n' "${tag#v}"
      return 0
    fi
  fi
  printf 'dev\n'
}

VERSION="$(resolve_version)"
export PROVEO_VERSION="$VERSION"

mkdir -p "$OUT_BIN"
# Drop legacy bash consumer assets if present. ${CDN_ROOT:?} guards the recursive
# rm so a mis-set/empty CDN_ROOT can never expand to `rm -rf /lib`.
rm -f "$OUT_BIN/proveo" "$OUT_BIN/help.sh" "$OUT_BIN/init.sh"
rm -rf "${CDN_ROOT:?}/lib"
rm -f "$OUT_BIN"/proveo-* "$CDN_ROOT/checksums.txt" "$CDN_ROOT/latest.json"

extract_from_dist() {
  local goos="$1" goarch="$2" dest="$3" bin="$4"
  local archive=""
  shopt -s nullglob
  local candidates=(
    "$DIST_DIR"/proveo_*_"${goos}"_"${goarch}".tar.gz
    "$DIST_DIR"/proveo_*_"${goos}"_"${goarch}".tgz
    "$DIST_DIR"/proveo_*_"${goos}"_"${goarch}".zip
  )
  shopt -u nullglob
  # bash 3.2 (macOS /usr/bin/env bash) errors on "${arr[@]}" for an empty array
  # under `set -u`; guard the expansion so the no-dist path doesn't crash.
  for archive in ${candidates[@]+"${candidates[@]}"}; do
    [[ -f "$archive" ]] || continue
    case "$archive" in
      *.zip)
        if unzip -Z1 "$archive" 2>/dev/null | grep -qE "(^|/)${bin}$"; then
          local member
          member="$(unzip -Z1 "$archive" 2>/dev/null | grep -E "(^|/)${bin}$" | head -1)"
          unzip -p "$archive" "$member" >"$dest"
          chmod +x "$dest"
          return 0
        fi
        ;;
      *)
        if tar -tzf "$archive" 2>/dev/null | grep -qE "(^|/)${bin}$"; then
          local member
          member="$(tar -tzf "$archive" 2>/dev/null | grep -E "(^|/)${bin}$" | head -1)"
          tar -xzf "$archive" -O "$member" >"$dest"
          chmod +x "$dest"
          return 0
        fi
        ;;
    esac
  done
  # Flat cdn-proveo archive (goreleaser formats: [binary], name proveo-OS-ARCH).
  local flat="$DIST_DIR/proveo-${goos}-${goarch}"
  if [[ -f "$flat" ]]; then
    cp "$flat" "$dest"
    chmod +x "$dest"
    return 0
  fi
  if [[ "$goos" == windows && -f "${flat}.exe" ]]; then
    cp "${flat}.exe" "$dest"
    chmod +x "$dest"
    return 0
  fi
  # Raw goreleaser build output (dist/proveo_<os>_<arch>_<variant>/<bin>).
  shopt -s nullglob
  for f in "$DIST_DIR"/proveo_"${goos}"_"${goarch}"*/"${bin}"; do
    if [[ -f "$f" ]]; then
      cp "$f" "$dest"
      chmod +x "$dest"
      return 0
    fi
  done
  shopt -u nullglob
  return 1
}

cross_compile() {
  local goos="$1" goarch="$2" dest="$3"
  echo "cross-compiling proveo ${goos}/${goarch} (version=$VERSION)..." >&2
  (
    cd "$REPO_ROOT"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o "$dest" ./cmd/proveo
  )
  chmod +x "$dest"
}

used_dist=0
for plat in "${platforms[@]}"; do
  goos="${plat%/*}"
  goarch="${plat#*/}"
  ext=""
  [[ "$goos" == windows ]] && ext=".exe"
  dest="$OUT_BIN/proveo-${goos}-${goarch}${ext}"
  bin="$(bin_name "$goos")"
  if extract_from_dist "$goos" "$goarch" "$dest" "$bin"; then
    used_dist=1
    echo "staged from dist: proveo-${goos}-${goarch}${ext}" >&2
  elif [[ "${PROVEO_CDN_REQUIRE_DIST:-0}" == "1" ]]; then
    {
      printf 'ERROR: release staging requires a goreleaser archive for %s/%s under\n' "$goos" "$goarch"
      printf '       %s, but none was found. Build real artifacts first:\n' "$DIST_DIR"
      printf '         mise run build-cli -- --release\n'
    } >&2
    exit 1
  else
    cross_compile "$goos" "$goarch" "$dest"
    echo "staged via go build: proveo-${goos}-${goarch}${ext}" >&2
  fi
done

(
  cd "$OUT_BIN"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum proveo-*
  else
    shasum -a 256 proveo-*
  fi
) >"$CDN_ROOT/checksums.txt"

# latest.json — versioned channel metadata for install.sh / proveo update.
json_escape() {
  # Minimal JSON string escape for version/asset names (no control chars expected).
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}
{
  printf '{\n  "version": "%s",\n  "checksums": {\n' "$(json_escape "$VERSION")"
  first=1
  while read -r sum name; do
    [[ -n "${sum:-}" && -n "${name:-}" ]] || continue
    if [[ "$first" -eq 1 ]]; then
      first=0
    else
      printf ',\n'
    fi
    printf '    "%s": "%s"' "$(json_escape "$name")" "$(json_escape "$sum")"
  done <"$CDN_ROOT/checksums.txt"
  printf '\n  }\n}\n'
} >"$CDN_ROOT/latest.json"

printf 'Wrote %s, %s/checksums.txt, %s/latest.json (version=%s)\n' "$OUT_BIN" "$CDN_ROOT" "$CDN_ROOT" "$VERSION" >&2
if [[ "$used_dist" -eq 0 ]]; then
  printf 'Note: no goreleaser archives found under %s — used go build fallback.\n' "$DIST_DIR" >&2
  printf 'For release artifacts: mise run build-cli -- --release (or mise run deploy-cli, which does that first)\n' >&2
fi
