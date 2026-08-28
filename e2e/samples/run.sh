#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=== Starting Polyglot Hello World Monorepo ==="

# Start Go API in background
echo "Starting Go API in background..."
cd "$DIR/apps/api"
go run main.go > /tmp/go-api.log 2>&1 &
API_PID=$!

cleanup() {
  echo "Cleaning up background processes (Go API PID: $API_PID)..."
  kill "$API_PID" || true
}
trap cleanup EXIT

echo "Waiting for Go API to start..."
for i in {1..10}; do
  if curl -s http://localhost:8080/health > /dev/null 2>&1; then
    echo "Go API is online and healthy!"
    break
  fi
  sleep 0.5
done

# Run Rust harness
echo "Running Rust Harness test..."
cd "$DIR/apps/harness"
cargo run

# Launch Bun TUI
echo "Launching Bun TUI..."
cd "$DIR/apps/tui"
bun index.ts
