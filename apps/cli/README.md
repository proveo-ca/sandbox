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
mise run build --cli                               # install into GOPATH/bin
mise run deploy --print --cli v0.3.2                 # preview the release plan
```

Merge and verify the source changes through the usual PR to `main`. From a
clean checkout on your host, pin the approved commit and run the release.

```bash
git fetch --tags origin main
git switch --detach origin/main
mise run deploy --cli v0.3.2 --yes
```

Use an explicit version. The release command checks that `HEAD` is on
`origin/main` and runs the retirement ledger. It acquires the shared Git lock,
builds and stages from the pinned SHA, publishes to Cloudflare, verifies the
live manifest, and pushes a tag for that SHA. Use `--yes` to skip the local
confirmation. Local publishing requires Wrangler credentials and permission to
create and delete `cli-deploy-lock` and create release tags on the canonical
`origin`. For a checkout using the old repository name, run
`git remote set-url origin git@github.com:proveo-ca/cli.git`.

GitHub Actions runs the same command. Configure the `cli-production`
environment for `main` and supply its `CLOUDFLARE_API_TOKEN` and
`CLOUDFLARE_ACCOUNT_ID` secrets. Dispatch the workflow from GitHub or from
your host.

```bash
gh workflow run cli-release.yml --ref main -f version=v0.3.2
```

Retry with the same SHA and version. Local runs keep the artifact in
`.cache/cli-release/<version>/<sha>`. CI reruns restore it from the workflow
cache when available. If the exact manifest is already live, the retry finishes
by pushing the tag. A different manifest for the same version requires the
original artifact or a fresh version.

An interrupted process can leave `refs/heads/cli-deploy-lock` on `origin`.
After confirming the owning release has stopped, inspect the ref and remove it
using the displayed object ID as the lease. The lease protects a lock that
changes after inspection.

```bash
git ls-remote --refs origin refs/heads/cli-deploy-lock
git push --force-with-lease=refs/heads/cli-deploy-lock:<object-id> origin :refs/heads/cli-deploy-lock
```

Preview without a release:

```bash
mise run test-images                     # goreleaser --snapshot, stage, installer + image suites
mise x -- wrangler dev --cwd apps/cli    # serve the staged tree locally — never deploys
```

Run the CDN install test suite:

```bash
go test -count=1 ./internal/installtest/   # runs install.sh/uninstall.sh under /bin/bash
```
