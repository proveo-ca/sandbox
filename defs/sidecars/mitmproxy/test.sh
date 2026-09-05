#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../../lib/docker-build.sh
source "$SCRIPT_DIR/../../lib/docker-build.sh"

IMAGE_NAME="$(proveo_test_image "${PROVEO_MITMPROXY_IMAGE:-proveo/mitmproxy:latest}")"

docker run --rm --entrypoint /bin/bash "$IMAGE_NAME" -lc '
  set -e
  mitmdump --version >/dev/null
  test -f /addons/ndjson_dump.py
  test -f /entrypoint.sh
'
