# cecli Definition

Candidate coding harness definition for running Cecli in Docker.

## Contract Status

This definition exposes the required candidate harness commands:

- `Dockerfile`
- `entrypoint.sh`
- `build.sh`
- `run.sh`
- `test.sh`
- `sample.cecli.conf.yml`
- `defaults/agents/` baked-in subagent prompts

`debug.sh` and `tests/` are not present yet. They are optional unless this definition graduates to require deeper troubleshooting or regression coverage.

## Image Name and Tag

`build.sh` builds a single image — the aider fork (`cecli-dev`) in a Python venv
on `proveo/base` — defaulting to `proveo/cecli:latest`. Override with `--tag`,
`--image`, or `PROVEO_CECLI_IMAGE`:

```bash
./build.sh
./build.sh --tag v2 --no-cache
PROVEO_CECLI_IMAGE=example/cecli:latest ./build.sh
```

`run.sh` runs `proveo/cecli:latest`. Override it with `--image` or `CECLI_IMAGE`.
(The old `cecli-node` / `cecli:python` dual-image split and its MCR/Playwright
lineage were deduped into this one browserless image.)

## Mounts

`run.sh` mounts:

- input workspace at `/app`
- output directory at `/app/output`

Defaults:

- input: current directory
- output: `./reports`

Use `--read-only` to mount the input workspace read-only and place Cecli state under `/tmp/.cecli` by default.

Proveo also mounts durable home config from `~/.proveo/.cecli` at `/proveo-home/.cecli`
(with `HOME=/proveo-home`) so `$HOME/.cecli.conf.yml` survives container exit. Project
agents stay under `CECLI_HOME=/app/.cecli` in the workspace.

## Environment Variables

- `PROVEO_CECLI_IMAGE`: image name used by `build.sh`; defaults to `proveo/cecli:latest`
- `CECLI_IMAGE`: image used by `run.sh`; defaults to `proveo/cecli:latest`
- `CECLI_INPUT_DIR`: input workspace override
- `CECLI_OUTPUT_DIR`: output directory override
- `CECLI_INSTALL_NODE_DEPS=1`: install Node dependencies when `package.json` is present
- `CECLI_HOME`: Cecli state directory inside the container
- `CECLI_AGENT_CONFIG`: override the generated Agent Mode JSON configuration
- `CECLI_RESEED=1`: overwrite existing `$CECLI_HOME/agents/*.md` with baked-in defaults

## Baked-in Subagents

Subagent bodies are shared across harnesses and baked at `/opt/proveo/subagents/` (one `.md`
per agent plus cecli's frontmatter under `_frontmatter/cecli/`); the entrypoint composes the
cecli roster from `_roster.json` into `$CECLI_HOME/agents/` on startup
(`proveo_seed cecli` → `render_subagents`, see `_spec/defs/agent-definition-sharing.puml`).
The entrypoint also sets a default `CECLI_AGENT_CONFIG` when one is not already provided:

```json
{"large_file_token_threshold":8192,"skip_cli_confirmations":false,"subagent_paths":["$CECLI_HOME/agents","/app/.cecli/agents"]}
```

This makes the agents available to Cecli Agent Mode through the `Delegate` tool. Project-specific agents can be added under `.cecli/agents/*.md`; they are included by the generated `subagent_paths` without modifying the image. Set `CECLI_RESEED=1` to refresh `$CECLI_HOME/agents/` from the baked-in copy.

Included defaults mirror the opencode reviewer set: `adversarial-reviewer`, `security-reviewer`, `architect`, `systems-design`, `frontend`, `backend`, `sre`, `devops`, `monorepo-coordinator`, and `spec-keeper`.

## Commands

Build images:

```bash
./build.sh
```

Run against the current directory:

```bash
./run.sh
```

Run with explicit mounts:

```bash
./run.sh --input-dir /path/to/repo --output-dir /path/to/reports
```

Run smoke tests against the latest image:

```bash
./test.sh
```

Override the image under test:

```bash
PROVEO_CECLI_IMAGE=proveo/cecli:latest ./test.sh
```
