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
atomically replaces the running binary. Stamp a release with a git tag / goreleaser
(or `PROVEO_VERSION=1.2.3 mise run build-cli -- --release`) so `latest.json` is not `dev`.

Publish:

```bash
mise run build-cli -- --release   # goreleaser into dist/ then stage
mise run deploy-cli               # build-cli --release, then Wrangler deploy
```

Run the CDN install test suite:

```bash
apps/cli/public/cli/tests/run_tests.sh
```
