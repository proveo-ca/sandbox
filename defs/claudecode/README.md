# Claude Code Container

A Docker container for running Claude Code in "dangerously skip permissions" mode.

## Contract Status

Candidate coding harness definition. This definition exposes:

- variant `Dockerfile`s under `mcp/` and `solo/`
- root `build.sh`
- root `run.sh`
- root `test.sh`
- variant `entrypoint.sh` scripts under `mcp/` and `solo/`
- `README.md`
- sample Claude settings/config files under each variant
- `tests/`

Each variant owns its image-local `entrypoint.sh`. The root command surface remains `build.sh`, `run.sh`, and `test.sh`; root `run.sh` delegates to the variant runners.

https://github.com/user-attachments/assets/81c731d9-caeb-48cf-aa3e-65a48c55519e

Build the Docker images and execute `./run.sh` to run an isolated Claude Code variant with access to the current working directory mounted at `/app` (read-write by default).

```
/workspace/
├── input/              # Host input files (read-only mount of $PWD)
├── output/             # Analysis results (writable mount to host)
├── data/               # Reference data (optional read-only mount)
├── temp/               # Temporary files (tmpfs mount)
├── .claude/            # Claude Code project settings
│   └── settings.local.json
└── mcp-servers/        # MCP server installations
```


## Variants

### 1. solo
Basic Claude Code container without MCP servers configured. Clean, simple setup.

### 2. mcp
Claude Code container with MCP servers pre-configured. Shows how to add MCP servers, configure them, and auto-trust their execution.

## Quick Start

### Prerequisites

1. **Claude Code License**: Ensure you have a valid Claude Code license
2. **OAuth Token**: Set your Claude Code OAuth token
3. **Docker**: Docker must be installed and running

### Build and Run

Use the root scripts for the definition-local command surface:

```bash
# Build both variants
./build.sh

# Build one variant
./build.sh --variant solidity
./build.sh --variant mcp --tag local

# Run the default MCP variant
CLAUDE_CODE_OAUTH_TOKEN=sk-... ./run.sh

# Run the solo variant
CLAUDE_CODE_OAUTH_TOKEN=sk-... ./run.sh --variant solidity

# Pass additional Claude options through to the variant runner
CLAUDE_CODE_OAUTH_TOKEN=sk-... ./run.sh -- --debug --mcp-debug

# Resume a prior session (transcripts under ~/.proveo/.claude)
CLAUDE_CODE_OAUTH_TOKEN=sk-... ./run.sh -- --resume
proveo run claudecode --continue
proveo run claudecode --resume <session-id>
```

## Image Names, Mounts, and Commands

Default images:

- MCP variant: `proveo/claudecode:latest`
- Solidity variant: `proveo/claudecode-solidity:latest`

Image overrides:

```bash
./run.sh --variant mcp --image example/claudecode:tag
./run.sh --variant solidity --image example/claudecode-solidity:tag
```

Variant runners mount:

- workspace at `/app`
- output directory at `/app/output`
- optional data directory at `/workspace/data`
- temporary storage at `/workspace/temp`
- proveo home (`~/.proveo`) at `/proveo-home` with `HOME` set there — Claude sessions
  under `~/.claude/projects/` survive `--rm`; host `~/.claude` is never mounted

Run tests:

```bash
./test.sh
```

Open a variant debug shell through the parent run wrapper:

```bash
./run.sh --variant mcp --shell
./run.sh --variant solidity --shell
```

## Environment Variables

- `CLAUDE_CODE_OAUTH_TOKEN`: Your Claude Code OAuth token (required)
- `CLAUDECODE_RESEED=1`: overwrite existing `$HOME/.claude/agents/*.md` with the baked-in defaults

Run `claude setup-token`, login, save the resulting `sk-*` token.

## Baked-in Subagents

The image ships subagent definitions in `/opt/claudecode/defaults/agents/` and seeds them
into `$HOME/.claude/agents/` on startup (missing-only, so a durable proveo home keeps your
edits; `CLAUDECODE_RESEED=1` refreshes them). Claude Code also reads project-level
`.claude/agents/*.md` from the mounted workspace — drop a file there to override or add one.

| Subagent | Tools | Active when |
| --- | --- | --- |
| `architect` | read-only | Before implementing anything non-trivial — new module, new contract, more than a couple of files |
| `monorepo-coordinator` | read-only | The change spans more than one project, touches a shared package, or moves a project boundary |
| `adversarial-reviewer` | read-only | Always, on the final diff, before the loop declares the task done |
| `security-reviewer` | read-only | Auth, secrets, network, dependencies, sandbox posture, permissions, payments, user data, or serialization are touched |
| `spec-keeper` | `Edit`/`Write` | `_spec/`, `PLAN.md`, `CLAUDE.md`/`AGENTS.md`, architecture boundaries, or harness contracts changed |

The trigger table lives in the seeded `CLAUDE.md` loop rule (steps 3, 6, 7) — the
definitions alone are inert; the loop rule is what makes them run. `[BLOCKER]` and `[HIGH]`
findings from either reviewer are completion blockers.

**The read-only split is the point.** This harness launches with
`--dangerously-skip-permissions`, so a reviewer that could edit would be able to fix its own
findings and mark itself green. Every advisor is restricted by its frontmatter `tools:`
allowlist to `Read, Grep, Glob` — no `Edit`, no `Write`, and no `Bash` for anyone. The main
agent runs `git diff` and hands over the diff. `spec-keeper` is the single writer, and its
prompt scopes it to `_spec/`, `PLAN.md`, `CLAUDE.md`, and `AGENTS.md`.

## Security Features

### Container Security
- **Root-free execution**: baked non-root user `claude` (uid 1000); `run.sh` launches as the invoking host uid via `--user $(id -u):$(id -g)`
- **Capability dropping**: Minimal Linux capabilities
- **Process limits**: Host-scaled `--pids-limit` (base floor 512; browser variants higher; override via `PROVEO_PIDS_LIMIT`). Runs fail fast if the host ceiling is below the tier minimum.
- **Tmpfs mounts**: Isolated temporary storage for /tmp and /workspace/temp
- **Network isolation**: Bridge network with no host access
- **Security options**: No new privileges allowed

### Jailfree Mode
- **Dangerous executions allowed**: Pre-configured for full automation
- **Auto-trusted workspace**: No trust prompts during analysis
- **Comprehensive tool permissions**: Access to all tools via wildcard allowlist

## MCP Server Integration (`mcp` variant)

The `mcp` variant shows how to integrate Model Context Protocol servers:

### Adding Your Own MCP Server

1. **Copy MCP to build context**: `./mcp/<your-mcp>/`
2. **Update Dockerfile**: Add COPY and build steps
3. **Configure in claude-config.json**: Add MCP server definition
4. **Build and run**: Use the build script

Example MCP configuration:
```json
"mcpServers": {
   "your-mcp": {
      "type": "stdio",
      "command": "node",
      "args": ["/workspace/mcp-servers/your-mcp/build/index.js", "stdio"],
      "env": {},
      "trusted": true,
      "autoStart": true
   }
}
```

## Usage Examples

### Basic Claude Session
```bash
export CLAUDE_CODE_OAUTH_TOKEN="sk-your-token"
./run.sh
```

### With Debug Options
```bash
./run.sh -- --debug --mcp-debug
```

## Browser: agent-browser in the sandbox, Claude in Chrome on the host

Two different browsers, two different add-ons in the `proveo run` picker:

| add-on | what the agent drives | image | backend |
| --- | --- | --- | --- |
| `browser` | a headless Chromium **inside the sandbox** — Playwright's, shared by the `playwright` CLI and [vercel-labs/agent-browser](https://github.com/vercel-labs/agent-browser) (`open` · `snapshot` · `click` · `fill` · `screenshot` over CDP) | `proveo/claudecode-browser` (FROM `proveo/base-node-browser`) | sbx or docker |
| `chrome (host browser)` | **your own Chrome**, through the Claude in Chrome extension — your logins, the extension's site permissions | any claudecode image | docker only |

### `browser` — agent-browser beside Playwright

The `-browser` variant bakes agent-browser pointed at Playwright's Chromium
(`AGENT_BROWSER_EXECUTABLE_PATH`), with its bundled skills (`agent-browser skills get core`)
and a discovery stub that the seed copies to `~/.claude/skills/agent-browser/SKILL.md`. The
stub tells Claude Code to load the version-matched guide and never to run
`agent-browser install`. Opt out of the skill with `PROVEO_BROWSER_SKILL=off`. Details:
`defs/base-node-browser/README.md`, spec `_spec/defs/browser-layer.puml`.

### `chrome (host browser)` — the Claude in Chrome bridge

Claude Code's browser integration is two processes on one machine: its `claude-in-chrome`
MCP server, and a native messaging host that Chrome spawns for the extension. They meet on a
Unix socket the native host listens on (`/tmp/claude-mcp-browser-bridge-<user>/<pid>.sock`)
and the CLI connects to. Anthropic does not carry that across a container boundary
(anthropics/claude-code#25506, #21299), so proveo does:

1. `proveo run` starts a TCP relay on the host (`internal/chromebridge`, loopback on macOS,
   per-run token) and hands the agent `PROVEO_CHROME_BRIDGE=host.docker.internal:<port>` plus
   the token.
2. The entrypoint starts `chrome-bridge.js` in the container, listening on the exact socket
   path Claude Code looks for (same username rule, `0700`/`0600`), piping every connection to
   the host relay, which pipes on to the newest native host socket.
3. Claude Code is launched with `--chrome`. Nothing is persisted into your `~/.claude.json`
   except the one-time onboarding flag; a run without the add-on loads no browser tools.

Preconditions, each named in the picker when it fails:

- Chrome (or Edge/Brave…) is open on the host with the Claude in Chrome extension, and
  `claude --chrome` has run once on the host so the native host is registered.
- The run signs in with `/login` (a persisted login in `~/.proveo/.claude` works). Claude Code
  turns Chrome integration off for `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` sessions —
  their OAuth scope is inference-only and the extension cannot authenticate with them.
- `--egress-mode open --credentials forward` and the docker backend: the bridge network is
  the only one with a route to the host, and a sandbox VM cannot reach it (Docker Sandboxes
  proxies every outbound TCP). Ticking `docker (sandbox)` greys this add-on.

The agent then has your browser's sessions. The extension's site permissions still apply, and
the relay closes any connection that does not present the run's token. Spec:
`_spec/defs/claudecode/chrome-bridge.puml`.

## Code intelligence

The image bakes the language servers and **seeds the official Claude Code LSP plugins**
(`typescript-lsp`, `pyright-lsp`, `gopls-lsp`, `rust-analyzer-lsp`, `clangd-lsp`, `jdtls-lsp`,
`lua-lsp`) at `/opt/proveo/claude-plugins`. Claude Code otherwise offers to install the plugin for every
server binary it finds on PATH, on every fresh sandbox home. At seed time proveo registers the
seeded plugins in the agent home (install and marketplace records pointing at the seed),
enables each plugin whose binary is present after provisioning, and `proveo-lsp` (the skills-directory
plugin proveo writes) declares only the languages no official plugin covers, so no extension
gets two servers. Opt out with `PROVEO_CLAUDE_LSP_PLUGINS=off`, or disable one plugin with
`/plugin disable <name>@claude-plugins-official`; that choice is kept across runs.

## Troubleshooting

### OAuth Token Issues
Verify your OAuth token is set correctly:
```bash
export CLAUDE_CODE_OAUTH_TOKEN="sk-your-token-here"
./run.sh
```

### Debug Container Access
```bash
./run.sh --variant mcp --shell   # Access the MCP variant debug shell
./run.sh --variant solidity --shell  # Access the solo variant debug shell
```

## License

This project is provided under the terms consistent with Claude Code's licensing requirements.
