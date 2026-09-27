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
does (`internal/egress/plan.go`). `entrypoint.sh` writes hermes's own `config.yaml`
(`model.provider custom`, `model.base_url <OLLAMA_API_BASE>/v1`, `model.default <tag>`) — hermes
ignores `OPENAI_BASE_URL`/`HERMES_MODEL` for this. Muse Glimmer and Qwen
3.8 are both published on the Ollama library. Docker Model Runner is intentionally not wired in —
it runs inference at the host-Docker-Desktop level, bypassing containerization, which doesn't
compose with this repo's per-sandbox isolated-microVM model.

## Local models on the model row

The model row picks the source: API keys (an external provider through your keys), Qwen 3.8 27B or
Muse Glimmer 30B. Each local option is a `--local-model` value for this host — "… MLX" on macOS arm64
(`qwen3.8:27b-mlx`, `muse-glimmer:30b-mlx`, 2.6x the generation speed of GGUF measured on an M4 Pro),
"… GGUF" elsewhere (`qwen3.8:latest`, `muse-glimmer:latest`). On sbx the model runs on **your host's
Ollama** and hermes reaches it at `host.docker.internal:11434`. proveo refuses before launch when Ollama
is not running, the tag is not pulled (it prints `ollama pull <tag>`), or the model would not fit in free
memory (`PROVEO_LOCAL_MODEL_FORCE=1` overrides). The baked-model images (`hermes-muse-glimmer`,
`hermes-qwen3.8`) are retired. See `_spec/internal/sbx/host-inference.puml`.

## Host Chrome (logged-in sites)

Tick **host chrome (CDP)** in the interface row (sbx only). proveo opens Chrome on a dedicated
profile (`~/Library/Application Support/proveo/host-chrome/hermes` on macOS) with CDP on
`127.0.0.1:9222` (`PROVEO_HOST_CDP_PORT` overrides), or reuses a browser already serving CDP there.
Log into the site in that window once; the profile keeps the session across runs. hermes attaches by
IP from inside the sandbox. It controls every tab in that profile — keep other logins out of it. See
`_spec/internal/sbx/host-browser-cdp.puml`.

## Explicitly out of scope

Hermes's cloud "Browser Use Cloud" tier ships bot-detection-evasion infrastructure (residential
proxies, CAPTCHA solving, fingerprint randomization). None of it is wired into this def, and no
future change here should add it. Local browser-profile reuse (the operator's own
already-authenticated cookies/saved logins) is supported upstream but ships **off** by default —
opt in explicitly if you need it, since it copies real credentials into the sandbox.

## Testing

`internal/imagetest/hermes_test.go` (build tag `image`) — `go test -tags=image -run
'^TestImageHermes$' ./internal/imagetest/`, or `mise run test-defs hermes`.
