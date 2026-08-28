# Polyglot Monorepo Samples

This directory contains a minimal polyglot hello-world monorepo to assert TUI testing for the container runner per `_spec/components.puml`.

## Structure

```
samples/
  package.json              # Workspace root (detects apps/ + packages/)
  run.sh                    # Orchestrator: starts Go API, runs Rust harness, launches Bun TUI
  apps/
    api/                    # Go Hello World REST API (port 8080)
    tui/                    # Bun interactive TUI (consumes @polyglot/utils)
    harness/                # Rust zero-dep TCP/HTTP verification harness
  packages/
    utils/                  # Shared TypeScript library (workspace package)
```

## Quick Start

```bash
cd samples
./run.sh
```

Or manually:

```bash
# Terminal 1: Start Go API
cd apps/api && go run main.go

# Terminal 2: Run Rust harness
cd apps/harness && cargo run

# Terminal 3: Run Bun TUI
cd apps/tui && bun index.ts
```

## Workspace Detection

The root `package.json` declares workspaces for both `apps/*` and `packages/*`, ensuring the container runner detects both deployables and shared utilities.
