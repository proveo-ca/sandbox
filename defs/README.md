# Image Definitions

This directory contains Docker image definitions used by Proveo. A definition may be a coding harness, an experimental harness, or a non-harness utility image.

## Coding Harness Contract

A mature coding harness definition under `defs/<name>/` should expose, where applicable:

```txt
Dockerfile or Dockerfile.*
entrypoint.sh
help.sh, optional
README.md
sample config files
```

Its image suite lives outside the definition, at `internal/imagetest/<name>_test.go`.

Definition-local scripts are the preferred deterministic command surface:

- The build recipe (parent, pins, variants) is the def's row in `internal/imagebuild/targets.go`; `mise run build <target>` runs it.
- `proveo run <target>` runs the harness with the manifest's mounts and environment.
- `mise run test-defs <target>` (or `proveo test <target>`) runs the image suite, `go test -tags=image -run '^TestImage<Name>$' ./internal/imagetest/`.
- `proveo run <target> --shell` opens a troubleshooting shell with the same mounts and env.

## Current Classification

No definition is considered mature yet; this project is still standardizing the contract.

- Candidate coding harnesses: `cecli`, `opencode`, `claudecode`, `codex`, `cursor`
- Non-harness sidecar image definitions live under `defs/sidecars/` (e.g. `mitmproxy`, `squid-proxy`)

Non-harness sidecar image definitions live under `defs/sidecars/` and are not required to satisfy the coding harness contract.

## Package Boundary

Keep definitions under `defs/` until a stronger package boundary is justified. Shared code should move to `packages/` only when duplication warrants it.