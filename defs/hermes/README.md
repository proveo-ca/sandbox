# hermes

Wraps [Hermes Agent](https://github.com/NousResearch/hermes-agent) (NousResearch, MIT) — a
general-purpose assistant with native browser automation (navigate, click, type, screenshot),
not a narrow coding agent. Chosen over building manual MCP browser tooling on top of Docker
Agent because Hermes already ships the browser tool, local-model support, and a Docker
backend out of the box.

## Base image

`defs/hermes/Dockerfile` builds `FROM nousresearch/hermes-agent:${HERMES_AGENT_VERSION}`
directly — not proveo's own `base-node-lsp`/`base-node-browser` chain. Upstream's image already
bakes Python, Node, Chromium, ffmpeg, and ripgrep; extending `base-node-browser` on top would
duplicate that Chromium install rather than reuse it. See
`_spec/defs/hermes/hermes-paradigm.puml` for the full reasoning, including why this def
overrides upstream's s6-overlay `ENTRYPOINT` with the usual `dumb-init` + `entrypoint.sh`
single-process launch every other def here uses.

## Local models

`PROVEO_LOCAL_MODEL` wires Hermes at an Ollama sidecar the same way `opencode`'s `--local-model`
does (`internal/egress/plan.go`), via `OPENAI_BASE_URL`/`OPENAI_API_KEY=ollama` — Hermes takes any
OpenAI-compatible endpoint natively, so no bespoke config file is needed. Muse Glimmer and Qwen
3.8 are both published on the Ollama library. Docker Model Runner is intentionally not wired in —
it runs inference at the host-Docker-Desktop level, bypassing containerization, which doesn't
compose with this repo's per-sandbox isolated-microVM model.

## Explicitly out of scope

Hermes's cloud "Browser Use Cloud" tier ships bot-detection-evasion infrastructure (residential
proxies, CAPTCHA solving, fingerprint randomization). None of it is wired into this def, and no
future change here should add it. Local browser-profile reuse (the operator's own
already-authenticated cookies/saved logins) is supported upstream but ships **off** by default —
opt in explicitly if you need it, since it copies real credentials into the sandbox.

## Testing

`internal/imagetest/hermes_test.go` (build tag `image`) — `go test -tags=image -run
'^TestImageHermes$' ./internal/imagetest/`, or `mise run test-defs hermes`.
