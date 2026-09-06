#!/usr/bin/env python3
# SPEC: _spec/internal/provider/model-catalog.puml
"""Rank a gateway's models for internal/provider planFallback, from models.dev.

planFallback is tier 3 of model resolution: what a run uses when neither the
operator's remembered answer nor their .env can authenticate. It is a hand-
ordered list because models.dev carries no opinion — there is no "recommended"
field, every OpenCode Go model reports tool_call:true so capability cannot
discriminate, and sorting by release_date alone puts `omen-alpha` at the top.

So this ranks by recency AFTER dropping ids that name themselves provisional,
and it PRINTS what it dropped. The exclusion is a guess about naming, not a
contract: a model called neither alpha nor preview can still be unfit, and a
good one tagged `-exp` gets dropped here. Read the excluded list before
trusting the ranked one — that is the whole reason it is printed.

ENTITLEMENT IS THE HARD PART, not recency. Holding OPENCODE_API_KEY does not
say which plan it entitles — Zen and Go share the variable — so a fallback named
from the Go catalog assumes a subscription proveo cannot see. On a Zen key
opencode answers "configured model is not valid" and silently falls through to
whatever it likes; observed once as Whisper Large V3 Turbo, a 2024
speech-to-text model, driving a coding agent.

So `opencode-go` is refused as a source outright, and `--free-only` (the
default for gateway providers) keeps the ranking to ids the gateway serves to
any key.

  python3 scripts/rank-plan-fallbacks.py [provider] [--top N] [--json PATH]
"""
import argparse
import json
import re
import sys
import urllib.request

API = "https://models.dev/api.json"

# Ids that advertise themselves as not-for-production. A naming heuristic.
PROVISIONAL = re.compile(r"(^|[-_])(alpha|beta|preview|exp|experimental|free)([-_]|$)")


def load(path):
    if path:
        with open(path) as fh:
            return json.load(fh)
    with urllib.request.urlopen(API, timeout=60) as resp:
        return json.load(resp)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("provider", nargs="?", default="opencode")
    ap.add_argument("--top", type=int, default=3)
    ap.add_argument("--json", dest="path", help="read a saved api.json instead of fetching")
    args = ap.parse_args()

    if args.provider == "opencode-go":
        sys.exit(
            "opencode-go is plan-gated: proveo cannot see whether a key entitles Go, and\n"
            "a wrong guess degrades the run to whatever opencode picks instead. Rank\n"
            "`opencode` (Zen) instead — its -free tier is served to any key.")
    catalog = load(args.path)
    if args.provider not in catalog:
        sys.exit(f"{args.provider}: not in models.dev — check the id, do not guess one")
    models = catalog[args.provider]["models"]

    keep, dropped = [], []
    for m in models.values():
        # A fallback is the model that RUNS, not the best one: free ids are the
        # only ones a gateway serves regardless of plan.
        provisional = PROVISIONAL.search(m["id"]) and not m["id"].endswith("-free")
        entitled = m.get("cost", {}).get("input", 0) not in (0, None)
        (dropped if (provisional or entitled) else keep).append(m)
    for group in (keep, dropped):
        group.sort(key=lambda m: m.get("release_date", ""), reverse=True)

    def line(m):
        cost = m.get("cost", {}).get("input", "?")
        ctx = m.get("limit", {}).get("context", "?")
        return f'{m["id"]:<34} {m.get("release_date","?")}  ctx={ctx:<9} in=${cost}'

    print(f"# {args.provider}: {len(models)} models, {len(dropped)} look provisional\n")
    print(f"# READ THIS FIRST — excluded by the naming heuristic, which is a guess:")
    for m in dropped:
        print(f"#   {line(m)}")
    print(f"\n# ranked, newest first:")
    for m in keep[: args.top]:
        print(f'\t\t\t"{args.provider}/{m["id"]}", // {m.get("release_date","?")}, '
              f'{m.get("limit",{}).get("context","?")} ctx, '
              f'${m.get("cost",{}).get("input","?")}/M in')
    if len(keep) > args.top:
        print(f"# ({len(keep) - args.top} more not shown)")


if __name__ == "__main__":
    main()
