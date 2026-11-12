// SPEC: _spec/internal/cdn/distribution-update.puml
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/cdn"
)

const snapshotVersion = "0.1.6-SNAPSHOT-7a0c8cf4"

var (
	allGoos   = []string{"linux", "darwin", "freebsd", "windows"}
	allGoarch = []string{"amd64", "arm64"}
)

// distEntry is one dist/artifacts.json entry as goreleaser writes it.
type distEntry struct {
	Name   string            `json:"name"`
	Path   string            `json:"path"`
	Goos   string            `json:"goos,omitempty"`
	Goarch string            `json:"goarch,omitempty"`
	Type   string            `json:"type"`
	Extra  map[string]string `json:"extra"`
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func goreleaserYAML(goos, goarch []string) string {
	return fmt.Sprintf(`version: 2
project_name: proveo
builds:
  - id: proveo
    main: ./cmd/proveo
    goos: [%s]
    goarch: [%s]
  - id: proveo-entrypoint
    main: ./cmd/proveo-entrypoint
    goos: [linux, plan9]
    goarch: [amd64, arm64]
archives:
  - id: cdn-proveo
    ids: [proveo]
    formats: [binary]
    name_template: "proveo-{{ .Os }}-{{ .Arch }}"
`, strings.Join(goos, ", "), strings.Join(goarch, ", "))
}

// newDist writes a goreleaser-shaped project under a temp root: .goreleaser.yaml over goos × goarch, dist/metadata.json,
// one built binary per platform and dist/artifacts.json listing them per build, per cdn-proveo asset and per archive.
func newDist(t *testing.T, goos, goarch []string) (string, []distEntry) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".goreleaser.yaml"), goreleaserYAML(goos, goarch))
	writeFile(t, filepath.Join(root, "dist", "metadata.json"),
		`{"project_name":"proveo","tag":"v0.1.6","previous_tag":"v0.1.5","version":"`+snapshotVersion+`"}`)
	var arts []distEntry
	for _, system := range goos {
		for _, arch := range goarch {
			bin := "proveo"
			if system == "windows" {
				bin += ".exe"
			}
			path := "dist/proveo_" + system + "_" + arch + "_v1/" + bin
			body := "proveo " + system + "/" + arch + " " + snapshotVersion
			writeFile(t, filepath.Join(root, filepath.FromSlash(path)), body)
			arts = append(arts,
				distEntry{Name: bin, Path: path, Goos: system, Goarch: arch, Type: "Binary",
					Extra: map[string]string{"Binary": "proveo", "ID": "proveo"}},
				distEntry{Name: cdn.AssetName(system, arch), Path: path, Goos: system, Goarch: arch, Type: "Binary",
					Extra: map[string]string{"Binary": bin, "Checksum": "sha256:" + sha([]byte(body)), "Format": "binary", "ID": "cdn-proveo"}},
				distEntry{Name: "proveo_" + snapshotVersion + "_" + system + "_" + arch + ".tar.gz",
					Path: "dist/proveo_" + snapshotVersion + "_" + system + "_" + arch + ".tar.gz", Goos: system, Goarch: arch,
					Type: "Archive", Extra: map[string]string{"Checksum": "sha256:00", "Format": "tar.gz", "ID": "default"}})
		}
	}
	arts = append(arts, distEntry{Name: "checksums.txt", Path: "dist/checksums.txt", Type: "Checksum", Extra: map[string]string{}})
	writeArtifacts(t, root, arts)
	return root, arts
}

func writeArtifacts(t *testing.T, root string, arts []distEntry) {
	t.Helper()
	b, err := json.Marshal(arts)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "dist", "artifacts.json"), string(b))
}

// cdnEntries is the cdn-proveo subset of arts.
func cdnEntries(arts []distEntry) []distEntry {
	var out []distEntry
	for _, a := range arts {
		if a.Extra["ID"] == "cdn-proveo" {
			out = append(out, a)
		}
	}
	return out
}

func without(arts []distEntry, name string) []distEntry {
	return slices.DeleteFunc(slices.Clone(arts), func(a distEntry) bool { return a.Name == name })
}

func treePath(root string, parts ...string) string {
	return filepath.Join(append([]string{root, "apps", "cli", "public", "cli"}, parts...)...)
}

func readManifest(t *testing.T, root string) cdn.Manifest {
	t.Helper()
	b, err := os.ReadFile(treePath(root, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m cdn.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("latest.json does not decode as cdn.Manifest: %v\n%s", err, b)
	}
	return m
}

// stagedNames lists the files under the staged bin/.
func stagedNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(treePath(root, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func mustStage(t *testing.T, root string) string {
	t.Helper()
	var out bytes.Buffer
	if err := stage(root, &out); err != nil {
		t.Fatalf("stage: %v", err)
	}
	return out.String()
}

// refuses runs stage and wants an error naming each of want, with no latest.json written.
func refuses(t *testing.T, root string, want ...string) {
	t.Helper()
	err := stage(root, &bytes.Buffer{})
	if err == nil {
		t.Fatal("stage succeeded, want a refusal")
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("stage error %q does not name %q", err, w)
		}
	}
	if _, err := os.Stat(treePath(root, "latest.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused stage left latest.json behind (stat: %v)", err)
	}
}

func TestStageCopiesEveryCDNBinaryAndWritesTheManifest(t *testing.T) {
	t.Parallel()
	root, arts := newDist(t, allGoos, allGoarch)
	out := mustStage(t, root)
	if !strings.Contains(out, snapshotVersion) {
		t.Errorf("stage printed %q, want a summary naming version %s", out, snapshotVersion)
	}
	cdnArts := cdnEntries(arts)
	if len(cdnArts) != 8 {
		t.Fatalf("fixture holds %d cdn-proveo artifacts, want 8", len(cdnArts))
	}
	slices.SortFunc(cdnArts, func(a, b distEntry) int { return strings.Compare(a.Name, b.Name) })
	sums := map[string]string{}
	var want []string
	for _, a := range cdnArts {
		staged := treePath(root, "bin", a.Name)
		got, err := os.ReadFile(staged)
		if err != nil {
			t.Errorf("%s was not staged: %v", a.Name, err)
			continue
		}
		src, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(a.Path)))
		if !bytes.Equal(got, src) {
			t.Errorf("bin/%s holds %q, want the bytes of %s", a.Name, got, a.Path)
		}
		if st, _ := os.Stat(staged); runtime.GOOS != "windows" && st.Mode().Perm() != 0o755 {
			t.Errorf("bin/%s has mode %v, want 0755", a.Name, st.Mode().Perm())
		}
		sums[a.Name] = sha(got)
		want = append(want, sha(got)+"  "+a.Name)
	}
	if names := stagedNames(t, root); len(names) != len(cdnArts) {
		t.Errorf("bin/ holds %v, want the %d cdn-proveo assets alone", names, len(cdnArts))
	}
	b, err := os.ReadFile(treePath(root, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != strings.Join(want, "\n")+"\n" {
		t.Errorf("checksums.txt =\n%s\nwant sha256sum lines sorted by name:\n%s", got, strings.Join(want, "\n"))
	}
	m := readManifest(t, root)
	if m.Version != snapshotVersion {
		t.Errorf("latest.json version = %q, want %q", m.Version, snapshotVersion)
	}
	if len(m.Checksums) != len(cdnArts) {
		t.Errorf("latest.json lists %d checksums, want %d", len(m.Checksums), len(cdnArts))
	}
	for name, sum := range sums {
		if got, err := m.ChecksumFor(name); err != nil || got != sum {
			t.Errorf("latest.json ChecksumFor(%s) = %q, %v; want %s", name, got, err, sum)
		}
	}
}

// gitEnv is the environment minus GIT_* and HOME, with no global or system git config.
func gitEnv(t *testing.T) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
}

func git(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestStagedVersionIsGoreleasersVersion(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	root, _ := newDist(t, allGoos, allGoarch)
	env := gitEnv(t)
	git(t, env, root, "init", "-q")
	git(t, env, root, "add", ".goreleaser.yaml")
	git(t, env, root, "commit", "-qm", "release")
	git(t, env, root, "tag", "v9.9.9")
	if describe := git(t, env, root, "describe", "--tags", "--always"); describe != "v9.9.9" {
		t.Fatalf("fixture repo describes as %q, want v9.9.9", describe)
	}
	mustStage(t, root)
	if got := readManifest(t, root).Version; got != snapshotVersion {
		t.Errorf("latest.json version = %q, want dist/metadata.json's %q (not git describe's v9.9.9)", got, snapshotVersion)
	}
}

func TestStagePlatformsComeFromTheGoreleaserMatrix(t *testing.T) {
	t.Parallel()
	matrix := []string{"proveo-linux-arm64", "proveo-windows-arm64.exe"}
	narrow := goreleaserYAML([]string{"linux", "windows"}, []string{"arm64"})

	root, _ := newDist(t, allGoos, allGoarch)
	writeFile(t, filepath.Join(root, ".goreleaser.yaml"), narrow)
	mustStage(t, root)
	if got := stagedNames(t, root); !slices.Equal(got, matrix) {
		t.Errorf("a [linux, windows] × [arm64] matrix over a full dist/ staged %v, want %v", got, matrix)
	}
	m := readManifest(t, root)
	if got := slices.Sorted(maps.Keys(m.Checksums)); !slices.Equal(got, matrix) {
		t.Errorf("latest.json lists %v, want %v", got, matrix)
	}

	root, _ = newDist(t, []string{"linux", "windows"}, []string{"arm64"})
	mustStage(t, root)
	if got := stagedNames(t, root); !slices.Equal(got, matrix) {
		t.Errorf("a dist/ of the matrix alone staged %v, want %v", got, matrix)
	}

	root, arts := newDist(t, []string{"linux", "windows"}, []string{"arm64"})
	writeArtifacts(t, root, without(arts, "proveo-windows-arm64.exe"))
	refuses(t, root, "proveo-windows-arm64.exe")
}

func TestStageRefusesADistMissingAPlatform(t *testing.T) {
	t.Parallel()
	root, arts := newDist(t, allGoos, allGoarch)
	writeArtifacts(t, root, without(arts, "proveo-darwin-arm64"))
	refuses(t, root, "proveo-darwin-arm64", "mise run snapshot", "mise run release")
	if _, err := os.Stat(treePath(root, "bin")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused stage created bin/ (stat: %v)", err)
	}
}

func TestStageRefusesAChecksumMismatch(t *testing.T) {
	t.Parallel()
	root, arts := newDist(t, allGoos, allGoarch)
	for i, a := range arts {
		if a.Name == "proveo-linux-amd64" {
			arts[i].Extra = map[string]string{"ID": "cdn-proveo", "Checksum": "sha256:" + sha([]byte("another build"))}
		}
	}
	writeArtifacts(t, root, arts)
	refuses(t, root, "proveo-linux-amd64")

	root, _ = newDist(t, allGoos, allGoarch)
	writeFile(t, filepath.Join(root, "dist", "proveo_freebsd_arm64_v1", "proveo"), "rebuilt after goreleaser hashed it")
	refuses(t, root, "proveo-freebsd-arm64")
}

func TestStageRefusesADistWithoutMetadata(t *testing.T) {
	t.Parallel()
	for name, meta := range map[string]string{
		"missing":       "",
		"no version":    `{"project_name":"proveo","tag":"v0.1.6"}`,
		"empty version": `{"project_name":"proveo","version":" "}`,
	} {
		root, _ := newDist(t, allGoos, allGoarch)
		path := filepath.Join(root, "dist", "metadata.json")
		if meta == "" {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		} else {
			writeFile(t, path, meta)
		}
		t.Run(name, func(t *testing.T) { refuses(t, root, "mise run snapshot") })
	}
}

func TestStageRemovesStaleBinaries(t *testing.T) {
	t.Parallel()
	root, _ := newDist(t, allGoos, allGoarch)
	stale := treePath(root, "bin", "proveo-plan9-amd64")
	writeFile(t, stale, "a platform the matrix dropped")
	mustStage(t, root)
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("bin/proveo-plan9-amd64 survived a stage (stat: %v)", err)
	}
}
