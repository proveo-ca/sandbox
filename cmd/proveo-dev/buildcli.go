// SPEC: _spec/cmd/proveo/usage.puml, _spec/internal/cdn/distribution-update.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/ui"
)

const goreleaserVersion = "v2.17.0"

func init() {
	var release bool
	c := &cobra.Command{
		Use:   "build-cli [--release]",
		Short: "Install the Go proveo CLIs onto GOPATH/bin; --release adds goreleaser → dist/ + CDN stage",
		Long: `(default) go install → $(go env GOPATH)/bin (version=dev@sha)
--release runs goreleaser into dist/, then stages apps/cli CDN assets:
  • HEAD is an exact git tag (vX.Y.Z) → goreleaser release --skip=publish
    (version from the tag; no GitHub Release — CDN is Wrangler)
  • otherwise → goreleaser release --snapshot (dev/CI dry-run)`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return buildCLI(root, release)
		},
	}
	c.Flags().BoolVar(&release, "release", false, "goreleaser into dist/, then stage the CDN tree")
	register(c)
}

// installTargets are the binaries build-cli installs, with their ldflags.
func installTargets(version string) [][2]string {
	return [][2]string{
		{"./cmd/proveo", "-s -w -X main.version=" + version},
		{"./cmd/proveo-egress", "-s -w"},
		{"./cmd/proveo-entrypoint", "-s -w"},
	}
}

func devVersion(commit string) string {
	if commit == "" {
		commit = "unknown"
	}
	return "dev@" + commit
}

func goreleaserArgs(tag string) []string {
	if tag != "" {
		return []string{"release", "--clean", "--skip=publish"}
	}
	return []string{"release", "--snapshot", "--clean"}
}

func buildCLI(root string, release bool) error {
	gopath, err := output(root, "go", "env", "GOPATH")
	if err != nil {
		return err
	}
	gobin := filepath.Join(gopath, "bin")
	for k, v := range map[string]string{"CGO_ENABLED": "0", "GOBIN": gobin} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(gobin, 0o755); err != nil {
		return err
	}
	commit, _ := quietOutput(root, "git", "rev-parse", "--short", "HEAD")
	version := devVersion(commit)
	for _, t := range installTargets(version) {
		if err := run(root, nil, "go", "install", "-trimpath", "-ldflags="+t[1], t[0]); err != nil {
			return err
		}
	}
	ui.Storef("Installed %s/{proveo,proveo-egress,proveo-entrypoint} (%s)", gobin, version)
	ui.Notef("Ensure %s is on PATH (or set PROVEO_BIN).", gobin)

	if release {
		if err := goreleaserRelease(root, gobin); err != nil {
			return err
		}
	}
	ui.Okf("build-cli succeeded.")
	return nil
}

func goreleaserRelease(root, gobin string) error {
	if _, err := exec.LookPath("goreleaser"); err != nil {
		ui.Asyncf("goreleaser not on PATH — installing %s via go install...", goreleaserVersion)
		if err := run(root, nil, "go", "install", "github.com/goreleaser/goreleaser/v2@"+goreleaserVersion); err != nil {
			return err
		}
		if err := os.Setenv("PATH", gobin+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
			return err
		}
	}
	tag, _ := quietOutput(root, "git", "describe", "--tags", "--exact-match")
	if tag != "" {
		ui.Appf("Tagged HEAD %s — goreleaser release --skip=publish (CDN-only)", tag)
	} else {
		ui.Appf("No exact git tag on HEAD — goreleaser release --snapshot")
	}
	if err := run(root, nil, "goreleaser", goreleaserArgs(tag)...); err != nil {
		return err
	}
	ui.Storef("Artifacts under dist/ (gitignored).")
	return stageCDN(root, true)
}
