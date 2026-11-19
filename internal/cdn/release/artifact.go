// SPEC: _spec/internal/cdn/distribution-update.puml
package release

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

	"github.com/proveo-ca/proveo/internal/cdn"
	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	publicDir     = "apps/cli/public"
	artifactCache = ".cache/cli-release"
)

func (r Repo) artifact(tag, head string, st Steps, pr *ui.Printer) (string, cdn.Manifest, error) {
	assets, err := filepath.Abs(filepath.Join(r.Root, artifactCache, tag, head))
	if err != nil {
		return "", cdn.Manifest{}, err
	}
	if _, err := os.Stat(assets); err == nil {
		manifest, err := readArtifact(assets, tag)
		if err != nil {
			return "", cdn.Manifest{}, fmt.Errorf("check saved artifact %s: %w", assets, err)
		}
		pr.Notef("reusing the staged %s artifact for %.12s", tag, head)
		return assets, manifest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", cdn.Manifest{}, err
	}

	parent := filepath.Dir(assets)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", cdn.Manifest{}, err
	}
	temp, err := os.MkdirTemp(parent, ".stage-")
	if err != nil {
		return "", cdn.Manifest{}, err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if err := r.copyPublicFiles(head, temp); err != nil {
		return "", cdn.Manifest{}, err
	}
	pr.Appf("building %s from %.12s", tag, head)
	if err := st.Build(tag); err != nil {
		return "", cdn.Manifest{}, err
	}
	current, err := r.git("rev-parse", "HEAD")
	if err != nil {
		return "", cdn.Manifest{}, err
	}
	if current != head {
		return "", cdn.Manifest{}, fmt.Errorf("HEAD moved during the build; retry from %.12s", head)
	}
	if err := st.Stage(filepath.Join(temp, "cli")); err != nil {
		return "", cdn.Manifest{}, err
	}
	manifest, err := readArtifact(temp, tag)
	if err != nil {
		return "", cdn.Manifest{}, err
	}
	if err := os.Rename(temp, assets); err != nil {
		return "", cdn.Manifest{}, err
	}
	return assets, manifest, nil
}

func (r Repo) copyPublicFiles(head, dst string) error {
	files, err := r.raw("ls-tree", "-r", "-z", "--name-only", head, "--", publicDir)
	if err != nil {
		return err
	}
	for file := range strings.SplitSeq(strings.TrimSuffix(files, "\x00"), "\x00") {
		if file == "" {
			continue
		}
		body, err := r.raw("show", head+":"+file)
		if err != nil {
			return err
		}
		path := filepath.Join(dst, filepath.FromSlash(strings.TrimPrefix(file, publicDir+"/")))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func readArtifact(assets, tag string) (cdn.Manifest, error) {
	cli := filepath.Join(assets, "cli")
	body, err := os.ReadFile(filepath.Join(cli, "latest.json"))
	if err != nil {
		return cdn.Manifest{}, err
	}
	var m cdn.Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return cdn.Manifest{}, err
	}
	if "v"+m.Version != tag || len(m.Checksums) == 0 {
		return cdn.Manifest{}, fmt.Errorf("latest.json must describe binaries for %s", tag)
	}
	var lines []string
	for asset, want := range m.Checksums {
		if filepath.Base(asset) != asset || !strings.HasPrefix(asset, "proveo-") {
			return cdn.Manifest{}, fmt.Errorf("invalid CLI asset %q", asset)
		}
		if err := checkSum(filepath.Join(cli, "bin", asset), want); err != nil {
			return cdn.Manifest{}, err
		}
		lines = append(lines, want+"  "+asset)
	}
	index, err := os.ReadFile(filepath.Join(cli, "checksums.txt"))
	if err != nil {
		return cdn.Manifest{}, err
	}
	slices.Sort(lines)
	got := strings.Split(strings.TrimSuffix(string(index), "\n"), "\n")
	slices.Sort(got)
	if !slices.Equal(lines, got) {
		return cdn.Manifest{}, errors.New("checksums.txt differs from latest.json")
	}
	return m, nil
}

func checkSum(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != want {
		return fmt.Errorf("%s differs from the checksum in latest.json", path)
	}
	return nil
}
