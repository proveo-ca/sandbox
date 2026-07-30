# Roadmap

`proveo/sandbox` turns any repo into a **one-command, hardened sandbox** for AI coding
agents. The adversarial security review is resolved and the core capabilities have
landed. This roadmap is about turning that into a **shippable, self-defending
pipeline** — and charting the longer-term isolation direction.

Phases are ordered by leverage, not dates: **Now → Next → Then → Later**, plus a
**Horizon** for the exploratory bet.

## Landed

- **Egress security** — broker / proxy / firewall modes, credential broker (keys never
  enter the agent), Squid allowlist + MITM DLP. All review findings resolved.
- **Local models on the GPU** — opencode and cecli via an Ollama sidecar;
  macOS routes to the host GPU, Linux gets `--gpus`. (Cursor is vendor-pinned;
  Claude Code needs Anthropic API shape — sidecar is not a drop-in.)
- **Browser variants** — `base-node-browser` + `opencode/claudecode/cursor-browser`
  (Playwright + Chromium, one shared layer).
- **Cursor broker-default** — its vendor-pinned auth only works brokered.
- **Run capability picker** — Tab to add browser / DinD, Enter to continue.
- **Agent-E2E** in `tests/e2e/` — side-effect + credential-isolation suites, green.
- **Docker-Sandbox adoption experiment** — `_spec/experiments/docker-sandbox.puml`.

## Now — ship what's built

*Goal: get this session's work into users' hands.*

- [ ] `mise build-cli` → `deploy-cli` — the capability picker, cursor broker-default, local-model wiring, macOS-host-GPU / Linux `--gpus` routing, and the `proveo/sandbox` rename + tagline.
- [ ] `mise deploy` the new images — `base-node-browser` + the `-browser` variants.
- [ ] Unblock the macOS keychain (`docker-credential-osxkeychain -50`) so registry push/pull auth works again.

*Outcome:* `curl … | bash` installs the current CLI; browser variants are pullable.

## Next — a self-defending pipeline (CI)

*Goal: no silent regression of the resolved review findings.* (`_spec/testing.md`)

- [ ] **Static-analysis gate** — `.golangci.yml` + hard gates: `gofmt -l internal cmd tests` · `go vet ./...` · `golangci-lint run` · `go build ./...` · `go test -race ./...`. Enforces `tests/go-testing-standards.md`.
- [ ] **L1 unit + L2 contract** — every push, no Docker: `go test -race ./...` + `bash defs/tests/run_contract_tests.sh` (Go↔Bash parity).
- [ ] **L3 egress infra-integration** — Docker runner: build `proveo/egress-proxy`, then `PROVEO_EGRESS_INTEGRATION=1 go test ./internal/egress/ -run TestFirewall`.
- [ ] **L4 agent-E2E** — `PROVEO_LLM_TEST=1` on a **GPU runner** (`TestPromptfulE2E` + `TestCredentialForwardingIntegrity`); pin a small model via `PROVEO_TEST_LOCAL_MODEL` on CPU-only CI. Extend to claudecode + cecli (only opencode is scripted today).
- [ ] **Build the full image graph in CI** — the base chain (`base → base-node → base-node-lsp → base-node-browser`) + harnesses + `-browser` variants + sidecars. `.goreleaser.yaml` covers only the binary.

*Outcome:* every push gated; every egress mode + agent exercised.

## Then — supply chain & coverage

*Goal: trust the artifacts, pin the invariants.*

- [ ] **Digest-pin the enforcement images** (`ubuntu/squid`, `proveo/egress-proxy`, `ollama/ollama`) — the egress trust root, currently floating `:latest`. *(review S6)*
- [ ] **CDN install trust** — the advertised `proveo.ca/cli` path self-verifies (checksum + binary from one origin). Advertise the GitHub-release-verified `dist/install.sh` instead, or sign the CDN artifacts. *(review L3)*
- [ ] **Full egress validation matrix** — pin each mode's invariants (broker direct egress; proxy denies RFC1918 + metadata + writes; firewall MITM decrypt/allow/deny + flow record; local model reachable while denies hold) + active Squid / egress-proxy readiness probes.
- [ ] **Pre-bake `@ai-sdk/openai-compatible`** into `base-node-browser`/opencode so opencode's local model resolves under **firewall** (runtime install is blocked by the locked egress; broker + host-GPU already work).

*Outcome:* pinned, verifiable, matrix-covered.

## Later — reach & polish

- [ ] **Cursor under enforced egress** — decrypt cursor-agent's TLS to `api2.cursor.sh` so firewall can broker it, or document the broker-only limitation. *(follow-up #2)*
- [ ] `PROVEO_EGRESS_INSPECTOR=…` to attach the firewall inspector to any harness, reusing the sidecar/log/dashboard pipeline.
- [ ] Publish the **egress-dashboard** build in CI + smoke-test it against a recorded `flows.ndjson`.
- [ ] Record the **live** hero gif (`vhs _spec/assets/hero.tape`) once the host toolchain is unbroken (brew `trust.rb` · ffmpeg x265 · docker keychain `-50`), replacing the hand-built `_spec/assets/hero.gif`.

## Horizon — isolation substrate (exploratory)

Docker shipped **Sandboxes** — a microVM per workspace with deny-by-default egress,
host-side credential injection ("keys never enter the VM"), and a private Docker
Engine. The experiment in [`_spec/experiments/docker-sandbox.puml`](_spec/experiments/docker-sandbox.puml)
maps proveo's ~2,800 LOC + 5 sidecars + per-session MITM-CA stack onto `sbx`
primitives: the egress topology, credential broker, privileged DinD, in-guest
hardening, and teardown could largely **collapse**.

Open questions before committing: platform gating (Apple-Silicon / KVM / Hyper-V),
the **DLP content-scan gap** (sbx allowlists domains, it does not inspect payloads),
pre-GA status, and whether adoption reframes proveo as **Kits/templates on `sbx`**
rather than a `docker run` wrapper.
