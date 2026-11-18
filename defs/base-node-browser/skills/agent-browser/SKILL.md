---
name: agent-browser
description: Browse with the preinstalled agent-browser CLI. Use for browser interaction, screenshots, data extraction, web-app testing, and exploratory QA.
allowed-tools: Bash(agent-browser:*)
---

# agent-browser (proveo sandbox)

Fast browser automation CLI for AI agents: Chrome/Chromium over CDP, accessibility-tree
snapshots with compact `@eN` element refs.

## This sandbox is already provisioned

- Use `agent-browser` directly. When `PROVEO_HOST_CDP_PORT` is set, the launcher connects
  to the operator's dedicated Chrome window over CDP. Use the configured connection.
- The isolated browser choice attaches to Chromium inside the sandbox. The operator can take control temporarily. Retry browser commands after control returns.
- Use the preinstalled binary and browser. Skip installation commands.
- Bundled skills are served from `$AGENT_BROWSER_SKILLS_DIR`, version-matched to the binary.

## Start here

This file is a discovery stub, not the usage guide. Before the first `agent-browser`
command, load the workflow content from the CLI itself:

```bash
agent-browser skills get core             # workflows, snapshot/ref loop, troubleshooting
agent-browser skills get core --full      # plus the full command reference
```

## Always use your own session

The default session is a single shared browser. Name one for the task before the first
command, so parallel agents never hijack each other's page:

```bash
export AGENT_BROWSER_SESSION="$(agent-browser session id --scope worktree --prefix task)"
```

## Specialized skills

```bash
agent-browser skills list                 # everything available on this version
agent-browser skills get dogfood          # exploratory testing / QA / bug hunts
agent-browser skills get derive-client    # record a HAR, derive a standalone API client
```
