# Claude Code Paradigm (ML Blackbox Algorithm)

## Intended Working Mode
Claude Code operates as a machine-learning-style execution loop:
spec → plan → implement → verify → repeat until goal achieved.

The harness deliberately runs with `--dangerously-skip-permissions` to enable full automation inside the container sandbox. This is an explicit part of the paradigm, not a temporary shortcut.

## Core Principles
- Small, verifiable loops are preferred over large open-ended tasks.
- The model must be given acceptance criteria, verification commands, and failure-inspection instructions.
- Human provides the goal and stopping condition; the agent loops autonomously.
- Errors are expected; the value is fast iteration inside the loop.

## Entrypoint Responsibilities (`defs/claudecode/*/entrypoint.sh`)
- Run as the invoking host user, never root (see `paradigms.md` — Runtime User Boundary): the wrapper passes `--user $(id -u):$(id -g)` and `ensure_runtime_user` makes any uid usable.
- Load `.env`.
- Report git context at startup (shared `report_git_context`, pointed at the `/workspace/input` mount): git-tracked repo or not, remote origin (or "not tracking a remote repo"), commit identity, and whether a gh session is authenticated.
- Optional RTK attach.
- Smoke-test mode support.
- Launch `claude --dangerously-skip-permissions`.
- Future: seed `CLAUDE.md` if absent, detect verification commands, surface them in the session.

## Required Steering (`CLAUDE.md`)
- Encode the loop pattern explicitly.
- Require the agent to state acceptance criteria before starting.
- Require the agent to identify verification commands before editing.
- Require the agent to inspect failures and adjust hypothesis on each iteration.
- Stop only when verification passes or the human intervenes.

## Permission Posture
`--dangerously-skip-permissions` is intentional. The container sandbox, tmpfs mounts, and network isolation are the safety boundary. The harness must document this clearly.

### Outbound Web Access Policy
Claude Code is allowed to perform read-oriented web operations (web searches, `WebFetch`, package metadata reads, documentation lookups). Pinecone docs (`https://docs.pinecone.io/guides/get-started/overview`), Claude Code docs (`https://code.claude.com/docs/en/overview`), Google, and DuckDuckGo are examples only; the policy must allow any documentation site or search engine over the safe web protocols.

Network access is constrained by protocol, not by one-off blocked services. The allowed protocol set is HTTP and HTTPS only, routed through the configured proxy chain. HTTP/2 is allowed only as HTTPS traffic through the proxy. FTP, SSH, database protocols, mail protocols, Redis, Docker daemon ports, and arbitrary raw TCP/UDP are denied by default.

Write/mutation operations that require authentication (npm publish, PyPI uploads, git push, registry writes) are blocked where the proxy can observe method/path/host policy. Squid alone sees only CONNECT host/port for HTTPS, so host/port policy is its hard boundary; the mitmproxy inspector adds the decrypted method/path view.

Two proxies coexist in `firewall` mode:
- **Enforcement proxy** (Squid): default-deny outside HTTP/HTTPS, allow-list for read/search destinations and methods (`GET`, `HEAD`, `OPTIONS`), with explicit provider API exceptions. It is the only container with internet egress.
- **Inspection proxy** (mitmproxy): the agent's first hop. It performs TLS interception to decrypt and record each request (method/path/host) as NDJSON, then forwards to Squid as its upstream. The agent trusts mitmproxy's generated CA via standard CA env vars; mitmproxy has no direct internet route.

Docker networking enforces the path: `claudecode` only reaches mitmproxy, mitmproxy only reaches Squid, and only Squid has internet egress.

`--local-model` still starts an Ollama sidecar on the agent network (`NO_PROXY=ollama,localhost,127.0.0.1`), but Claude Code speaks the Anthropic API shape, not Ollama's OpenAI-compatible endpoint — the sidecar is not a drop-in (see [`_spec/testing.md`](../../testing.md)). Use opencode or cecli for fully local inference.

The Squid layer uses deterministic reserved/private/bogon destination blocks by default, informed by FireHOL's deny-all/allow-some firewall posture and bogon/fullbogon guidance. Dynamic FireHOL blocklist-ipsets such as `firehol_level1` are optional generated ACLs because FireHOL's own IP list documentation warns that third-party threat feeds can create false positives.

## Differentiation from Other Paradigms
- No subagent crew orchestration.
- No pair-programming containment.
- Emphasis on autonomous repetition until a measurable goal is reached.
