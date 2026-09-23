// SPEC: _spec/internal/cdn/distribution-update.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func noGit(...string) (string, error) { return "", errors.New("no git") }

func TestResolveCDNVersionPrecedence(t *testing.T) {
	dist := t.TempDir()
	if got := resolveCDNVersion(dist, "v1.2.3", noGit); got != "1.2.3" {
		t.Errorf("PROVEO_VERSION: %q", got)
	}
	if got := resolveCDNVersion(dist, "", noGit); got != "dev" {
		t.Errorf("fallback: %q", got)
	}
	git := func(args ...string) (string, error) {
		if args[len(args)-1] == "--exact-match" {
			return "", errors.New("no tag")
		}
		return "v0.9.0-3-gabc", nil
	}
	if got := resolveCDNVersion(dist, "", git); got != "0.9.0-3-gabc" {
		t.Errorf("git describe --always: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dist, "VERSION"), []byte(" v2.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveCDNVersion(dist, "", git); got != "2.0.0" {
		t.Errorf("dist/VERSION: %q", got)
	}
	arts := `[{"name":"x"},{"extra":{"Version":""}},{"extra":{"Version":"v3.1.0"}}]`
	if err := os.WriteFile(filepath.Join(dist, "artifacts.json"), []byte(arts), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveCDNVersion(dist, "", git); got != "3.1.0" {
		t.Errorf("artifacts.json: %q", got)
	}
}

func TestLatestJSONShapeAndValidity(t *testing.T) {
	sums := []assetSum{{"proveo-darwin-arm64", "aa"}, {"proveo-linux-amd64", "bb"}}
	got := latestJSON("1.0.0", sums)
	want := "{\n  \"version\": \"1.0.0\",\n  \"checksums\": {\n" +
		"    \"proveo-darwin-arm64\": \"aa\",\n    \"proveo-linux-amd64\": \"bb\"\n  }\n}\n"
	if got != want {
		t.Fatalf("latest.json:\n%s\nwant:\n%s", got, want)
	}
	var v struct {
		Version   string            `json:"version"`
		Checksums map[string]string `json:"checksums"`
	}
	if err := json.Unmarshal([]byte(latestJSON(`a"b\c`, nil)), &v); err != nil || v.Version != `a"b\c` {
		t.Fatalf("escaping: %v %q", err, v.Version)
	}
}

func TestChecksumsTextIsSha256sumFormat(t *testing.T) {
	if got := checksumsText([]assetSum{{"proveo-a", "ff"}}); got != "ff  proveo-a\n" {
		t.Errorf("%q", got)
	}
}

func TestExtractFromDistSources(t *testing.T) {
	dist := t.TempDir()
	writeTarGz(t, filepath.Join(dist, "proveo_1.0.0_linux_amd64.tar.gz"), map[string]string{"proveo-egress": "E", "proveo": "LINUX"})
	writeZip(t, filepath.Join(dist, "proveo_1.0.0_windows_arm64.zip"), map[string]string{"sub/proveo.exe": "WIN"})
	mustWrite(t, filepath.Join(dist, "proveo-darwin-arm64"), "FLAT")
	mustWrite(t, filepath.Join(dist, "proveo_freebsd_amd64_v1", "proveo"), "RAW")

	for _, c := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "LINUX"},
		{"windows", "arm64", "WIN"},
		{"darwin", "arm64", "FLAT"},
		{"freebsd", "amd64", "RAW"},
	} {
		dest := filepath.Join(t.TempDir(), "out")
		ok, err := extractFromDist(dist, c.goos, c.goarch, dest)
		if err != nil || !ok {
			t.Fatalf("%s/%s: ok=%v err=%v", c.goos, c.goarch, ok, err)
		}
		b, _ := os.ReadFile(dest)
		st, _ := os.Stat(dest)
		if string(b) != c.want || st.Mode().Perm()&0o100 == 0 {
			t.Errorf("%s/%s: got %q mode %v", c.goos, c.goarch, b, st.Mode())
		}
	}
	if ok, err := extractFromDist(dist, "linux", "arm64", filepath.Join(t.TempDir(), "x")); ok || err != nil {
		t.Errorf("missing platform: ok=%v err=%v", ok, err)
	}
}

func TestResetCDNTreeRemovesLegacyAndStaleAssets(t *testing.T) {
	l := cdnPaths("", t.TempDir(), "")
	for _, f := range []string{"bin/proveo", "bin/help.sh", "bin/proveo-linux-amd64", "lib/ui.sh", "checksums.txt", "latest.json"} {
		mustWrite(t, filepath.Join(l.root, f), "x")
	}
	mustWrite(t, filepath.Join(l.root, "install.sh"), "keep")
	if err := resetCDNTree(l); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"bin/proveo", "bin/help.sh", "bin/proveo-linux-amd64", "lib", "checksums.txt", "latest.json"} {
		if _, err := os.Stat(filepath.Join(l.root, f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived", f)
		}
	}
	if _, err := os.Stat(filepath.Join(l.root, "install.sh")); err != nil {
		t.Error("install.sh removed")
	}
}

func TestStageCDNRequireDistFails(t *testing.T) {
	t.Setenv("PROVEO_CDN_ROOT", t.TempDir())
	t.Setenv("PROVEO_DIST_DIR", t.TempDir())
	t.Setenv("PROVEO_VERSION", "1.0.0")
	err := stageCDN(t.TempDir(), true)
	if ee, ok := errors.AsType[*exitError](err); !ok || ee.code != 1 {
		t.Fatalf("got %v, want exit 1", err)
	}
}

func TestStageCDNFromDistWritesManifest(t *testing.T) {
	cdn, dist := t.TempDir(), t.TempDir()
	for _, p := range cdnPlatforms {
		mustWrite(t, filepath.Join(dist, cdnAssetName(p[0], p[1])), p[0]+p[1])
	}
	t.Setenv("PROVEO_CDN_ROOT", cdn)
	t.Setenv("PROVEO_DIST_DIR", dist)
	t.Setenv("PROVEO_VERSION", "v4.5.6")
	if err := stageCDN(t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Version   string            `json:"version"`
		Checksums map[string]string `json:"checksums"`
	}
	b, err := os.ReadFile(filepath.Join(cdn, "latest.json"))
	if err != nil || json.Unmarshal(b, &v) != nil {
		t.Fatalf("latest.json: %v %s", err, b)
	}
	if v.Version != "4.5.6" || len(v.Checksums) != len(cdnPlatforms) || v.Checksums["proveo-windows-amd64.exe"] == "" {
		t.Errorf("latest.json: %+v", v)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []interface{ Close() error }{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
