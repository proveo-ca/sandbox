#!/usr/bin/env bash
set -euo pipefail

IMAGE_NAME="${PROVEO_SQUID_PROXY_IMAGE:-ubuntu/squid:latest}"
CONFIG_DIR="$PWD/config"
PORT="3128"

usage() {
  cat <<'EOF'
Usage:
  ./run.sh [--image <image>] [--config-dir <path>] [--port <port>]

Runs the Squid proxy harness for egress enforcement.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --image)
      if [[ $# -lt 2 ]]; then
        echo "--image requires a value" >&2
        exit 1
      fi
      IMAGE_NAME="$2"
      shift 2
      ;;
    --config-dir)
      if [[ $# -lt 2 ]]; then
        echo "--config-dir requires a value" >&2
        exit 1
      fi
      CONFIG_DIR="$2"
      shift 2
      ;;
    --port)
      if [[ $# -lt 2 ]]; then
        echo "--port requires a value" >&2
        exit 1
      fi
      PORT="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown run option: $1" >&2
      usage
      exit 1
      ;;
  esac
done

mkdir -p "$CONFIG_DIR"

if [[ ! -f "$CONFIG_DIR/squid.conf" ]]; then
  cp "$(dirname "$0")/squid.conf" "$CONFIG_DIR/squid.conf"
fi
if [[ ! -f "$CONFIG_DIR/firehol-blocked-nets.conf" ]]; then
  cp "$(dirname "$0")/firehol-blocked-nets.conf" "$CONFIG_DIR/firehol-blocked-nets.conf"
fi
if [[ ! -f "$CONFIG_DIR/firehol-ipset.conf" ]]; then
  cp "$(dirname "$0")/firehol-ipset.conf" "$CONFIG_DIR/firehol-ipset.conf"
fi
if [[ ! -f "$CONFIG_DIR/provider-allow.conf" ]]; then
  cp "$(dirname "$0")/provider-allow.conf" "$CONFIG_DIR/provider-allow.conf"
fi

docker run -it --rm \
  -p "$PORT:3128" \
  -v "$CONFIG_DIR:/etc/squid:ro" \
  "$IMAGE_NAME"
