# opencode Docker Runner

Custom Docker image for OpenCode v2 ([`@opencode/cli`](https://opencode.ai/v2/docs)) with:

- `proveo/base` (MCR `playwright` noble floor: Node, Chromium + OS deps, `pnpm`)
- Root-free runtime: baked non-root user `opencode` (uid 1000); sbx runs the agent as that uid
- Monorepo-friendly entrypoint (`pnpm install` on first run if needed)
- `.env` autoloading outside sbx and auto-detection of common provider API keys

## Browser variant

`mise run build opencode-browser` builds `proveo/opencode-browser` FROM `proveo/base-node-browser`: a
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
- `README.md`

Its image suite is `internal/imagetest/opencode_test.go` (`TestImageOpencode`).

For a shell in a running OpenCode sandbox: `sbx ls`, then `sbx exec -it <sandbox> bash`.

This definition follows the shared [coding harness container contract](../../CODING_HARNESSES.md), including runtime config discovery, `.env` bridging, and monorepo mount expectations.

## Image Names and Mounts

- Default image: `proveo/opencode:latest`
- Build override: `PROVEO_OPENCODE_IMAGE=example/opencode mise run build opencode --tag tag`
- Run override: `proveo run opencode --image example/opencode:tag`
- Workspace mount: input directory mounted at `/app`

## Build

```bash
mise run build opencode
```

The build resolves `@opencode/cli`'s current npm release and installs that exact version.
`opencode --version` must report `opencode v<version>` before the build succeeds.
The image records `proveo.agent=@opencode/cli` and `proveo.agent.version=<version>`.

To pin a specific v2 release:

```bash
OPENCODE_VERSION=2.0.6 mise run build opencode
```

To build a specific tag:

```bash
mise run build opencode --tag local
```

## Run
Run it with `proveo run`:

```bash
proveo run opencode --input "$PWD"
```

### From a repo root

```bash
proveo run opencode --input "$PWD"
```

### With a specific image

```bash
proveo run opencode --image proveo/opencode:local --input "$PWD"
```

### Non-interactive (single prompt)

```bash
ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY" \
  proveo run opencode -- run -m anthropic/claude-sonnet-4-5 "List the files in /app"
```

Any args passed after `--` are forwarded to `opencode`.

## Provider API Keys

proveo detects these keys on the host. With `--credentials broker` (the default) sbx's proxy
holds the key; with `--credentials forward` it enters the sandbox env. Inside sbx the entrypoint
never sources the workspace `.env`. The entrypoint warns when no provider key and no
`opencode.json` are detected. Recognised env vars:

| Provider     | Env var                |
| ------------ | ---------------------- |
| OpenCode Zen / Go | `OPENCODE_API_KEY` |
| Anthropic    | `ANTHROPIC_API_KEY`    |
| OpenAI       | `OPENAI_API_KEY`       |
| OpenRouter   | `OPENROUTER_API_KEY`   |
| xAI          | `XAI_API_KEY`          |
| Google       | `GEMINI_API_KEY` / `GOOGLE_API_KEY` |
| DeepSeek     | `DEEPSEEK_API_KEY`     |
| Groq         | `GROQ_API_KEY`         |
| Mistral      | `MISTRAL_API_KEY`      |

### OpenCode Zen and OpenCode Go

OpenCode's own gateway — Zen (pay-as-you-go, model ids `opencode/<model>`) and Go (the
subscription, `opencode-go/<model>`) — takes the same key, `OPENCODE_API_KEY`. Copy it from
<https://opencode.ai/auth> and export it on the host **before launch**, in your shell rc or a
gitignored `.env`; the running server can use the environment key without an in-sandbox `/connect` step.
proveo detects the key like any other provider: with `--credentials broker` sbx's proxy holds
it and attaches it on `opencode.ai` and `*.opencode.ai` only.

OpenCode v2 stores saved credentials in its SQLite database.
proveo runs the CLI with private data and publishes a credential-free snapshot of session history after it exits.
The runtime holds a lease on the durable data store and rejects a concurrent stateful run with exit code `75`.
Startup checks a runtime-owned prepared generation instead of copying and sanitizing full history.
The cache stays on the owning sbx engine's local filesystem.
Successful OpenCode runs retain that engine for subsequent launches.
Post-exit still performs the existing three sanitized checkpoint/publication operations.
Environment API keys remain available to the running CLI.
Saved logins from `opencode auth login` or `/connect` do not carry into the next run.
The OpenCode-only host-owned OAuth persistence proposal lives in
[`_spec/_paradigms/opencode-oauth-persistence.puml`](../../_spec/_paradigms/opencode-oauth-persistence.puml).
That policy remains planned; the current runtime still removes saved tokens.
The same holds for providers with no
dedicated env var (Together, Hugging Face, …): prefer an API key via env or sbx's credential proxy.
Resume a prior session with:

```bash
proveo run opencode --resume <session-id>
```

## Seed progress

OpenCode images enable `PROVEO_SEED_PROGRESS=1`.
While startup waits for the seed, stderr displays `Waiting for seed`, the current
run category and operation, elapsed seconds, and an animated indicator on terminals.
Categories include `credentials`, `workspace`, `setup`, `interface`, and `starting`.
Credential steps identify prepared-state checks without exposing tokens.
The seed writes the same operation labels and credential-phase timings to
`/var/log/sbx-kit-startup.log` on sbx.
Piped output reports step changes and five-second heartbeats without terminal control codes.
The display does not delay instruction release or change credential persistence.
`PROVEO_SEED_PROGRESS=0` disables the progress display.

## Versioned history and preparation

OpenCode V2 uses `opencode/v2/config` and `opencode/v2/share` under the proveo home.
Ordinary launches do not mount or import V1/unversioned history.
Historical V1 session versions inside an already-adopted V2 database remain valid.
The image requires a stable `2.x` native version.
An ordinary launch prepares an existing V2 database when the engine cache is unprepared, and reports the three prepare steps.
If that database is absent and an older `opencode.db` is on disk, the launch copies it into `opencode/v2/share` and prepares the copy. The older file stays in place. OpenCode's own V1 migration then runs inside the copy.
A removed engine prepares that database again when its private cache is gone and the journal's producer is the same stable major. A patch bump does not block it. A journal from another major stops the launch with the runtime's text; run the image of that major, or move the store aside.
sbx stops an idle engine about 30s after its last session, which ends the keeper. The next launch re-verifies the cache that engine kept: at once when its last run ended prepared or published, after asking when it was interrupted (a headless run recovers). It never deletes without a headed yes.

Prepared startup has a five-second preparation budget.
The seed gate does not run database backup, VACUUM, SQL dump hashing, or legacy migration.
The native client reuses the prepared engine-local cache.
The host keeps the keeper's kernel PID/start-time identity outside guest-writable mounts.
Readiness files inside the sandbox cannot replace that authority.

proveo adds no maintenance commands; arguments after `--` go to OpenCode.
To bring an existing **V2-schema** store, copy its `opencode.db` (with any `opencode.db-wal` and `opencode.db-shm` beside it) into `<proveo home>/opencode/v2/share/` before the first launch.
That launch prepares the mounted V2 database and publishes sanitized history into the V2 namespace.

An interrupted run leaves pending private state.
The next launch recovers it, or asks first: recover, reset with `sbx rm --force`, or stop and ask again next time.
Recovery does not overwrite a published store changed by another writer.
Use `sbx rm --force <engine-name>` only after preserving any unpublished work.

An OpenCode launch that no proveo host prepared (a raw `sbx run` of the kit, or `opencode` typed in an `sbx exec` shell) runs the native client as a raw `sbx run` would.
Its history stays in that sandbox; the proveo V2 store is not used.
Creating a new engine for an existing V2 database prepares that database on the next launch.

## Baked-in workflow and presentation defaults

The image launches OpenCode in native automatic-permission mode and ships its workflow defaults at `/opt/opencode/defaults/`.
The shared seed copies missing configuration into `~/.config/opencode/` on first run.
Re-run with `-e OPENCODE_RESEED=1`
to force a refresh from the baked-in copy.

### Automatic permissions and visible execution

The image sets `OPENCODE_CLI_CONFIG_CONTENT` with these session preferences:

```json
{
  "session": {
    "permissions": "autoaccept",
    "markdown": "source",
    "thinking": "show",
    "grouping": "none",
    "verbosity": "high",
    "sidebar": "auto"
  },
  "tabs": { "mode": "on", "indicators": "status" },
  "mini": {
    "tools": "show",
    "thinking": "show",
    "shell_output": "show",
    "turn_summary": "show",
    "footer": "show"
  }
}
```

Inline preferences override the saved global `cli.json` while the environment variable is set.
Headless `run` also receives the native `--auto` flag.
Automatic approval accepts permission requests that no explicit deny rule blocks.
Source mode disables Markdown concealment.
Thinking mode expands available reasoning blocks.
High verbosity displays individual tools and thoughts.

Click a running subagent row to open its live transcript.
Alternatively, use **Ctrl+P → Toggle subagent picker** and press **Enter** on a child.
Status-marked tabs show busy and attention states for opened sessions.
V2 has no setting that renders a live child-operation wait graph in the main transcript.
Code Mode's `execute` row opens its separate **Code / Output** dialog.

### Default `opencode.json`

Two primary agents, mirroring the plan→build loop:

| Agent   | `edit` | `shell` | Use it for                              |
| ------- | ------ | ------- | --------------------------------------- |
| `plan`  | `deny` | `deny`  | Spec'ing, drafting a step list to review |
| `build` | `allow`| `ask`   | Implementation with automatic approval  |

V2 expresses permissions as ordered `action`, `resource`, and `effect` rules under `agents.<name>.permissions`.
The image's automatic session policy satisfies `ask` requests.
The defaults enable `compaction.auto` and set `update: "disable"`.
The image also sets `OPENCODE_DISABLE_AUTOUPDATE=1`.
The defaults leave model selection to OpenCode.

### Default subagents (`@`-mentionable)

All read-only (`edit:deny`, `shell:deny`) — they advise, you decide whether to act.
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
3. Switch to the `build` agent. The automatic session policy handles permission requests.
   Commit incrementally on an `agent/<task>` branch — never `main`.
4. After each chunk: `@adversarial-reviewer` on the diff. Treat its `[BLOCKER]` and
   `[HIGH]` items as merge gates.
5. Cross-review with a different model family for a second opinion, e.g.:
   `git diff main | opencode run -m openai/gpt-... "Adversarial review of this diff"`.

### Overriding the defaults

OpenCode merges global configuration, direct project configs from ancestors to the working directory, and then `.opencode` configs in the same order.
Within one config directory, `opencode.jsonc` takes precedence over `opencode.json`.
Drop a `.opencode/agents/<name>.md` in your repo to override or add a subagent.
proveo preserves existing global JSON, JSONC, and rendered agents unless `OPENCODE_RESEED=1` is set.
Automatic LSP and formatter wiring leaves JSONC and malformed JSON untouched.
An explicit local-model request fails if those files prevent safe wiring.
Quit and restart OpenCode after changing its configuration.

## Project configuration

opencode reads `opencode.json` (or `opencode.jsonc`) from the working directory.
A minimal example:

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5",
  "agents": {
    "title": { "model": "anthropic/claude-haiku-4-5" }
  },
  "providers": {
    "anthropic": {
      "settings": { "apiKey": "{env:ANTHROPIC_API_KEY}" }
    }
  }
}
```

See <https://opencode.ai/v2/docs/config> for the v2 configuration contract.
The published editor schema still describes v1 fields; the image suite validates native settings against the installed v2 CLI.

## MCP servers

Declare MCP servers under `mcp.servers` in `opencode.json`:

```jsonc
{
  "mcp": {
    "servers": {
      "filesystem": {
        "type": "local",
        "command": ["npx", "-y", "@modelcontextprotocol/server-filesystem", "/app"]
      }
    }
  }
}
```

See <https://opencode.ai/v2/docs/mcp-servers> for transport options (`local` / `remote`)
and trust settings.
Set a server's `disabled` field to `true` to keep it configured without connecting.

## Idle git synchronization

The native v2 plugin observes session idle events and schedules best-effort git synchronization.
The CLI can return before synchronization finishes.
Commit-subject helper runs use a private `--standalone` server.

## Tests

```bash
mise run test-defs opencode      # or: proveo test opencode
```

The suite (`internal/imagetest/opencode_test.go`) covers build, tool presence, security hardening, baked-in default seeding
(including `OPENCODE_RESEED=1` behaviour), MCP config loading, and — when
`ANTHROPIC_API_KEY` (or another provider key) is set — a live LLM round-trip via
`opencode run`.

## Conventions

See [`CONVENTIONS.md`](../CONVENTIONS.md) at the repo root for project-wide agent
conventions. opencode automatically picks up `AGENTS.md` from the working directory.
