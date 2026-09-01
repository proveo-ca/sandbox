# opencode Docker Runner

Custom Docker image for [`opencode-ai`](https://github.com/anomalyco/opencode) with:

- `proveo/base` (MCR `playwright` noble floor: Node, Chromium + OS deps, `pnpm`)
- Root-free runtime: baked non-root user `opencode` (uid 1000); the run wrapper launches as the invoking host uid via `--user $(id -u):$(id -g)`
- Monorepo-friendly entrypoint (`pnpm install` on first run if needed)
- `.env` autoloading and auto-detection of common provider API keys

## Browser variant

`./build.sh --browser` builds `proveo/opencode-browser` FROM `proveo/base-node-browser`: a
headless Chromium shared by the `playwright` CLI and
[vercel-labs/agent-browser](https://github.com/vercel-labs/agent-browser) (`open` ·
`snapshot` · `click` · `fill` · `screenshot`, accessibility-tree refs over CDP). The seed drops
agent-browser's discovery stub into `~/.config/opencode/skills/agent-browser/SKILL.md`, which
points the agent at `agent-browser skills get core` (the guide matching the installed binary)
and tells it not to run `agent-browser install`. Pick the `browser` add-on in the `proveo run`
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
- `tests/`

`debug.sh` is not present yet; it is optional unless this definition needs a dedicated troubleshooting workflow.

This definition follows the shared [coding harness container contract](../../CODING_HARNESSES.md), including runtime config discovery, `.env` bridging, and monorepo mount expectations.

## Image Names and Mounts

- Default image: `proveo/opencode:latest`
- Build override: `PROVEO_OPENCODE_IMAGE=example/opencode:tag ./build.sh`
- Run override: `./run.sh --image example/opencode:tag`
- Workspace mount: input directory mounted at `/app`

## Build

```bash
./build.sh
```

To build a specific tag:

```bash
./build.sh --tag local
```

## Run
Use `run.sh` for the definition-local command surface:

```bash
./run.sh --input-dir "$PWD"
```

### From a repo root

```bash
./run.sh --input-dir "$PWD"
```

### With a specific image

```bash
./run.sh --image proveo/opencode:local --input-dir "$PWD"
```

### Non-interactive (single prompt)

```bash
ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY" \
  ./run.sh -- run -m anthropic/claude-sonnet-4-5 "List the files in /app"
```

Any args passed after `--` are forwarded to `opencode`.

## Provider API Keys

If a `.env` file exists in the working directory it is auto-sourced by the entrypoint,
so you usually don't need `--env-file` or `-e` flags on `docker run`. The entrypoint
warns when no provider key and no `opencode.json` are detected. Recognised env vars:

| Provider     | Env var                |
| ------------ | ---------------------- |
| Anthropic    | `ANTHROPIC_API_KEY`    |
| OpenAI       | `OPENAI_API_KEY`       |
| OpenRouter   | `OPENROUTER_API_KEY`   |
| xAI          | `XAI_API_KEY`          |
| Google       | `GEMINI_API_KEY` / `GOOGLE_API_KEY` |
| DeepSeek     | `DEEPSEEK_API_KEY`     |
| Groq         | `GROQ_API_KEY`         |
| Mistral      | `MISTRAL_API_KEY`      |

For providers without a dedicated env var (Together, Hugging Face, OpenCode Zen, …) run
`opencode auth login` once — credentials are stored at
`~/.local/share/opencode/auth.json` inside the container. Proveo mounts durable config/share
under `~/.proveo/opencode/` but **scrubs `auth.json` each run** so login tokens are not
cached; prefer provider API keys via env / egress broker. Resume a prior session with:

```bash
proveo run opencode --resume <session-id>
```

## Baked-in HITL defaults

The image ships an opinionated, non-YOLO setup at `/opt/opencode/defaults/` and
seeds it into `~/.config/opencode/` on first run. Re-run with `-e OPENCODE_RESEED=1`
to force a refresh from the baked-in copy.

### Default `opencode.json`

Two primary agents, mirroring the plan→build loop:

| Agent   | `edit` | `bash`  | temp | Use it for                              |
| ------- | ------ | ------- | ---- | --------------------------------------- |
| `plan`  | `deny` | `deny`  | 0.1  | Spec'ing, drafting a step list to review |
| `build` | `allow`| `ask`   | 0.2  | Implementation — every shell call is a checkpoint |

Plus `context.rot: true` and `context.summarize: true` to keep long sessions sane.

### Default subagents (`@`-mentionable)

All read-only (`edit:deny`, `bash:deny`) — they advise, you decide whether to act.
`@spec-keeper` is the single exception: it has `edit:allow` *scoped by its prompt*
to `_spec/`, `PLAN.md`, and `AGENTS.md` only.

| Subagent                | Role                                                    |
| ----------------------- | ------------------------------------------------------- |
| `@adversarial-reviewer` | Ruthless senior-eng review of the diff. Finds, never fixes. |
| `@security-reviewer`    | OWASP-style threat review with CWE-tagged findings.     |
| `@architect`            | Layered design + file plan **before** code is written.  |
| `@systems-design`       | Capacity, failure modes, consistency, observability.    |
| `@frontend`             | React/Next/Vite/TS specialist; accessibility + bundle. |
| `@backend`              | APIs, schemas, transactions, queues, validation.        |
| `@sre`                  | SLOs, error budgets, rollout/rollback, runbooks.        |
| `@devops`               | Dockerfiles, CI, IaC, reproducibility, supply chain.    |
| `@monorepo-coordinator` | Cross-project boundaries, build graph, shared deps.     |
| `@spec-keeper`          | Owns `_spec/*.puml`, `PLAN.md`, `AGENTS.md`. Only role with scoped edit rights outside source code. |

### Suggested loop

1. Switch to the `plan` agent. Ask `@architect` for a design and a file plan; commit
   the plan as `PLAN.md` so it shows up in `git log`.
2. Hand the plan to `@adversarial-reviewer` and `@security-reviewer` (or
   `@systems-design`, `@monorepo-coordinator`) before any code is written.
3. Switch to the `build` agent. It edits freely but **every** `bash` invocation asks
   you first. Commit incrementally on a `agent/<task>` branch — never `main`.
4. After each chunk: `@adversarial-reviewer` on the diff. Treat its `[BLOCKER]` and
   `[HIGH]` items as merge gates.
5. Cross-review with a different model family for a second opinion, e.g.:
   `git diff main | opencode run -m openai/gpt-... "Adversarial review of this diff"`.

### Overriding the defaults

Precedence (highest wins): project `opencode.json` → project `.opencode/agents/*.md` →
seeded `~/.config/opencode/opencode.json` → seeded `~/.config/opencode/agents/*.md`.
Drop a `.opencode/agents/<name>.md` in your repo to override or add a subagent.

## Project configuration

opencode reads `opencode.json` (or `opencode.jsonc`) from the working directory.
A minimal example:

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5",
  "small_model": "anthropic/claude-haiku-4-5",
  "provider": {
    "anthropic": {
      "options": { "apiKey": "{env:ANTHROPIC_API_KEY}" }
    }
  }
}
```

See <https://opencode.ai/docs/config/> for the full schema.

## MCP servers

Declare MCP servers under `mcp` in `opencode.json`:

```jsonc
{
  "mcp": {
    "filesystem": {
      "type": "local",
      "command": ["npx", "-y", "@modelcontextprotocol/server-filesystem", "/app"],
      "enabled": true
    }
  }
}
```

See <https://opencode.ai/docs/mcp-servers/> for transport options (`local` / `remote`)
and trust settings.

## Tests

```bash
./test.sh
```

The suite covers build, tool presence, security hardening, baked-in default seeding
(including `OPENCODE_RESEED=1` behaviour), MCP config loading, and — when
`ANTHROPIC_API_KEY` (or another provider key) is set — a live LLM round-trip via
`opencode run`.

## Conventions

See [`CONVENTIONS.md`](../CONVENTIONS.md) at the repo root for project-wide agent
conventions. opencode automatically picks up `AGENTS.md` from the working directory.
