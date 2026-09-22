// SPEC: _spec/internal/cdn/distribution-update.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/ui"
)

// cdnPlatforms is every GOOS/GOARCH the CDN publishes a host binary for.
var cdnPlatforms = [][2]string{
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"freebsd", "amd64"}, {"freebsd", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

func init() {
	register(&cobra.Command{
		Use:   "stage-cdn",
		Short: "Stage host proveo binaries + checksums.txt + latest.json into apps/cli/public/cli",
		Long: `Prefers goreleaser archives under dist/; falls back to cross-compiling.
Env: PROVEO_CDN_ROOT (default apps/cli/public/cli), PROVEO_DIST_DIR (default dist),
PROVEO_VERSION (release stamp), PROVEO_CDN_REQUIRE_DIST=1 (fail instead of cross-compiling).`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return stageCDN(root, os.Getenv("PROVEO_CDN_REQUIRE_DIST") == "1")
		},
	})
}

type cdnLayout struct{ root, dist, bin string }

func cdnPaths(repo, cdnOverride, distOverride string) cdnLayout {
	l := cdnLayout{root: cdnOverride, dist: distOverride}
	if l.root == "" {
		l.root = filepath.Join(repo, "apps", "cli", "public", "cli")
	}
	if l.dist == "" {
		l.dist = filepath.Join(repo, "dist")
	}
	l.bin = filepath.Join(l.root, "bin")
	return l
}

func cdnBinName(goos string) string {
	if goos == "windows" {
		return "proveo.exe"
	}
	return "proveo"
}

func cdnAssetName(goos, goarch string) string {
	name := "proveo-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func stageCDN(repo string, requireDist bool) error {
	l := cdnPaths(repo, os.Getenv("PROVEO_CDN_ROOT"), os.Getenv("PROVEO_DIST_DIR"))
	version := resolveCDNVersion(l.dist, os.Getenv("PROVEO_VERSION"), func(args ...string) (string, error) {
		return quietOutput(repo, "git", append([]string{"-C", repo}, args...)...)
	})
	if err := os.Setenv("PROVEO_VERSION", version); err != nil {
		return err
	}
	if err := resetCDNTree(l); err != nil {
		return err
	}

	usedDist := false
	for _, p := range cdnPlatforms {
		goos, goarch := p[0], p[1]
		asset := cdnAssetName(goos, goarch)
		dest := filepath.Join(l.bin, asset)
		ok, err := extractFromDist(l.dist, goos, goarch, dest)
		switch {
		case err != nil:
			return err
		case ok:
			usedDist = true
			ui.Appf("staged from dist: %s", asset)
		case requireDist:
			ui.Failf("release staging requires a goreleaser archive for %s/%s under", goos, goarch)
			ui.Notef("%s, but none was found. Build real artifacts first:", l.dist)
			ui.Notef("  mise run build-cli -- --release")
			return &exitError{code: 1}
		default:
			ui.Appf("cross-compiling proveo %s/%s (version=%s)...", goos, goarch, version)
			if err := run(repo, crossEnv(goos, goarch), "go", crossArgs(version, dest)...); err != nil {
				return err
			}
			if err := os.Chmod(dest, 0o755); err != nil {
				return err
			}
			ui.Appf("staged via go build: %s", asset)
		}
	}

	sums, err := checksumBin(l.bin)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(l.root, "checksums.txt"), []byte(checksumsText(sums)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(l.root, "latest.json"), []byte(latestJSON(version, sums)), 0o644); err != nil {
		return err
	}

	ui.Storef("Wrote %s, %s/checksums.txt, %s/latest.json (version=%s)", l.bin, l.root, l.root, version)
	if !usedDist {
		ui.Notef("Note: no goreleaser archives found under %s — used go build fallback.", l.dist)
		ui.Notef("For release artifacts: mise run build-cli -- --release (or mise run deploy-cli, which does that first)")
	}
	return nil
}

// resolveCDNVersion: PROVEO_VERSION, dist/artifacts.json, dist/VERSION, git describe, "dev".
func resolveCDNVersion(dist, env string, git func(args ...string) (string, error)) string {
	if env != "" {
		return strings.TrimPrefix(env, "v")
	}
	if b, err := os.ReadFile(filepath.Join(dist, "artifacts.json")); err == nil {
		if v := artifactsVersion(b); v != "" {
			return strings.TrimPrefix(v, "v")
		}
	}
	if b, err := os.ReadFile(filepath.Join(dist, "VERSION")); err == nil {
		return strings.TrimPrefix(strings.Join(strings.Fields(string(b)), ""), "v")
	}
	for _, args := range [][]string{{"describe", "--tags", "--exact-match"}, {"describe", "--tags", "--always"}} {
		if tag, err := git(args...); err == nil && tag != "" {
			return strings.TrimPrefix(tag, "v")
		}
	}
	return "dev"
}

func artifactsVersion(b []byte) string {
	var arts []struct {
		Extra map[string]any `json:"extra"`
	}
	if json.Unmarshal(b, &arts) != nil {
		return ""
	}
	for _, a := range arts {
		if v, ok := a.Extra["Version"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func resetCDNTree(l cdnLayout) error {
	if err := os.MkdirAll(l.bin, 0o755); err != nil {
		return err
	}
	stale := []string{
		filepath.Join(l.bin, "proveo"), filepath.Join(l.bin, "help.sh"), filepath.Join(l.bin, "init.sh"),
		filepath.Join(l.root, "checksums.txt"), filepath.Join(l.root, "latest.json"),
	}
	old, err := filepath.Glob(filepath.Join(l.bin, "proveo-*"))
	if err != nil {
		return err
	}
	for _, f := range append(stale, old...) {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.RemoveAll(filepath.Join(l.root, "lib"))
}

func crossEnv(goos, goarch string) []string {
	return []string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch}
}

func crossArgs(version, dest string) []string {
	return []string{"build", "-trimpath", "-ldflags=-s -w -X main.version=" + version, "-o", dest, "./cmd/proveo"}
}

// extractFromDist copies the goos/goarch proveo binary out of dist/ into dest.
func extractFromDist(dist, goos, goarch, dest string) (bool, error) {
	bin := cdnBinName(goos)
	for _, ext := range []string{".tar.gz", ".tgz", ".zip"} {
		archives, _ := filepath.Glob(filepath.Join(dist, "proveo_*_"+goos+"_"+goarch+ext))
		for _, a := range archives {
			var ok bool
			var err error
			if ext == ".zip" {
				ok, err = extractZip(a, bin, dest)
			} else {
				ok, err = extractTarGz(a, bin, dest)
			}
			if err != nil || ok {
				return ok, err
			}
		}
	}
	flat := filepath.Join(dist, "proveo-"+goos+"-"+goarch)
	candidates := []string{flat}
	if goos == "windows" {
		candidates = append(candidates, flat+".exe")
	}
	raw, _ := filepath.Glob(filepath.Join(dist, "proveo_"+goos+"_"+goarch+"*", bin))
	for _, f := range append(candidates, raw...) {
		if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
			return true, copyExecutable(f, dest)
		}
	}
	return false, nil
}

func archiveMember(name, bin string) bool {
	return name == bin || strings.HasSuffix(name, "/"+bin)
}

func extractTarGz(archive, bin, dest string) (bool, error) {
	f, err := os.Open(archive)
	if err != nil {
		return false, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return false, nil
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return false, nil
		}
		if h.Typeflag == tar.TypeReg && archiveMember(h.Name, bin) {
			return true, writeExecutable(dest, tr)
		}
	}
}

func extractZip(archive, bin, dest string) (bool, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return false, nil
	}
	defer func() { _ = zr.Close() }()
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() || !archiveMember(zf.Name, bin) {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return false, err
		}
		defer rc.Close()
		return true, writeExecutable(dest, rc)
	}
	return false, nil
}

func copyExecutable(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeExecutable(dest, f)
}

func writeExecutable(dest string, r io.Reader) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dest, 0o755)
}

type assetSum struct{ name, sum string }

func checksumBin(dir string) ([]assetSum, error) {
	files, err := filepath.Glob(filepath.Join(dir, "proveo-*"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	sums := make([]assetSum, 0, len(files))
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		_, err = io.Copy(h, fh)
		fh.Close()
		if err != nil {
			return nil, err
		}
		sums = append(sums, assetSum{name: filepath.Base(f), sum: hex.EncodeToString(h.Sum(nil))})
	}
	return sums, nil
}

func checksumsText(sums []assetSum) string {
	var b strings.Builder
	for _, s := range sums {
		fmt.Fprintf(&b, "%s  %s\n", s.sum, s.name)
	}
	return b.String()
}

var jsonEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func latestJSON(version string, sums []assetSum) string {
	var b strings.Builder
	fmt.Fprintf(&b, "{\n  \"version\": \"%s\",\n  \"checksums\": {\n", jsonEscaper.Replace(version))
	for i, s := range sums {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "    \"%s\": \"%s\"", jsonEscaper.Replace(s.name), jsonEscaper.Replace(s.sum))
	}
	b.WriteString("\n  }\n}\n")
	return b.String()
}

// quietOutput is output with the child's stderr discarded.
func quietOutput(dir, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Dir = dir
	b, err := c.Output()
	return strings.TrimSpace(string(b)), err
}
