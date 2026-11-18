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

### Publish a version (Cloudflare CDN, tag included)

Distribution is **Wrangler-only** — goreleaser builds multi-arch binaries; it does **not**
need to publish a GitHub Release for the consumer channel.

```bash
mise run deploy --cli                          # next patch after the newest v* tag
mise run deploy --cli --cli-version v0.4.0     # or minor | major | an explicit vX.Y.Z
mise run deploy all --cli --yes                # unattended: the CLI first, then every image
```

`deploy --cli` (internal/cdn/release) runs, in order:

1. the retirement ledger (`TestNoRetirementOutlivesOneRelease` under `PROVEO_RELEASE_GATE=1`);
2. one question, answered on a TTY or by `--yes`; nothing is written before it;
3. the cut: HEAD on `main` and equal to `origin/main` after a fetch, a clean tree except
   `latest.json` (restored from HEAD), `GORELEASER_CURRENT_TAG=<tag> goreleaser release --clean
   --skip=publish,validate`, the CDN stage, a commit of `latest.json` alone (`release: <tag>`),
   and `git tag <tag>` on that commit;
4. the staged check: `latest.json` names the tag on HEAD;
5. `wrangler deploy --cwd apps/cli`;
6. `git push --atomic origin HEAD:refs/heads/main refs/tags/<tag>`.

A HEAD it already cut resumes at step 4 when the tree is clean and `bin/` still holds the
binaries `latest.json` names. `mise run deploy --print --cli` prints the plan and runs none of it.
The binaries come from the commit before the tagged one; `proveo --version` still reports the tag.

Preview without a release:

```bash
mise run dev-cli    # goreleaser --snapshot, stage, then wrangler dev — never deploys
```

Run the CDN install test suite:

```bash
go test -count=1 ./internal/installtest/   # runs install.sh/uninstall.sh under /bin/bash
```
