---
name: sync-providers
description: Reconcile proveo's provider registry with the upstream public docs — the LLM vendors' own API docs and each harness's supported-provider list — so detection, brokering, allowlists and model-id resolution stay 1:1 with what the harnesses actually support. Use when adding a provider, when a harness ships new provider support, when a model id fails to resolve its provider, when a key is set but the agent still gets the sentinel, or on a periodic drift check.
---

# Sync providers to their public docs

proveo brokers credentials and allowlists egress per provider. Every provider it
claims to support must be supported in **four** places at once, and a gap in any
one of them is silent — the user sets a key, nothing errors, and the agent gets a
sentinel that the provider rejects with a 401/403 that reads like a bad key.

## The four places

| # | Place | File | What breaks if missing |
|---|---|---|---|
| 1 | Registry entry | `internal/provider/provider.go` | Never detected, never brokered, never allowlisted |
| 2 | ACL / hosts | same entry (`ACL`, `Hosts`) | Detected and pinned, but Squid blocks every request |
| 3 | Bare model-id prefix | `internal/provider/models.go` (`bareIDPrefixes`) | `ARCHITECT_MODEL=<bare-id>` cannot pin the broker, so a multi-key host falls back to the sentinel |
| 4 | Harness capability | `defs/<name>/harness.manifest` (`capabilities.providers`) | A harness is offered providers it cannot use, which makes detection ambiguous and disarms the broker |

Three tests enforce the invariants between them. Run them first — they usually
name the gap for you:

```
go test ./internal/provider/ -run 'TestBareIDPrefixes|TestEveryRegisteredProviderIsReachable'
go test ./cmd/proveo/       -run TestInitAdvertisesOnlyRegisteredKeys
```

## Procedure

### 1. Establish what upstream actually says

Fetch the **current public docs**, do not rely on recall — provider lists move.

- Vendor API docs for the base URL, auth header shape, and the model-id naming
  convention (that last one drives step 4).
- Harness supported-provider lists:
  - OpenCode — `opencode.ai/docs` (it sources models from Models.dev)
  - Cursor CLI — `docs.cursor.com`
  - cecli — the aider docs it forked from, `aider.chat/docs/llms.html`

If a page is unreachable or ambiguous, **say so and stop** for that harness. An
invented provider list produces an allowlist that silently fails closed, or worse
one that opens a host nobody vetted.

### 2. Diff against the registry

```bash
rg -oP '\{Name: "\K[a-z]+(?=")' internal/provider/provider.go | sort
```

Report both directions — supported-but-missing, and registered-but-unsupported.
The second direction matters: a stale entry widens the allowlist for a provider
no harness can reach.

### 3. Add or correct the registry entry

```go
{Name: "<name>", Detect: []string{"<PROVIDER>_API_KEY"},
    ACL: "dstdomain .<host>", Hosts: []string{".<host>"}, Auth: bearer("<PROVIDER>_API_KEY")},
```

`Detect` lists every env var the vendor's own docs bless, including legacy
aliases. `Hosts` must be the **narrowest** suffix that works — never a whole
cloud (`.amazonaws.com` is explicitly rejected by an existing test). A provider
whose auth is not a bearer token takes an explicit `[]AuthOption`.

### 4. Add the bare model-id prefix

Only when the vendor's ids are unambiguous. `gpt-`, `grok-`, `glm-` are safe;
a generic prefix that another vendor also uses is not — a wrong pin sends the
credential to the wrong host. When in doubt, leave it out: the operator can still
use the `provider/model` form, which always resolves.

### 5. Set per-harness capabilities

A vendor-locked harness declares exactly its vendor. A general harness declares
nothing (unconstrained) **unless** its docs name a closed list.

```yaml
capabilities:
  providers: [anthropic]   # claudecode: reads only its own key + OAuth token
```

This is not cosmetic. Detection is filtered through it, and a key the harness
cannot use would otherwise make the broker ambiguous — it refuses to pin, and the
agent receives the sentinel.

### 6. Verify

```bash
mise run ci
go test ./internal/provider/ ./internal/egress/ ./cmd/proveo/
```

Then confirm the pin resolves end to end for a representative model:

```bash
PROVEO_WIZARD=off proveo run opencode --egress-mode allowlist --print | grep PROVEO_EGRESS_PROVIDER
```

## What NOT to add

**The long tail routes through `openrouter`.** OpenCode's directory alone names
~48 providers and cecli's LiteLLM surface many more — 302.AI, Chutes, Poe,
STACKIT, SAP AI Core, Modal, Scaleway, OVHcloud, ZenMux and the rest. These are
deliberately absent: they are reachable via the `openrouter` entry or a custom
OpenAI-compatible endpoint, and every individually allowlisted host is unvetted
egress surface for a provider almost nobody pins. Do not sweep them in.

Add a provider individually only when it clears one of these:
- a harness names it **first-class** in its own docs (not just via a gateway), or
- it is the **direct** vendor for a model family an operator would name in
  `ARCHITECT_MODEL`.

**Local runtimes are not providers.** Ollama, LM Studio and llama.cpp serve
models over loopback with no egress to allowlist; proveo handles them through
`--local-model`, and `ModelProvider` deliberately returns `""` for `ollama/` and
`openai-compatible/` prefixes because those endpoints serve arbitrary ids.

## Two rules that are easy to get backwards

**Pinning is not reaching.** The broker pins ONE provider because it injects one
credential. The Squid allowlist covers EVERY detected provider, because a session
may switch model mid-run. Only a manifest `provider:` lock narrows the allowlist.

**Never widen an ACL to make a test pass.** A failing reachability test means the
host suffix is wrong or the provider genuinely cannot be brokered (cloud-SDK auth
like bedrock/vertex). Those are exempted by name in the test, with the reason.

## Record the outcome

Provider support is part of the egress contract, so material changes belong in
`_spec/internal/egress/egress-tiers.puml`, and the README's supported-tooling
pills are asserted against the registry by `TestReadmePillsMatchToolingRegistry`.
