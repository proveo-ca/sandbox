---
name: sync-providers
description: Reconcile proveo's provider registry with the upstream public docs — the LLM vendors' own API docs and each harness's supported-provider list — so detection, sbx credential injection and model-id resolution stay 1:1 with what the harnesses actually support. Use when adding a provider, when a harness ships new provider support, when a model id fails to resolve its provider, when a key is set but the provider still answers 401/403, or on a periodic drift check.
---

# Sync providers to their public docs

proveo hands each detected provider key to sbx's credential proxy, which attaches it
on that provider's hosts. Every provider proveo claims to support must be supported
in **four** places at once, and a gap in any one of them is silent — the user sets a
key, nothing errors, and the request leaves without it, so the provider's 401/403
reads like a bad key.

## The four places

| # | Place | File | What breaks if missing |
|---|---|---|---|
| 1 | Registry entry | `internal/provider/provider.go` | Never detected, never stored in sbx's secret store, never injected |
| 2 | Hosts | same entry (`Hosts`; `ACL` until that field retires) | Detected and stored, but sbx's proxy matches no domain and never attaches the key |
| 3 | Bare model-id prefix | `internal/provider/models.go` (`bareIDPrefixes`) | A bare model id resolves no provider, so the missing-key warning and the posture's `role providers` row skip it |
| 4 | Harness capability | `defs/<name>/harness.manifest` (`capabilities.providers`) | A harness is offered providers it cannot use, so proveo stores or forwards keys the agent never spends |

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
  - Codex CLI — `developers.openai.com/codex` (`[model_providers.*]` in config.toml)
  - cecli — the aider docs it forked from, `aider.chat/docs/llms.html`

If a page is unreachable or ambiguous, **say so and stop** for that harness. An
invented provider list produces inject domains that silently match nothing, or worse
a credential attached to a host nobody vetted.

### 2. Diff against the registry

```bash
rg -oP '\{Name: "\K[a-z]+(?=")' internal/provider/provider.go | sort
```

Report both directions — supported-but-missing, and registered-but-unsupported.
The second direction matters: a stale entry hands sbx a key, and its hosts, for a
provider no harness can reach.

### 3. Add or correct the registry entry

```go
{Name: "<name>", Detect: []string{"<PROVIDER>_API_KEY"},
    ACL: "dstdomain .<host>", Hosts: []string{".<host>"}, Auth: bearer("<PROVIDER>_API_KEY")},
```

`Detect` lists every env var the vendor's own docs bless, including legacy
aliases. `Hosts` must be the **narrowest** suffix that works — never a whole
cloud (`.amazonaws.com` is explicitly rejected by an existing test). Each host
becomes a Kit `inject[].domain` (`.host` → `host` + `*.host`,
`internal/sbx/domain.go`). `TestEveryRegisteredProviderIsReachable` still requires
`ACL`, though nothing outside tests reads it. A provider whose auth is not a bearer
token takes an explicit `[]AuthOption`.

### 4. Add the bare model-id prefix

Only when the vendor's ids are unambiguous. `gpt-`, `grok-`, `glm-` are safe;
a generic prefix that another vendor also uses is not — a wrong prefix attributes
the model to the wrong provider, and the missing-key warning names the wrong key.
When in doubt, leave it out: the operator can still use the `provider/model` form,
which always resolves.

### 5. Set per-harness capabilities

A vendor-locked harness declares exactly its vendor. A general harness declares
nothing (unconstrained) **unless** its docs name a closed list.

```yaml
capabilities:
  providers: [anthropic]   # claudecode: reads only its own key + OAuth token
```

This is not cosmetic. Detection is filtered through it, and so is the set of keys
proveo stores in sbx's secret store or forwards: a key the harness cannot use would
otherwise leave the host for nothing.

### 6. Verify

```bash
mise run ci
go test ./internal/provider/ ./internal/backend/sandbox/ ./cmd/proveo/
```

Then confirm the injection end to end for a representative provider:

```bash
PROVEO_WIZARD=off proveo run opencode --print   # names the written `sandbox kit: <dir>/spec.yaml`
```

The Kit's `credentials[].apiKey.inject[].domain` must name the provider's hosts. The
Kit declares a credential only once sbx's secret store holds it (`sbx secret ls`).

## What NOT to add

**The long tail routes through `openrouter`.** OpenCode's directory alone names
~48 providers and cecli's LiteLLM surface many more — 302.AI, Chutes, Poe,
STACKIT, SAP AI Core, Modal, Scaleway, OVHcloud, ZenMux and the rest. These are
deliberately absent: they are reachable via the `openrouter` entry or a custom
OpenAI-compatible endpoint, and every individually registered host is an unvetted
credential destination for a provider almost nobody names. Do not sweep them in.

Add a provider individually only when it clears one of these:
- a harness names it **first-class** in its own docs (not just via a gateway), or
- it is the **direct** vendor for a model family an operator would name in
  `ARCHITECT_MODEL`.

**Local runtimes are not providers.** Ollama, LM Studio and llama.cpp serve
models from the host with no key to inject; proveo handles them through
`--local-model`, and `ModelProvider` deliberately returns `""` for `ollama/` and
`openai-compatible/` prefixes because those endpoints serve arbitrary ids.

## Two rules that are easy to get backwards

**Injecting is not reaching.** For a harness that runs its own agent, the Kit
declares a credential for EVERY detected provider the harness allows and sbx's
secret store holds, because a session may switch model mid-run. Reach is sbx's
host-wide policy (`sbx policy`). proveo writes no provider allowlist; only the
inject domains of a declared credential enter the Kit's
`permissions.network.allow`, because SPEC-v2 requires them there.

**Never widen `Hosts` (or `ACL`) to make a test pass.** A failing reachability
test means the host suffix is wrong or the provider genuinely cannot be injected
(cloud-SDK auth like bedrock/vertex). Those are exempted by name in the test,
with the reason.

## Record the outcome

Provider support is part of the credential contract, so material changes belong in
`_spec/internal/provider/provider-registry.puml`, and the README's supported-tooling
pills are asserted against the registry by `TestReadmePillsMatchToolingRegistry`.
