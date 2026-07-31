# Image Definitions

This directory contains Docker image definitions used by Proveo. A definition may be a coding harness, an experimental harness, or a non-harness utility image.

## Coding Harness Contract

A mature coding harness definition under `defs/<name>/` should expose, where applicable:

```txt
Dockerfile or Dockerfile.*
entrypoint.sh
build.sh
run.sh
test.sh
debug.sh, optional
help.sh, optional
README.md
sample config files
tests/, if applicable
```

Definition-local scripts are the preferred deterministic command surface:

- `build.sh` builds the image or image variants.
- `run.sh` runs the harness with documented mounts and environment variables.
- `test.sh` runs smoke tests or the definition-local test suite.
- `debug.sh`, when present, opens a troubleshooting shell or equivalent debug workflow.

## Current Classification

No definition is considered mature yet; this project is still standardizing the contract.

- Candidate coding harnesses: `cecli`, `opencode`, `claudecode`, `cursor`
- Non-harness sidecar image definitions live under `defs/sidecars/` (e.g. `mitmproxy`, `squid-proxy`)

Non-harness sidecar image definitions live under `defs/sidecars/` and are not required to satisfy the coding harness contract.

## Package Boundary

Keep definitions under `defs/` until a stronger package boundary is justified. Shared code should move to `packages/` only when duplication warrants it.