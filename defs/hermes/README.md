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

## Baked-model variants

`hermes-muse-glimmer` and `hermes-qwen3.8` (`defs/hermes/baked-model/Dockerfile`, `Needs: "hermes"`)
bake the model's weights into the image at build time — `ollama serve & ... ollama pull <tag> &&
pkill` during `docker build`, same pattern `base-node-browser` uses for Chromium. No sidecar
container, no `PROVEO_LOCAL_MODEL`, no runtime network dependency for inference: `entrypoint.sh`
starts a local `ollama serve` itself when it detects `OLLAMA_BAKED_MODEL_TAG` baked into the image,
waits for it to become ready, and writes the same `config.yaml` wiring at `localhost:11434` before
launching hermes. Trade-offs, measured building and running `hermes-muse-glimmer` in a sandbox:

- **Image size:** ~20GB (the `q4_K_M` weights are 17GB; `nvfp4`/`mlx` tags need Apple's MLX runtime
  and fail to pull on Linux).
- **Build disk:** peaks at ~3x the model size on the Docker volume — ~52GB for muse-glimmer — because
  BuildKit holds the build snapshot, the exported layer, and the unpacked image at once. Build one
  variant at a time, and set `PROVEO_BUILDKIT_CACHE=off` so the layer is not also exported to
  `~/.cache/proveo/buildkit`.
- **Inference speed:** in a CPU-only VM (no GPU; a Mac host exposes no Metal to the sandbox), a 30B
  model processes ~2 prompt tokens/s — too slow for hermes's agent prompt. These variants need a GPU
  host to be usable; the default `hermes` target stays lean and network-based.

## Explicitly out of scope

Hermes's cloud "Browser Use Cloud" tier ships bot-detection-evasion infrastructure (residential
proxies, CAPTCHA solving, fingerprint randomization). None of it is wired into this def, and no
future change here should add it. Local browser-profile reuse (the operator's own
already-authenticated cookies/saved logins) is supported upstream but ships **off** by default —
opt in explicitly if you need it, since it copies real credentials into the sandbox.

## Testing

`internal/imagetest/hermes_test.go` (build tag `image`) — `go test -tags=image -run
'^TestImageHermes$' ./internal/imagetest/`, or `mise run test-defs hermes`.
