# Coding Harness Paradigms

This directory defines the intended working mode for each harness so that entrypoints, configs, and steering files can be optimized consistently.

## The Four Paradigms

| Harness | Analogy | Core Idea | Key Files | Permission Posture |
|-------------|----------------------|------------------------------------------------|-------------------------------|----------------------------------------|
| Claude Code | ML blackbox algorithm | Spec → plan → implement → verify → loop | `CLAUDE.md`, entrypoint.sh | `--dangerously-skip-permissions` (intentional) |
| Cursor CLI | Policy-gated blackbox | Same autonomous loop, bounded by native deny rules + hooks | `.cursor/rules/*.mdc`, `cli-config.json`, `hooks.json` | `--force` (deny rules and enterprise hooks still win) |
| OpenCode | GStack subagent crew | Software engineering team with HITL oversight | `AGENTS.md`, subagents, config | Role-based; reviewers read-only |
| Cecli | Pair-programming sniper | Precise, low-token, human-guided edits | `CONVENTIONS.md`, config | Conservative; no blanket dangerous |

## Files in This Directory

- `claudecode.paradigm.md` — ML loop harness with explicit dangerous permissions.
- `cursor.paradigm.md` — ML loop harness whose safety boundary is layered: in-process deny rules and root-owned audit hooks survive `--force`.
- `opencode.paradigm.md` — Team workflow with subagent orchestration.
- `cecli.paradigm.md` — Pair-programming specialist with containment focus.

## Runtime User Boundary

Every harness container runs as the invoking host user, never root:

- **Host orchestrator** (`proveo run` in `cmd/proveo`, via `internal/runner`) launches with `docker run --user $(id -u):$(id -g)`, so files written to bind mounts come back owned by the developer — for any host uid, not just the image's baked default. Thin `defs/*/run.sh` shims only `exec` that binary.
- **Images** bake a non-root default user (uid 1000) and set `USER`, so even a bare `docker run` without the wrapper is never root.
- **Entrypoints** call the shared `ensure_runtime_user` helper (`packages/lib/entrypoint-lib.sh`) first: it gives an arbitrary run-as uid a passwd identity and a writable `HOME` without root. There is no gosu and no in-container privilege drop; this is one generic helper, identical across harnesses. **

## Git Identity & Context

Every harness image bakes `git` and `gh`. Containers ship no `~/.gitconfig` and never mount the host's, so commit identity flows through the environment:

- **Host orchestrator** forwards the invoking developer's identity as bare `-e GIT_AUTHOR_*` / `GIT_COMMITTER_*` (explicit env wins, else host `git config`) via `internal/gitidentity` — secrets stay off argv.
- **Entrypoints** bridge those env values into git's config-env (`bridge_git_identity` in `packages/lib/entrypoint-lib.sh`) so `git config --get` resolves file-free; repo-local identity stays authoritative.
- **Entrypoints report** the git context at startup (`report_git_context`): git-tracked repo or not, remote origin (or "not tracking a remote repo"), commit identity, and whether a gh session is authenticated.

## Network Egress Boundary

The dangerous in-container posture (especially Claude Code's `--dangerously-skip-permissions`) is made acceptable by a network egress layer, not just the container. Orchestration lives in Go (`internal/egress` + `cmd/proveo-egress`), driven by `proveo run --egress-mode` for every harness with `egress: true` in its `harness.manifest`:

- **broker** — direct bridge egress (container boundary only; ex-open).
- **proxy** — agent → Squid enforcement proxy; HTTP/HTTPS only, non-web protocols blocked by Docker network topology.
- **firewall** — agent → proveo-egress (TLS MITM + credential broker) → Squid → internet, with the agent trusting the inspector CA (**default**).

It serves two purposes:

1. **No irreversible action without HITL** — write methods (and pushes/publishes) are denied except to the model provider; attempts are logged.
2. **No leaks when using cloud LLM providers** — inference egress is pinned to an allowlisted provider, auto-detected from the API key present, while web reads (docs/search/scraping) stay open so agents can still gather context.

A local model can be assigned with `--local-model` (an Ollama sidecar serving host models offline, `NO_PROXY`-bypassed) for harnesses that speak OpenAI-compatible / litellm Ollama (`opencode`, `cecli`). Each run writes a top-allowed/top-denied egress report. See `claudecode.paradigm.md` and `defs/claudecode/claudecode-egress-topology.puml` for the full topology.

**DinD:** harnesses with `dind: true` (images that ship a docker client) may get a sibling privileged `docker:dind` when the scope has Dockerfiles/Compose and `PROVEO_DIND=1` (or an interactive yes). Lifecycle: `internal/dind`.

**Local-model exceptions:** Cursor is vendor-pinned (no custom base-URL; `--local-model` does not apply; provider pin maps `CURSOR_API_KEY` to `.cursor.sh`/`.cursor.com` — see `defs/cursor/cursor.paradigm.md`). Claude Code speaks the Anthropic API shape, not Ollama's OpenAI shape, so the sidecar is not a drop-in.

## Credential Boundary

Pinning *where* inference may go is only half the guarantee; the other half is *what secret the agent holds*. By default the provider key reaches the agent process (sourced from `.env` or forwarded via `-e`), so an autonomous agent can read its own environment and — absent method-level enforcement — attempt to send that key anywhere. In `firewall` mode this is closed by a **credential broker** on the inspection hop (the only point where TLS is decrypted), importing omnigent's `credential_proxy` principle ("inject keys, never expose"), adapted to the constraint that the *vendor CLI*, not the harness, makes the model call:

- **Inject** — the real provider credential is confined to the broker proxy, read at startup from a `0600` env-file mounted outside every agent mount (the same discipline that protects the CA private key). The broker sets the correct auth header on requests to the pinned-provider host only.
- **Strip** — credential headers (`authorization`, `x-api-key`, `x-goog-api-key`, `api-key`, `proxy-authorization`) are removed from requests to every other host, so a key the agent read from a mounted `.env` is useless for exfiltration at the network layer.
- **Sentinel** ** — in `firewall` mode `proveo run` forwards declared/detected secret env vars as the sentinel value (`proveo-brokered`) and sets `PROVEO_CREDENTIAL_BROKER_KEYS`; `proveo-entrypoint` / `apply_broker_sentinel` rewrites any residual load. The real secret stays in the MITM broker env-file only. A key committed to a *mounted* `.env` is still readable as a file in broker mode — use firewall + host-env provisioning for full isolation.

The broker is a property of `firewall` mode; `broker`/`proxy` modes cannot decrypt TLS, so they keep the key-in-env behavior with the existing honest warnings. **Implementation:** the inspector is `proveo-egress`, a Go MITM proxy (`cmd/proveo-egress`, `internal/{egressproxy,broker,provider}`) built on martian that records flows and brokers credentials — not a Python mitmproxy addon. The enforcement layer carries no static broad provider allowlist (`defs/sidecars/squid-proxy/squid.conf`); the tight per-provider pin from the provider registry is the sole write-allow.

HTTPS method/path policy (read-allow / write-deny / DLP) on the same MITM hop is specified in [`egress-policy.md`](egress-policy.md) and the linked `.puml` diagrams (`internal/egresspolicy`). See and .

## Usage

Entrypoints and default configs should reference these documents when seeding steering files or deciding defaults. Changes to any harness must preserve the paradigm described here.
