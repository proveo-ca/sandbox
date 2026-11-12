# Proveo CLI Distribution

This app is the Cloudflare-hosted distribution surface for the consumer `proveo`
**Go** binary.

Public install URL:

```bash
curl -fsSL https://proveo.ca/cli/install.sh | bash
```

The installer downloads a platform-specific binary (`bin/proveo-{os}-{arch}`),
verifies it against `checksums.txt`, and installs to `~/.proveo/bin/proveo`.

Current layout:

```txt
public/
  cli/
    install.sh       # checksum-verified Go binary installer (reads latest.json)
    install.ps1      # the Windows installer
    uninstall.sh     # prefers `proveo uninstall`; fallback strips PATH + ~/.proveo
    latest.json      # { version, checksums } — release channel for install/update
    checksums.txt    # SHA-256 of staged binaries (written by stage-cdn from goreleaser's dist/)
    bin/
      proveo-{linux,darwin,freebsd}-{amd64,arm64}
      proveo-windows-{amd64,arm64}.exe
```

`proveo update` fetches `latest.json`, verifies the platform asset checksum, and
atomically replaces the running binary.

### Publish a version (git tag → Cloudflare CDN)

Distribution is **Wrangler-only** — goreleaser builds multi-arch binaries; it does **not**
need to publish a GitHub Release for the consumer channel.

Intended workflow:

```bash
git tag -a v0.1.0 -m "…"
git push origin v0.1.0          # optional but good
mise run deploy-cli
```

`deploy-cli` depends on `mise run release`. On a tagged HEAD, `release` checks the
retirement ledger, runs `goreleaser release --clean --skip=publish` (version from the tag),
and stages `latest.json`, `checksums.txt` and `bin/` into `apps/cli/public/cli` from
goreleaser's `dist/`. `deploy-cli` then asks to confirm (`mise run --yes deploy-cli` in a
script) and runs `wrangler deploy`. An untagged HEAD refuses before goreleaser runs.

mise asks after the depends finish. A "no" therefore leaves `dist/` built and
`apps/cli/public/cli` restaged; `git checkout -- apps/cli/public/cli` restores the tracked files.
A dry run also meets the confirm: `mise run -n deploy-cli` exits 1 without a TTY, and
`mise run -n --yes deploy-cli` prints the release-ledger → goreleaser → stage-cdn → wrangler
plan and runs none of it.

Publish pieces separately:

```bash
mise run release                         # tagged HEAD: goreleaser + stage CDN assets
pnpm exec wrangler deploy --cwd apps/cli
```

Preview without a tag:

```bash
mise run snapshot   # goreleaser --snapshot into dist/ (version X-SNAPSHOT-<sha>)
mise run dev-cli    # snapshot, stage, then wrangler dev — never deploys
```

Run the CDN install test suite:

```bash
go test -count=1 ./internal/installtest/   # runs install.sh/uninstall.sh under /bin/bash
```
