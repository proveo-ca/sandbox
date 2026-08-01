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

Build the Docker images and execute `./run.sh` to run an isolated Claude Code variant with access to the current working directory mounted at `/workspace/input` (read-write by default).

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

- input workspace at `/workspace/input`
- output directory at `/workspace/output`
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

Run `claude setup-token`, login, save the resulting `sk-*` token.


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
