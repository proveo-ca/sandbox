# Model bridges — one declaration, two consumers

Each `<harness>.tsv` declares how the shared role vars (`ARCHITECT_MODEL`,
`EDITOR_MODEL`, `SMALL_MODEL`) become the env vars a harness actually reads.
Tab-separated, five columns, applied top to bottom:

| column | meaning |
| --- | --- |
| `slot` | harness-facing name shown in the prompt header; `-` means internal, never displayed |
| `targets` | comma-separated env vars to set; an explicit value already in the environment always wins |
| `roles` | comma-separated fallback chain — the first non-empty role wins |
| `default` | literal value, `$OTHER_VAR` to copy another target, or `-` for none |
| `transform` | `normalize` (add a `provider/` prefix), `bare` (strip one), or `-` |

Row order is load-bearing: a `$VAR` default must come after the row that sets `VAR`.

Two consumers read exactly this file, which is the point:

- **shell** — `apply_model_bridges <harness>` in `packages/lib/entrypoint-lib.sh`, at
  container start. Shell has to be the executor because `proveo-entrypoint prep`
  cannot export into its parent (`_spec/cmd/proveo-entrypoint/prep-process-boundary.puml`).
- **host** — `internal/provider` reads the same table, embedded at build time, to
  print resolved slots in the prompt header before a container exists.

Before this file existed the mapping lived in three places: each def's entrypoint,
`internal/entrypoint.ApplyEnvBridges`, and a display table in `internal/provider`.
