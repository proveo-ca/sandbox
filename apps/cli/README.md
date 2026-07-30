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
    uninstall.sh     # prefers `proveo uninstall`; fallback strips PATH + ~/.proveo
    latest.json      # { version, checksums } — release channel for install/update
    checksums.txt    # SHA-256 of staged binaries (written by deploy-cli / build-cli --release)
    bin/
      proveo-linux-amd64
      proveo-linux-arm64
      proveo-darwin-amd64
      proveo-darwin-arm64
    tests/
      run_tests.sh
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

On an exact tag, `deploy-cli` runs `build-cli -- --release` with
`goreleaser release --clean --skip=publish` (version from the tag), stages
`latest.json` + `bin/` into `apps/cli/public/cli`, then `wrangler deploy`.

Untagged `mise run build-cli -- --release` still uses `goreleaser --snapshot` (dev/CI dry-run).
Override with `PROVEO_VERSION=…` only if you must force the channel stamp.

Publish pieces separately:

```bash
mise run build-cli -- --release   # goreleaser + stage CDN assets
pnpm exec wrangler deploy --cwd apps/cli
```

Run the CDN install test suite:

```bash
apps/cli/public/cli/tests/run_tests.sh
```
