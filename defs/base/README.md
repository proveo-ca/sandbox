# base

`proveo/base` is the **minimal** shared floor for every harness image. It carries
only what is universal to all harnesses:

- `git`, `gh`, `ca-certificates`, `curl`, `dumb-init`, `bash`, `tmux`
- the `proveo-entrypoint` binary (built once here via a `golang` builder stage;
  every harness inherits `/usr/local/bin/proveo-entrypoint` instead of
  recompiling it)
- the `/usr/local/sbin/proveo-harden` pass (setuid/setgid strip + raw-network-tool
  removal)

It is `FROM debian:bookworm-slim` and lands at ~200 MB.

## What is deliberately NOT here

- **No language runtime.** Node and Python are not universal — `cursor` needs
  neither (its `cursor-agent` is a self-contained binary), `cecli` is Python,
  `opencode`/`claudecode` are Node. Node lives in the thin `proveo/base-node`
  intermediate (`defs/base-node`), which the Node harnesses build FROM; Python
  is added by the one harness that needs it (`cecli`).
- **No browsers in the base floor.** Playwright/Chromium fattened the shared floor
  to multiple GB, so it is not universal. Browser-capable Node harnesses build FROM
  the opt-in `proveo/base-node-browser` layer (`defs/base-node-browser`:
  base-node-lsp + Playwright + a Chromium browser under a world-readable
  `PLAYWRIGHT_BROWSERS_PATH`) as `<harness>-browser` variants
  (`opencode-browser`, `claudecode-browser`, `cursor-browser`), selected at run
  time from the `proveo run` capability picker (Tab to add, Enter continues). Default
  images stay
  slim; Chromium is downloaded once in `base-node-browser` and shared by every
  variant. (cecli/Python remains install-on-demand: `python -m playwright install
  chromium` into a mounted cache dir.)

## FROM tree

```
debian:bookworm-slim
└── proveo/base                    git/gh/jq/curl/dumb-init/bash/tmux/proveo-entrypoint/harden
     ├── cursor                    cursor-agent binary; no runtime
     ├── cecli                     + python3-venv (aider fork)
     └── proveo/base-node          + Node 22 LTS + pnpm
          └── proveo/base-node-lsp        + workspace language servers
               ├── opencode
               ├── claudecode (mcp)   └── claudecode-solidity
               └── proveo/base-node-browser   + Playwright + Chromium  (opt-in)
                    ├── opencode-browser
                    ├── claudecode-browser
                    └── cursor-browser        (cursor-agent atop the browser floor)
```

## Rules

- Harness Dockerfiles start with `ARG BASE_IMAGE=proveo/base:latest` (or
  `proveo/base-node:latest`) + `FROM ${BASE_IMAGE}` (the ARG is the
  white-label / pinning seam).
- Any harness layer that `apt-get install`s extras must end with
  `/usr/local/sbin/proveo-harden <paths>` — new packages can reintroduce setuid
  bits. Pass the prefixes you touched (e.g. `proveo-harden /usr /opt`) so the
  scan doesn't re-walk the whole filesystem.
- Harness `build.sh` scripts call `defs/base/ensure.sh` (or
  `defs/base-node/ensure.sh` for Node harnesses) first, so a lone
  `mise build <harness>` works on a clean machine and won't reuse a stale local
  tag from a different lineage.
- Per-harness users, runtimes, configs, and entrypoints do NOT belong here. If
  two harnesses need the same new tool, that is the bar for adding it to the
  appropriate shared layer.

It is not a runnable harness: no `harness.manifest`, no entrypoint. It is a mise
build/deploy target (`mise build base`) like the sidecar images.
