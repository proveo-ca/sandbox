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

## Re-rank the plan fallbacks

`internal/provider` keeps a `planFallback` list per harness and billing side:
tier 3 of model resolution, used when neither the operator's remembered answer
nor their `.env` can authenticate. Unlike the catalog above, this list **is**
rewritten on a sync — it is a recommendation, and recommendations go stale.

```
python3 scripts/rank-plan-fallbacks.py opencode --top 3
```

**Never rank `opencode-go`.** The script refuses it, and the reason is the one
thing this list gets wrong most easily: holding `OPENCODE_API_KEY` does not say
which plan it entitles — Zen and Go share the variable. A fallback named from
the Go catalog assumes a subscription proveo cannot see, and when the key is a
Zen key opencode answers "configured model is not valid" and silently falls
through to whatever it likes. Observed once as Whisper Large V3 Turbo: a 2024
speech-to-text model, driving a coding agent.

A fallback is the model that **runs**, not the best one. Only zero-cost ids
qualify, because those are what the gateway serves to any key.

It fetches models.dev, keeps only zero-cost ids, drops the rest and anything
whose name advertises it as provisional (`alpha`, `beta`, `preview`, `exp`),
sorts what remains by `release_date`, and prints pasteable Go lines.

**Read the excluded block before the ranked one.** That filter is a guess about
naming, not a contract:

- a model called neither alpha nor preview can still be unfit for an agent;
- a good model tagged `-exp` gets dropped;
- a priced model is excluded because proveo cannot see whether this key can pay
  for it, not because it is worse.

Sorting by recency alone is what makes the filter necessary at all: the newest
OpenCode Go model at the time of writing was `omen-alpha`. models.dev carries
`release_date`, `cost`, `limit.context` and capabilities — but no
`recommended` field, and every Go model reports `tool_call: true`, so nothing
in the data distinguishes production-ready from a preview. The judgement is
yours; the script only removes the recall.

Keep any id a human deliberately named, even when the ranking puts it lower —
the list is ordered judgement, and `PlanFallback` walks it until one resolves,
so a lower entry costs nothing until the ones above it are retired.

After editing, `go test ./internal/provider/` — `TestPlanFallbacksAreRealModels`
checks every id still resolves through the registry *and* still lands on the
billing side it is filed under.
