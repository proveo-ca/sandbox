# Contributing

Guidelines for adding or changing image definitions under `defs/`. The general definition
contract lives in [`CODING_HARNESSES.md`](CODING_HARNESSES.md); each harness's intended
working mode lives in [`_spec/_paradigms/harness-paradigms.puml`](_spec/_paradigms/harness-paradigms.puml) and
`_spec/defs/<name>/<name>.paradigm.md`. This file collects the cross-cutting rules every
contribution must satisfy.

## Image builds (buildx multi-arch)

`mise run build` / `proveo build` drive `docker buildx` from Go
([`internal/imagebuild`](internal/imagebuild)); each target's recipe is one row of
`imagebuild.Specs`. No host shell runs, so macOS's frozen `/bin/bash` 3.2 is out of the build path. Defaults:

- **Platforms:** `linux/amd64,linux/arm64` (`PROVEO_PLATFORMS` to override)
- **Local build:** `--load` of the **host** platform only (buildx cannot load a multi-arch image into the engine)
- **Deploy:** `mise run deploy` / `proveo deploy` rebuilds with `--push` and publishes a multi-arch manifest to Docker Hub (plain `docker push` of a local image is not enough)

Requires Docker Buildx. A `proveo-multiarch` builder (`docker-container` driver) is created on first use (`PROVEO_BUILDX_BUILDER` to override).

## Runtime User Boundary (required)

Every harness runs as a non-root user, never root
(introduced repo-wide by `c2ad88f` — "[FIX] Ensure running with local user (#7)"):

- **The runner** (`proveo run`, `internal/run`) launches every agent in sbx, which runs the
 image's baked user (uid 1000). Every Kit startup step proveo writes (`internal/sbx/kit.go`)
 declares `user: "1000"`, except the one root step that creates a linked worktree's directory.
- **Images** bake a non-root default user (uid 1000) and set `USER`, so even a bare
 `docker run` without the wrapper is never root. Use the shared create-or-rename block
 (`ARG USER_ID=1000` / `ARG USER_NAME=<harness>`; see any existing `defs/*/Dockerfile`) so
 base images that already ship uid 1000 are renamed instead of duplicated. Strip
 setuid/setgid bits and remove raw network helpers (`nc`, `netcat`, `netstat`, `ss`).
- **Entrypoints** call the shared `ensure_runtime_user` helper
 (`packages/lib/entrypoint-lib.sh` / `proveo-entrypoint`) first: it gives an arbitrary run-as
 uid a passwd identity and a writable `HOME` without root. There is no gosu and no
 in-container privilege drop; this is one generic helper, identical across harnesses. Never
 reintroduce gosu, sudo, or per-image uid branching.

Also from that commit, and equally required: bake `git` and `gh` into every harness image,
forward the developer's identity via `internal/gitidentity` in `proveo run`, and bridge it
file-free with `proveo-entrypoint` / `BridgeGitIdentity` in the entrypoint.

## Secrets and `.env` mounts (required)

**Do not bind-mount a project `.env` that contains provider API keys into the agent
container** when documenting or implementing a security-sensitive path. Mounted secrets
are readable by an autonomous agent regardless of in-tool deny rules; entrypoints that
`source` `.env` also export those values into the agent process.

Contributors should treat `.env` handling as follows:

- **Target posture:** keys live in the host environment. With `--credentials broker` (the
 default) proveo puts them in sbx's secret store; with `--credentials forward` they enter the
 sandbox environment.
- **Inside sbx:** `load_env` (`packages/lib/entrypoint-lib.sh`) returns early when
 `SANDBOX_VM_ID` is set, so the workspace `.env` never reaches the agent's environment.
 Document that in harness READMEs when `.env` autoload is mentioned.

When adding or changing mount logic (`internal/workspace`, `internal/backend/sandbox`), prefer
sbx's secret store over new `.env` bind mounts unless the change is explicitly scoped to
symlink resolution or smoke-test fixtures.

### Credential broker (`--credentials broker`, the default)

sbx's credential proxy is the broker. The key sits in sbx's secret store, and the proxy attaches
it to requests for the provider's domains, so the agent never holds it. `--credentials forward`
keeps the key-in-agent behavior with the existing honest warnings (see
`_spec/_paradigms/credential-boundary.puml`).

## Enforcement

The boundary is asserted in Go (`go test ./internal/contract/ ./internal/verify/ …`, part of
`mise run ci`) and exercised live in each definition's image suite, `internal/imagetest/<name>_test.go`
(`mise run test-defs <name>` or `proveo test <name>`).
When you add a definition:

1. Add its entrypoint to the `ensure_runtime_user` / no-gosu loop and add wrapper
 (git identity) and Dockerfile (`USER ${USER_NAME}`, no gosu, git/gh) assertions.
2. Prefer `proveo-entrypoint` for shared prelude; keep harness-specific launch in `entrypoint.sh`.
3. Cover the runtime posture in the definition's image suite, `internal/imagetest/<name>_test.go`
 (build tag `image`, func `TestImage<Name>`): runs as the baked user, no setuid binaries, no `nc`.
4. Run `mise run ci` (or `go test ./internal/contract/`) — it must pass.

## Definition checklist

- `Dockerfile`, `entrypoint.sh`, `harness.manifest`, `README.md` (the build recipe is a row in `internal/imagebuild/targets.go`; the image suite is `internal/imagetest/<name>_test.go`)
 per the [coding harness contract](CODING_HARNESSES.md).
- A paradigm doc + topology diagram under `_spec/defs/<name>/`, referenced from source via
 `# SPEC:` comments (see `_spec/_conventions/spec-conventions.puml` for the lifecycle rules).
- Baked defaults stay container-internal: never mutate the user-mounted workspace on first
 run; workspace seeding is opt-in and re-seeding is explicit (`<HARNESS>_RESEED=1`).
- Validate any new/edited `.puml` with `plantuml -checkonly`.
