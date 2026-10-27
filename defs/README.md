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
- `sbx exec -it <sandbox> bash` opens a shell in a running harness sandbox (`sbx ls` names it).

## Current Classification

No definition is considered mature yet; this project is still standardizing the contract.

- Candidate coding harnesses: `opencode`, `claudecode`, `codex`, `cursor`
- Candidate general-purpose harness: `hermes` (native browser automation, not coding-focused)
- Non-harness base layers: `base`, `base-node`, `base-node-lsp`, `base-node-browser`

Non-harness base layers are not required to satisfy the coding harness contract.

## Package Boundary

Keep definitions under `defs/` until a stronger package boundary is justified. Shared code should move to `packages/` only when duplication warrants it.