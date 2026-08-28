# Codex Container

A Docker container for running the OpenAI Codex CLI as an unattended coding loop.

## Contract Status

Candidate coding harness definition. This definition exposes:

- `Dockerfile`
- `entrypoint.sh`
- `build.sh`
- `run.sh`
- `test.sh`
- `README.md`
- baked defaults under `defaults/`
- `tests/`

Same paradigm as `claudecode`: the ML blackbox loop (spec → plan → verify), full
autonomy inside the container, and the container as the boundary. What differs is
everything the CLI itself decides — see [Differences from claudecode](#differences-from-claudecode).

```
/app/                   # the workspace (rw bind of the repo)
├── output/             # deliverables, mounted back to the host
└── .codex/             # PROJECT-scoped codex config (the repo's, never written here)

$CODEX_HOME (~/.codex)  # USER-scoped, durable under ~/.proveo/.codex
├── config.toml         # seeded from the baked defaults, missing-only
├── AGENTS.md           # proveo house rules (user layer)
├── agents/*.toml       # composed subagents
├── auth.json           # `codex login`, persisted between runs
└── sessions/           # rollout transcripts — what `codex resume` reads
```

## Quick Start

```bash
# Build
./build.sh --tag local
./build.sh --tag local --browser                 # + Playwright/Chromium
./build.sh --tag local --codex-version 0.101.0   # pin the CLI

# Run
OPENAI_API_KEY=sk-... ./run.sh

# Or authenticate once with a ChatGPT plan; the login persists under proveo home
./run.sh -- login

# Pass options through to codex
./run.sh -- exec "summarise the failing tests"

# Resume
proveo run codex --continue          # → codex resume --last
proveo run codex --resume <uuid>     # → codex resume <uuid>
proveo run codex --ls                # → codex resume (the picker)
```

Run the definition-local suite with `./test.sh` (`IMAGE=proveo/codex:local ./test.sh`
to point it at a local build).

### Variants

| Image | Base | Adds |
| --- | --- | --- |
| `proveo/codex` | `proveo/base-node-lsp` | node + the workspace language servers |
| `proveo/codex-browser` | `proveo/base-node-browser` | Playwright + Chromium |

`base-node-browser` is itself FROM `base-node-lsp`, so the browser variant is a
strict superset — it keeps the language servers rather than trading them for a
browser. It is not a target of its own: pick `browser` in the run prompt's addon
row (or `--addon browser`) and the run swaps the image, leaving the target `codex`.

## Environment Variables

| Variable | Effect |
| --- | --- |
| `OPENAI_API_KEY` | API-key auth. Optional when a `codex login` is persisted. |
| `CODEX_MODEL` | Model id passed as `--model`. Bridged from `ARCHITECT_MODEL` / `EDITOR_MODEL` — see `defs/bridges/codex.tsv`. |
| `CODEX_RESEED=1` | Overwrite `$CODEX_HOME/config.toml` and every composed subagent with the baked defaults. |
| `PROVEO_CODEX_SANDBOX` | Opt back into codex's own sandbox: launches `--sandbox <value> --ask-for-approval never` instead of the bypass flag. |
| `PROVEO_CODEX_IMAGE` / `PROVEO_CODEX_BROWSER_IMAGE` | Override the image reference a build writes. |
| `PROVEO_AGENT_EVIDENCE=default` | Drop the extra narration (`codex exec --json`). |
| `PROVEO_HOUSE_RULES=off` | Do not install proveo's conventions at `$CODEX_HOME/AGENTS.md`. |

## Authentication

Two ways to be authenticated, and they do not merge:

1. **`codex login`** — a ChatGPT plan. The credential lands in
   `$CODEX_HOME/auth.json`, which is inside the durable proveo home, so one login
   serves every later run. Codex prefers this when both are present, and proveo
   suppresses the competing API key host-side for the same reason.
2. **`OPENAI_API_KEY`** — per-token billing, and the only headless option on a
   fresh home.

`PersistedLogin` reports the login file by PRESENCE only: its OAuth stamps live
under a different key than the one `loginUsable` parses, and inferring "expired"
from a shape proveo does not understand would refuse runs that work. An expired
codex login therefore reaches the CLI's own re-auth rather than proveo's guard.

## The Loop

The entrypoint is thin and does the same five things every proveo harness does:
resolve the workspace, load `.env`, bridge the model role vars, seed the user-level
config, and report what it found before handing over.

Two reports are worth reading in a transcript:

- **Verification Commands** — the build/test/lint commands detected in the
  workspace. The loop's verify step is not invented per repository.
- **Steering Files** — which instruction file the run is actually under.

## Subagents

The image ships one shared body per agent in `/opt/proveo/subagents/` and composes
them into `$CODEX_HOME/agents/*.toml` on startup (missing-only; `CODEX_RESEED=1`
refreshes). Codex also reads `.codex/agents/*.toml` from the workspace, so a repo
can override or add one.

| Subagent | `sandbox_mode` | Active when |
| --- | --- | --- |
| `architect` | `read-only` | Before implementing anything non-trivial |
| `monorepo-coordinator` | `read-only` | The change spans projects or touches a shared package |
| `adversarial-reviewer` | `read-only` | Always, on the final diff, before the loop declares the task done |
| `security-reviewer` | `read-only` | Auth, secrets, network, dependencies, sandbox posture, permissions, payments, user data, or serialization are touched |
| `spec-keeper` | `workspace-write` | `_spec/`, `PLAN.md` or `AGENTS.md` changed |

**The read-only split is the point.** This harness launches with approvals off, so
an advisor that could edit would be able to fix its own findings and mark itself
green. Codex has no per-tool allowlist, so `sandbox_mode = "read-only"` is the
structural control — the same guarantee claudecode gets from `tools:`, expressed in
the vocabulary codex actually enforces. `spec-keeper` is the single writer.

## Security Posture

- **Root-free execution**: baked non-root user `codex` (uid 1000); the runner
  launches as the invoking host uid.
- **Capability dropping / no-new-privileges / pids-limit**: from `internal/runner`,
  not restated here.
- **Codex's own sandbox is off by default**, and that is a decision rather than an
  oversight. Unlike cursor's Landlock (which needs an unprivileged userns a
  cap-dropped container cannot give it), codex's Landlock+seccomp layer *can*
  engage here. It is off because confining an agent to part of a tree that is
  already a disposable copy buys little, while its approval prompts are fatal to an
  unattended run — a stall with nobody to answer. `PROVEO_CODEX_SANDBOX` turns it
  back on.
- **Egress** is the proveo tier in front of the container, not a CLI setting.

## Differences from claudecode

| | claudecode | codex |
| --- | --- | --- |
| House rules | `/etc/claude-code/CLAUDE.md` (managed policy, cannot be excluded) | `$CODEX_HOME/AGENTS.md` (user layer — the project's file outranks it) |
| Project steering | `CLAUDE.md`, bridged from `AGENTS.md` into the workspace | `AGENTS.md`, read natively — nothing is written into the checkout |
| Subagent format | markdown + YAML frontmatter, `tools:` allowlist | TOML document, `sandbox_mode` |
| LSP | native Claude Code plugin | MCP servers (`mcp-language-server`) in `config.toml` |
| Autonomy flag | `--dangerously-skip-permissions` | `--dangerously-bypass-approvals-and-sandbox` |
| Resume | `--continue` / `--resume <id>` | `resume --last` / `resume <id>` (a subcommand) |

## Troubleshooting

**"Cannot connect to the Docker daemon"** — sbx starts a per-sandbox daemon only
for an image carrying `com.docker.sandboxes.start-docker`. The label is set here;
if the daemon still does not come up it is the sbx-side privilege question recorded
in `defs/claudecode/mcp/Dockerfile`.

**The MCP LSP block broke my `config.toml`** — the generated block is appended
between `# >>> proveo lsp` markers and holds `[mcp_servers.*]` tables. TOML requires
top-level keys to precede every table, so add yours ABOVE the marker, not after it.

**Sessions do not survive `--rm`** — check that `CODEX_HOME` is unset in the
container. It must follow `$HOME`, which proveo points at the mounted home.
