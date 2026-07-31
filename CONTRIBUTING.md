# Contributing

Guidelines for adding or changing image definitions under `defs/`. The general definition
contract lives in [`CODING_HARNESSES.md`](CODING_HARNESSES.md); each harness's intended
working mode lives in [`_spec/paradigms.md`](_spec/paradigms.md) and
`_spec/defs/<name>/<name>.paradigm.md`. This file collects the cross-cutting rules every
contribution must satisfy.

## Image builds (buildx multi-arch)

`mise run build` / `proveo build` run each def's `build.sh`, which goes through
[`defs/lib/docker-build.sh`](defs/lib/docker-build.sh) (`docker buildx`). Defaults:

- **Platforms:** `linux/amd64,linux/arm64` (`PROVEO_PLATFORMS` to override)
- **Local build:** `--load` of the **host** platform only (buildx cannot load a multi-arch image into the engine)
- **Deploy:** `mise run deploy` / `proveo deploy` rebuilds with `--push` and publishes a multi-arch manifest to Docker Hub (plain `docker push` of a local image is not enough)

Requires Docker Buildx. A `proveo-multiarch` builder (`docker-container` driver) is created on first use (`PROVEO_BUILDX_BUILDER` to override).

## Runtime User Boundary (required)

Every harness container runs as the invoking host user, never root
(introduced repo-wide by `c2ad88f` — "[FIX] Ensure running with local user (#7)"):

- **Wrappers** (`defs/*/run.sh`, the distributable CLI runners) launch with
 `docker run --user $(id -u):$(id -g)`, so files written to bind mounts come back owned by
 the developer — for any host uid, not just the image's baked default. Pair it with the
 hardening baseline: `--cap-drop=ALL --security-opt=no-new-privileges:true` plus a
 host-scaled `--pids-limit` (never omitted). The limit is
 `clamp(cpus×256, 512, ceiling)` for base agents and `clamp(cpus×512, 1024, ceiling)` for
 `*-browser` images, where `ceiling = min(cpus×1024, pid_max/64)` from host CPU (cgroup-aware)
 and kernel `pid_max` (from `/proc` on Linux; on macOS/Windows, probed from the Docker
 engine's Linux VM via a local image — preferring the agent image, never pulled —
 falling back to `32768` only if both fail). Override with `PROVEO_PIDS_LIMIT`
 (clamped to `[256, ceiling]`).
 If the host ceiling (or override) is below the tier minimum (`512` base / `1024` browser),
 `proveo run` failfasts with `insufficient host pids capability` instead of starting a
 sandbox that will exhaust processes.
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

- **Target posture:** keys live in the host environment (or `docker run -e`); in
 `firewall` egress mode the credential broker injects on the pinned provider host from a
 secret file mounted **outside** every agent bind mount.
- **Pragmatic mounts today:** in `broker` mode, `internal/workspace` overlays a
 **symlink-resolved** file at `/app/.env`. In `proxy`/`firewall`, the same package
 **masks** `.env` with `/dev/null` so a whole-tree bind cannot expose secrets; the
 broker reads the host-side file instead. Document the caveat in harness READMEs
 when `.env` autoload is mentioned.
- **Warnings:** the Go CLI's `warnMountedSecrets` applies to `broker` mounts;
 broker-mode egress DLP still mitigates **egress** exfiltration if a secret leaks
 into the agent another way.

When adding or changing `run.sh` / `runners.sh` mount logic, prefer host-env forwarding
plus broker injection over new `.env` bind mounts unless the change is explicitly scoped
to symlink resolution or smoke-test fixtures.

### Credential broker (firewall mode only)

The credential broker is a property of **`firewall` egress mode only**. It runs on the
Go MITM sidecar (`proveo-egress`), the only hop where TLS is decrypted. `proxy` and
`broker`/`proxy` modes cannot inject or strip auth headers on HTTPS traffic; they keep the
key-in-agent behavior with the existing honest warnings (see `_spec/paradigms.md`,
Credential Boundary).

**Firewall gaps contributors should not worsen**

| Gap | Where | Status |
| --- | --- | --- |
| `.env` mounted into agent | `internal/workspace/mount.go` | **Closed** — masked with `/dev/null` in `proxy`/`firewall` |
| `load_env` always runs | `packages/lib/entrypoint-lib.sh` | **Closed** — skips when `PROVEO_EGRESS_MODE` is `proxy`/`firewall` |
| Go CLI forwards secrets in all modes | `cmd/proveo/main.go` | **Closed** — secret env only in `broker` |
| Distributable CLI forwards `CURSOR_API_KEY` | `apps/cli/public/cli/lib/runners.sh` | Firewall-only path (consumer CLI has no broker/proxy topology) |
| Broker reads host env only | `writeBrokerEnv` | **Closed** — host `.env` / `PROVEO_EGRESS_ENV_FILE` ingested into `broker.env` |
| Sentinel rewrite | `internal/entrypoint`, `packages/lib/entrypoint-lib.sh` | **Closed** — firewall injects sentinel + `PROVEO_CREDENTIAL_BROKER_KEYS` |

### Wrapper surface inconsistencies

These surfaces should stay aligned on credential forwarding:

| Surface | `firewall` / `proxy` intent | Current behavior |
| --- | --- | --- |
| `defs/cursor/run.sh` | No `CURSOR_API_KEY` in agent | Passes key only in `broker` |
| `defs/claudecode/run.sh` | No `CLAUDE_CODE_OAUTH_TOKEN` in agent | Passes token only in `broker` |
| `cmd/proveo run` | Match bash wrappers | Secret manifest env forwarded only in `broker` |
| `apps/cli` `run_cursor` | broker egress only | Always `-e CURSOR_API_KEY` (no broker/proxy topology) |

## Enforcement

The boundary is asserted in Go (`go test ./internal/contract/ ./internal/verify/ …`, via
`defs/tests/test_harness_contracts.sh`) and exercised live in each definition's `tests/` suite.
When you add a definition:

1. Add its entrypoint to the `ensure_runtime_user` / no-gosu loop and add wrapper
 (`--user`, git identity) and Dockerfile (`USER ${USER_NAME}`, no gosu, git/gh) assertions.
2. Prefer `proveo-entrypoint` for shared prelude; keep harness-specific launch in `entrypoint.sh`.
3. Cover the runtime posture in the definition's own `tests/test_security.sh` (runs as the
 baked user, no setuid binaries, no `nc`).
4. Run `bash defs/tests/test_harness_contracts.sh` — it must pass (delegates to `go test`).

## Definition checklist

- `Dockerfile`, `entrypoint.sh`, `build.sh`, `run.sh`, `test.sh`, `README.md`, `tests/`
 per the [coding harness contract](CODING_HARNESSES.md).
- A paradigm doc + topology diagram under `_spec/defs/<name>/`, referenced from source via
 `# SPEC:` comments (see `_spec/README.md` for the lifecycle rules).
- Baked defaults stay container-internal: never mutate the user-mounted workspace on first
 run; workspace seeding is opt-in and re-seeding is explicit (`<HARNESS>_RESEED=1`).
- Validate any new/edited `.puml` with `plantuml -checkonly`.
