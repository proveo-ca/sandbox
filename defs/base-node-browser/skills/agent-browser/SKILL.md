---
name: agent-browser
description: Browser automation CLI for AI agents, preinstalled in this sandbox with a headless Chromium. Use when the task needs a web page — navigating, filling forms, clicking, taking screenshots, extracting data, testing a local web app, logging into a site, exploratory QA — or any programmatic browser interaction. Prefer agent-browser over ad-hoc Playwright scripts for interactive browsing; keep Playwright for the project's own test suites.
allowed-tools: Bash(agent-browser:*)
---

# agent-browser (proveo sandbox)

Fast browser automation CLI for AI agents: Chrome/Chromium over CDP, accessibility-tree
snapshots with compact `@eN` element refs.

## This sandbox is already provisioned

- `agent-browser` is on PATH and Chromium is preinstalled at `$AGENT_BROWSER_EXECUTABLE_PATH`
  (Playwright's build, shared with the `playwright` CLI). **Do not run `npm i -g agent-browser`
  or `agent-browser install`** — there is no network path for a second browser download and
  nothing to install.
- Runs headless. There is no display in the sandbox; `--headed` has nothing to draw on.
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
