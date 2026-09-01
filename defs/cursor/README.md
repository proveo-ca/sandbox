# cursor Docker Runner

Custom Docker image for the [Cursor CLI](https://cursor.com/docs/cli) (`agent`, legacy alias
`cursor-agent`) with:

- `proveo/base` (MCR `playwright` noble floor: Node, Chromium + OS deps, `pnpm` — the CLI
 itself is a self-contained binary)
- Root-free runtime: baked non-root user `cursor` (uid 1000); the run wrapper launches as the
 invoking host uid via `--user $(id -u):$(id -g)`
- CLI installed under a root-owned prefix (`/opt/cursor-dist`) — the agent cannot tamper with
 or self-update the binary; updating the CLI means rebuilding the image
- `.env` autoloading, git identity bridging, monorepo-friendly mounts
- Reusable network egress modes (`broker|proxy|firewall`)

Paradigm: **policy-gated autonomous loop** — see
[`_spec/defs/cursor/cursor-paradigm.puml`](../../_spec/defs/cursor/cursor-paradigm.puml).

## Browser variant

`./build.sh --browser` builds `proveo/cursor-browser` FROM `proveo/base-node-browser`: the
same `cursor-agent` binary atop a headless Chromium shared by the `playwright` CLI and
[vercel-labs/agent-browser](https://github.com/vercel-labs/agent-browser) (`open` ·
`snapshot` · `click` · `fill` · `screenshot`, accessibility-tree refs over CDP). The seed drops
agent-browser's discovery stub into `~/.cursor/skills/agent-browser/SKILL.md`, which points
the agent at `agent-browser skills get core` (the guide matching the installed binary) and
tells it not to run `agent-browser install`. Pick the `browser` add-on in the `proveo run`
picker to use this image; `PROVEO_BROWSER_SKILL=off` skips the skill. Details in
`defs/base-node-browser/README.md`.

## Contract Status

Candidate coding harness definition. This definition exposes:

- `Dockerfile`
- `entrypoint.sh`
- `build.sh`
- `run.sh`
- `test.sh`
- `README.md`
- `defaults/` (baked policy + steering)
- `tests/`

`debug.sh` is not present; `./run.sh --shell` covers the troubleshooting workflow.

This definition follows the shared [coding harness container contract](../../CODING_HARNESSES.md).

## Image Names and Mounts

- Default image: `proveo/cursor:latest`
- Build override: `PROVEO_CURSOR_IMAGE=example/cursor:tag ./build.sh`
- Run override: `./run.sh --image example/cursor:tag`
- Workspace mount: input directory mounted at `/app` (monorepo scope preserved under
 `/app/<relative-scope>` with root `.git` mounted alongside)

## Build

```bash
./build.sh # or: ./build.sh --tag local
```

The build runs the official installer (`https://cursor.com/install`), which resolves the
current CLI release — Cursor publishes no pinning env var. To pin, mirror the versioned
tarball from `downloads.cursor.com` and pass `--build-arg CURSOR_INSTALL_URL=<mirror>`.

## Run

```bash
./run.sh # interactive TUI in the current repo
./run.sh --egress-mode allowlist # fully audited egress (cursor needs --credentials forward)
./run.sh --shell # debug shell with the same mounts/env
```

### Headless (CI shape)

```bash
CURSOR_API_KEY=... ./run.sh -- -p "Fix the failing tests" --output-format stream-json
```

Any args after `--` are forwarded to `agent`. The entrypoint launches
`agent --force --sandbox disabled` and adds `--trust` automatically for `-p/--print` runs;
utility subcommands (`login`, `status`, `ls`, `mcp`, …) pass through without the autonomy
flags. Set `CURSOR_MODEL` to pin a model (`agent --list-models` enumerates valid ids), or use
the shared `.env` aliases `ARCHITECT_MODEL` / `EDITOR_MODEL` (see root `README.md`) — the
entrypoint bridges them into `CURSOR_MODEL` when `CURSOR_MODEL` is unset.

## Authentication

All inference transits the Cursor backend — there is **no** provider-API-key or local-model
alternative (`--local-model` is rejected by the wrapper).

| Method | How |
| ------ | --- |
| API key (recommended) | Create at cursor.com/dashboard → API Keys; export `CURSOR_API_KEY` (the wrapper forwards it) |
| Interactive login | `./run.sh -- login` — `NO_OPEN_BROWSER=1` is baked, so the URL is printed |

Login tokens from `agent login` write under `$HOME/.cursor` inside the container. Proveo
mounts a durable **proveo home** at `~/.proveo/.cursor` (override root with `PROVEO_HOME`)
and scrubs `auth.json` each run — prefer `CURSOR_API_KEY` so auth never lands in the cache.
Host IDE `~/.cursor` is never bind-mounted.

### Resume

```bash
proveo run cursor --ls
proveo run cursor --continue
proveo run cursor --resume <chat-id>
```

Sessions survive container `--rm` because chats live under proveo home, keyed by workspace
path `/app`.

## Baked-in policy defaults

The image ships defaults at `/opt/cursor/defaults/` and seeds them into `~/.cursor/` on first
run. Re-run with `-e CURSOR_RESEED=1` to force a refresh. The launch posture is `--force`
(Cursor's documented autonomous mode), qualified by three native controls that survive it:

| Layer | File | What it does |
| ----- | ---- | ------------ |
| Deny rules | `~/.cursor/cli-config.json` | Denies `sudo`/`su`, host power commands, `nc`/`netcat`, and credential reads (`.env*`, `.ssh`, AWS creds). Deny beats allow — even under `--force`. **Caveat:** if `.env` is bind-mounted or sourced by the entrypoint (`load_env`), the agent already holds those values in process memory and can read the file directly — deny rules are policy guidance, not isolation. See [Credential isolation](../../README.md#credential-isolation-by-egress-mode) and . |
| Enterprise hook | `/etc/cursor/hooks.json` (root-owned) | Audits every `beforeShellExecution` to `~/.cursor/audit-shell.ndjson` (override: `PROVEO_CURSOR_AUDIT_LOG`). Highest hooks precedence; the run-as uid cannot edit or out-rank it. Audit-only and fail-open — enforcement lives in deny rules + egress. |
| Readonly subagents | `~/.cursor/agents/*.md` | `adversarial-reviewer` and `security-reviewer` review gates with the native `readonly: true` bit. |

Cursor's own OS sandbox is disabled (`--sandbox disabled`): Docker is the sandbox, and
Landlock/seccomp inside a cap-dropped container is nondeterministic.

### Steering (rules)

The CLI natively reads `.cursor/rules/*.mdc`, root `AGENTS.md`, `CLAUDE.md`, and legacy
`.cursorrules` from the mounted repo. The entrypoint **detects and reports** these — it never
writes into your workspace on its own. To seed the baked verification-loop rule
(`proveo-loop.mdc`, `alwaysApply: true`) into `.cursor/rules/`, opt in:

```bash
./run.sh ... -e CURSOR_SEED_RULES=1 # via docker args, or export in .env
```

### Overriding the defaults

Precedence (highest wins): enterprise hooks (`/etc/cursor/hooks.json`) → project
`.cursor/cli.json` + `.cursor/hooks.json` + `.cursor/agents/*.md` → seeded `~/.cursor/*`.
Project deny rules extend (and can only tighten alongside) the seeded baseline; drop a
`.cursor/agents/<name>.md` in your repo to override or add a subagent. The CLI also reads
`.claude/agents/` and `.codex/agents/` for compatibility.

## Egress modes

`--egress-mode allowlist|review` reuses the shared sidecar lifecycle
(`defs/lib/egress.sh`). Cursor specifics:

- Provider pinning auto-detects `CURSOR_API_KEY` and pins inference writes to
 `.cursor.sh`/`.cursor.com` (agent traffic: `api5.cursor.sh`; API/auth: `api2.cursor.sh`).
 Web reads (docs/search) stay open, as for every harness.
- The entrypoint detects proxy env and sets `useHttp1ForAgent: true` in the seeded config —
 Cursor's HTTP/2 streaming does not survive every proxy chain. The CLI honors
 `NODE_EXTRA_CA_CERTS`, which the firewall mode points at the mitmproxy CA.
- If the network cannot reach the Cursor backend, there is no inference, full stop — this
 harness has no offline/local-model fallback.

## MCP servers

Declare MCP servers in project `.cursor/mcp.json` (stdio or remote):

```jsonc
{
 "mcpServers": {
 "filesystem": {
 "command": "npx",
 "args": ["-y", "@modelcontextprotocol/server-filesystem", "/app"]
 }
 }
}
```

Gate them with `Mcp(server:tool)` permission rules. See <https://cursor.com/docs/mcp>.

## Tests

```bash
./test.sh
```

The suite covers image availability/labels, tool presence, security hardening (no setuid, no
`nc`, immutable enterprise hook + dist prefix), entrypoint behavior (smoke mode, proxy
compat, preamble), default seeding (`CURSOR_RESEED`, workspace non-mutation,
`CURSOR_SEED_RULES` opt-in, audit hook round-trip), and — when `CURSOR_API_KEY` is set — a
live round-trip through the Cursor backend.

## Conventions

See [`CONVENTIONS.md`](../CONVENTIONS.md) at the repo root for project-wide agent
conventions. Cursor automatically picks up `AGENTS.md` / `CLAUDE.md` from the working
directory.
