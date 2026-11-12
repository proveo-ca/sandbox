// SPEC: _spec/internal/cdn/distribution-update.puml
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/proveo-ca/proveo/internal/cdn"
	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	cdnTree    = "apps/cli/public/cli"
	cdnArchive = "cdn-proveo"
	rebuild    = "rebuild dist/ with `mise run snapshot` (`mise run release` on a tag)"
)

func main() {
	if err := stage(".", os.Stdout); err != nil {
		ui.Failf("stage-cdn: %v", err)
		os.Exit(1)
	}
}

// artifact is one dist/artifacts.json entry.
type artifact struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Type  string `json:"type"`
	Extra struct {
		ID       string `json:"ID"`
		Checksum string `json:"Checksum"`
	} `json:"extra"`
}

// stage copies goreleaser's cdn-proveo binaries from root/dist into the CDN tree, then writes checksums.txt and latest.json.
func stage(root string, out io.Writer) error {
	required, err := requiredAssets(root)
	if err != nil {
		return err
	}
	version, err := distVersion(root)
	if err != nil {
		return err
	}
	arts, err := cdnArtifacts(root)
	if err != nil {
		return err
	}
	sums := map[string]string{}
	for _, asset := range required {
		a, ok := arts[asset]
		if !ok {
			return fmt.Errorf("dist/artifacts.json has no %s binary %s: %s", cdnArchive, asset, rebuild)
		}
		want, _ := strings.CutPrefix(a.Extra.Checksum, "sha256:")
		got, err := fileSHA256(sourcePath(root, a.Path))
		if err != nil {
			return fmt.Errorf("%s: %w: %s", asset, err, rebuild)
		}
		if got != strings.ToLower(want) {
			return fmt.Errorf("%s: %s hashes to sha256:%s, but dist/artifacts.json records %q: %s",
				asset, a.Path, got, a.Extra.Checksum, rebuild)
		}
		sums[asset] = got
	}

	tree := filepath.Join(root, filepath.FromSlash(cdnTree))
	bin := filepath.Join(tree, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	stale, err := filepath.Glob(filepath.Join(bin, "proveo-*"))
	if err != nil {
		return err
	}
	for _, f := range stale {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	var lines strings.Builder
	for _, asset := range required {
		if err := copyExecutable(sourcePath(root, arts[asset].Path), filepath.Join(bin, asset)); err != nil {
			return err
		}
		fmt.Fprintf(&lines, "%s  %s\n", sums[asset], asset)
	}
	if err := os.WriteFile(filepath.Join(tree, "checksums.txt"), []byte(lines.String()), 0o644); err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(cdn.Manifest{Version: version, Checksums: sums}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tree, "latest.json"), append(manifest, '\n'), 0o644); err != nil {
		return err
	}
	ui.New(out).Storef("%s: %d proveo binaries, checksums.txt and latest.json (version %s)", cdnTree, len(required), version)
	return nil
}

// requiredAssets is cdn.AssetName over goos × goarch of the .goreleaser.yaml build with id proveo, sorted.
func requiredAssets(root string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Builds []struct {
			ID     string   `yaml:"id"`
			Goos   []string `yaml:"goos"`
			Goarch []string `yaml:"goarch"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf(".goreleaser.yaml: %w", err)
	}
	for _, b := range cfg.Builds {
		if b.ID != "proveo" {
			continue
		}
		var assets []string
		for _, goos := range b.Goos {
			for _, goarch := range b.Goarch {
				assets = append(assets, cdn.AssetName(goos, goarch))
			}
		}
		if len(assets) == 0 {
			return nil, errors.New(".goreleaser.yaml: build proveo names no goos or goarch")
		}
		slices.Sort(assets)
		return slices.Compact(assets), nil
	}
	return nil, errors.New(".goreleaser.yaml holds no build with id proveo")
}

// distVersion is dist/metadata.json's version, the one goreleaser stamped into the binaries.
func distVersion(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "dist", "metadata.json"))
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, rebuild)
	}
	var meta struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", fmt.Errorf("dist/metadata.json: %w: %s", err, rebuild)
	}
	if v := strings.TrimSpace(meta.Version); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("dist/metadata.json names no version: %s", rebuild)
}

// cdnArtifacts maps each cdn-proveo Binary entry of dist/artifacts.json by its asset name.
func cdnArtifacts(root string) (map[string]artifact, error) {
	raw, err := os.ReadFile(filepath.Join(root, "dist", "artifacts.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, rebuild)
	}
	var all []artifact
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("dist/artifacts.json: %w: %s", err, rebuild)
	}
	out := map[string]artifact{}
	for _, a := range all {
		if a.Type == "Binary" && a.Extra.ID == cdnArchive {
			out[a.Name] = a
		}
	}
	return out, nil
}

// sourcePath resolves an artifacts.json path, which goreleaser writes relative to the project root.
func sourcePath(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, filepath.FromSlash(p))
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyExecutable(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dest, 0o755)
}
