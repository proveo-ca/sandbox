---
name: sync-models
description: Sync the model catalog to each harness's published model list, keeping role assignment portable across harnesses. Use when adding a model, when a harness reports an id it does not recognize, or on a periodic refresh.
---

# Sync the model catalog

Model ids are **immutable once published** — vendors add ids, they do not
redefine them. So this table goes stale only by omission, and the job is always
additive: find ids that exist and are missing, never rewrite ids already there.

## Fetch, never recall

Do not write a model id from memory. Ever. A recalled id that looks right is the
failure mode this skill exists to prevent: it becomes authoritative in the
repo, and the operator gets an "invalid model" error from the harness with no
indication that proveo supplied the wrong spelling.

Read each harness's own published list:

- **opencode** — models.dev is its catalog; ids are `provider/model`.
- **cecli / aider** — litellm ids, `provider/model`.
- **claudecode** — Anthropic's model list; ids go in `ANTHROPIC_MODEL`.
- **cursor** — vendor-pinned. Record as unsupported; do NOT emit an id.

If a source cannot be reached, stop and say so. A partial sync that silently
skips a harness is worse than no sync, because the gap is invisible afterwards.

## The four places a model must line up

1. `internal/provider/models.go` — `knownModels[provider]` for ids you can
   confirm, and `bareIDPrefixes` if a new **family** prefix appears (e.g. a new
   vendor whose ids start with something unmapped).
2. `ambiguousBareIDs` — add any open-weights family that **several** providers
   serve. An id that names the model but not the host must refuse attribution
   rather than guess.
3. The per-harness spelling, where the harness needs one that differs from the
   canonical id.
4. `_spec/internal/provider/model-catalog.puml` — only if the *shape* changes.
   Never add ids to the spec; it documents structure, not data.

## What NOT to add

- **Long-tail models reachable via openrouter.** Same rule as providers: an
  operator can name `openrouter/<vendor>/<model>` directly. The catalog covers
  what someone would plausibly set as a role.
- **Local ids.** `ollama/*` and `openai-compatible/*` serve arbitrary names;
  `ModelProvider` deliberately returns "" for them.
- **A prefix claiming a multi-hosted family.** `gpt-oss`, `llama-`, `qwen`,
  `mixtral` are served by many providers — these belong in `ambiguousBareIDs`,
  not `bareIDPrefixes`. Claiming one for its originator routes attribution to
  the wrong vendor.

## Why attribution is advisory

Nothing here gates a run. The broker holds a route for **every** detected
provider, so a model the table cannot attribute still authenticates as long as
its key is present. Attribution drives two things only:

- the missing-key warning (`Roles.MissingKeys`), which names the role, the
  model and the env var; and
- optional narrowing of the route set.

So a catalog miss costs a warning, never a failure. Keep it that way: if you
find yourself making resolution a precondition for launching, stop — that
would make a hand-refreshed table the reason a model released this week
cannot be used.

## Verify

```bash
go test ./internal/provider/
```

`TestRolesSpanningVendors` is the case that matters: roles pointed at two
different vendors must attribute to both.
