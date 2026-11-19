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
    checksums.txt    # SHA-256 of staged binaries (written by internal/cdn/stage from goreleaser's dist/)
    bin/
      proveo-{linux,darwin,freebsd}-{amd64,arm64}
      proveo-windows-{amd64,arm64}.exe
```

`proveo update` fetches `latest.json`, verifies the platform asset checksum, and
atomically replaces the running binary.

### Build locally and publish a release

```bash
mise run build --cli                           # install into GOPATH/bin
mise run deploy --print --cli                   # preview the release plan
mise run deploy --cli                          # prepare the next patch release
mise run deploy --cli v0.4.0                    # choose an explicit version, minor, or major
```

Start the release on a clean `main` synced with `origin/main`, with GitHub CLI
access to the repository. If `origin` still uses the old repository name, update it
with `git remote set-url origin git@github.com:proveo-ca/cli.git`.

1. The release checks the retirement ledger and asks for confirmation. `--yes`
   answers the local prompt.
2. GoReleaser builds the binaries and the stage writes the CDN assets. The
   task commits `latest.json` on `release/<tag>`, pushes that branch, and
   opens a PR into `main`.
3. Merge the PR on GitHub. Keep the local release branch and staged assets.
4. Rerun the same deploy command from `release/<tag>`. The task checks the
   merged tree and the staged binaries, publishes to Cloudflare, and pushes the
   tag for the commit on `main`.

`mise run deploy all --cli --yes` continues to the image deploy after the CLI
release completes. GitHub merge requirements apply in both runs.

Preview without a release:

```bash
mise run test-images                     # goreleaser --snapshot, stage, installer + image suites
mise x -- wrangler dev --cwd apps/cli    # serve the staged tree locally — never deploys
```

Run the CDN install test suite:

```bash
go test -count=1 ./internal/installtest/   # runs install.sh/uninstall.sh under /bin/bash
```
