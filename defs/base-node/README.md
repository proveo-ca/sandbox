# base-node

`proveo/base-node` = `proveo/base` + the TypeScript toolkit's runtimes: a Node 22 LTS
runtime (NodeSource) + `pnpm`, and [Bun](https://bun.sh) (`bun` + `bunx`). It exists so
the Node harnesses share the runtime layer once instead of each baking their own copy,
while `cursor` (self-contained binary) and `cecli` (Python) stay off it and carry no Node.

- FROM `proveo/base` (inherits git/gh/dumb-init/proveo-entrypoint/harden).
- Consumers: `opencode`, `claudecode` (mcp/solo/sol), and through `base-node-lsp` and
  `base-node-browser` every `-browser` variant.
- Not a runnable harness — a mise build/deploy target like `base`.

## What the project pins, the sandbox honours

The image ships ONE node, ONE pnpm and ONE bun; a repository pins its own, and the seed
(`ensure_node_toolchain`, both backends) aligns them: `packageManager: pnpm@…` / `yarn@…`
through corepack, `packageManager: bun@…`, `engines.bun` or `.bun-version` through mise
(corepack does not manage Bun; Bun is a mise core tool), `engines.node` through mise only
when the running node fails the range. A Bun project's dependencies are installed with
`bun install --frozen-lockfile` when `bun.lock`/`bun.lockb` is present.

## Pins

- Node major via `--build-arg NODE_MAJOR` (default 22).
- Bun via `BUN_VERSION` + `BUN_SHA256_X64` + `BUN_SHA256_AARCH64` — the digests published in
  the release's `SHASUMS256.txt`. Bump the three together; `internal/contract` checks the
  shape. `unzip` (Bun's Linux prerequisite) is installed here and inherited.

`build.sh` ensures `proveo/base` first; harness `build.sh` scripts call this def's `ensure.sh`,
whose floor probe requires node, pnpm and bun.
