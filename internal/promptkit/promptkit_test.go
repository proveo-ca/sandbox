// SPEC: _spec/internal/promptkit/prompt-kit.puml
package promptkit

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/shell"
)

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// starshipArchive is a release tarball holding one file named starship.
func starshipArchive(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "starship", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// fontArchive is a zip with two fonts, a licence, and one entry that tries to leave the directory.
func fontArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"FiraCodeNerdFont-Regular.ttf": "regular",
		"FiraCodeNerdFont-Bold.ttf":    "bold",
		"LICENSE":                      "OFL",
		"../../escape.ttf":             "escaped",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.Bytes()
}

// pinTo makes the pinned digests those of the test's own archives, and returns a Fetch that serves them.
func pinTo(t *testing.T, starship, font []byte) (Fetch, *[]string) {
	t.Helper()
	prevAssets, prevFont := starshipAssets, fontSHA256
	t.Cleanup(func() { starshipAssets, fontSHA256 = prevAssets, prevFont })
	starshipAssets = map[string]pinned{"linux/amd64": {"starship-x86_64-unknown-linux-musl.tar.gz", digest(starship)}}
	fontSHA256 = digest(font)
	var fetched []string
	return func(url string) ([]byte, error) {
		fetched = append(fetched, url)
		switch {
		case strings.Contains(url, "starship"):
			return starship, nil
		case strings.Contains(url, "nerd-fonts"):
			return font, nil
		}
		return nil, errors.New("unexpected URL " + url)
	}, &fetched
}

func TestThePinsNameOneAssetAndOneDigestPerHost(t *testing.T) {
	for _, host := range []string{"darwin/arm64", "darwin/amd64", "linux/arm64", "linux/amd64", "windows/arm64", "windows/amd64"} {
		goos, goarch, _ := strings.Cut(host, "/")
		asset, sum, url, ok := StarshipAsset(goos, goarch)
		if !ok || len(sum) != 64 || !strings.HasSuffix(url, "/v"+StarshipVersion+"/"+asset) || !strings.HasPrefix(url, "https://github.com/starship/starship/") {
			t.Errorf("%s: asset %q digest %q url %q ok %v", host, asset, sum, url, ok)
		}
	}
	if _, _, _, ok := StarshipAsset("plan9", "amd64"); ok {
		t.Error("a host starship does not build for got an asset")
	}
	if len(FontSHA256) != 64 || !strings.HasSuffix(FontURL(), "/"+FontRelease+"/"+FontAsset) {
		t.Errorf("font pin: %q %q", FontSHA256, FontURL())
	}
}

func TestInstallStarshipRefusesAnArchiveThatIsNotThePinnedOne(t *testing.T) {
	good := starshipArchive(t, "#!/bin/sh\necho starship\n")
	fetch, _ := pinTo(t, good, fontArchive(t))
	bin := t.TempDir()
	path, err := InstallStarship("linux", "amd64", bin, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o755 || path != filepath.Join(bin, "starship") {
		t.Errorf("installed %q mode %v, %v", path, fi, err)
	}

	tampered := starshipArchive(t, "#!/bin/sh\necho not starship\n")
	other := t.TempDir()
	_, err = InstallStarship("linux", "amd64", other, func(string) ([]byte, error) { return tampered, nil })
	if err == nil || !strings.Contains(err.Error(), "nothing was installed") {
		t.Errorf("a tampered archive installed: %v", err)
	}
	if left, _ := os.ReadDir(other); len(left) != 0 {
		t.Errorf("a refused archive left files behind: %v", left)
	}
	if _, err := InstallStarship("plan9", "amd64", other, fetch); err == nil {
		t.Error("InstallStarship accepted a host with no build")
	}
}

func TestANerdFontIsRecognisedByItsFileName(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	plain := filepath.Join(root, "plain")
	nerd := filepath.Join(root, "nerd", "deep")
	for dir, name := range map[string]string{plain: "FiraCode-Regular.ttf", nerd: "FiraCodeNerdFont-Regular.ttf"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(plain, "About Nerd Font.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if HasNerdFont([]string{empty, plain, filepath.Join(root, "missing")}) {
		t.Error("a host with no Nerd Font was read as having one")
	}
	if !HasNerdFont([]string{plain, filepath.Join(root, "nerd")}) {
		t.Error("a Nerd Font in a subdirectory was not found")
	}
	for name, want := range map[string]bool{"Fira Code Nerd Font Mono.otf": true, "JetBrainsMonoNerdFont.ttc": true, "NerdFont.zip": false} {
		if isNerdFont(name) != want {
			t.Errorf("isNerdFont(%q) = %v", name, !want)
		}
	}
}

func TestInstallFontWritesOnlyFontFilesAndOnlyInsideTheDirectory(t *testing.T) {
	fetch, _ := pinTo(t, starshipArchive(t, "x"), fontArchive(t))
	root := t.TempDir()
	dir := filepath.Join(root, "fonts")
	files, err := InstallFont(dir, fetch)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		if filepath.Dir(f) != dir {
			t.Errorf("a font landed outside the directory: %s", f)
		}
		names = append(names, filepath.Base(f))
	}
	if got := strings.Join(names, ","); got != "FiraCodeNerdFont-Bold.ttf,FiraCodeNerdFont-Regular.ttf,escape.ttf" {
		t.Errorf("fonts written: %s", got)
	}
	if left, _ := filepath.Glob(filepath.Join(root, "*.ttf")); len(left) != 0 {
		t.Errorf("an archive entry escaped the font directory: %v", left)
	}
	if !HasNerdFont([]string{dir}) {
		t.Error("the installed font is not recognised as a Nerd Font")
	}
}

func TestTheSegmentIsAddedOnceAndNothingElseChanges(t *testing.T) {
	const theirs = "# my prompt\nformat = \"$all\"\n\n[git_branch]\nsymbol = \"b \"\n"
	once := WithSegment(theirs)
	if !strings.HasPrefix(once, theirs) || strings.Count(once, "[custom.proveo]") != 1 {
		t.Fatalf("WithSegment changed the operator's config:\n%s", once)
	}
	if twice := WithSegment(once); twice != once {
		t.Errorf("a second WithSegment changed the file:\n%s", twice)
	}
	if got := WithoutSegment(once); got != theirs {
		t.Errorf("WithoutSegment = %q, want the file as it was: %q", got, theirs)
	}
	edited := strings.Replace(once, `style = "bold yellow"`, `style = "red"`, 1) + "\n[time]\ndisabled = false\n"
	repaired := WithSegment(edited)
	if !strings.Contains(repaired, `style = "bold yellow"`) || !strings.HasSuffix(repaired, "\n[time]\ndisabled = false\n") {
		t.Errorf("an edited block was not replaced in place:\n%s", repaired)
	}
	if got := WithSegment(""); got != Segment {
		t.Errorf("WithSegment of an empty config = %q", got)
	}
	if got := WithoutSegment(theirs); got != theirs {
		t.Error("WithoutSegment changed a config that holds no block")
	}
	for _, want := range []string{"when = true", "require_repo = true", `format = "[($output )]($style)"`, `command = "proveo prompt"`} {
		if !strings.Contains(Segment, want) {
			t.Errorf("the segment lacks %q", want)
		}
	}
}

// kitOptions is a kit on an empty Linux home whose shell is s.
func kitOptions(t *testing.T, s string, fetch Fetch) Options {
	t.Helper()
	home := t.TempDir()
	sh, _ := shell.Detect(s)
	return Options{
		GOOS: "linux", GOARCH: "amd64", Home: home, BinDir: filepath.Join(home, "bin"), StateDir: filepath.Join(home, "state"),
		Shell: sh, Getenv: func(string) string { return "" }, Fetch: fetch,
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		Preset:   func(_, name string) ([]byte, error) { return []byte("# preset " + name + "\n"), nil },
	}
}

func TestApplyOnAnEmptyHostInstallsEverythingAndUndoRemovesIt(t *testing.T) {
	fetch, fetched := pinTo(t, starshipArchive(t, "#!/bin/sh\n"), fontArchive(t))
	o := kitOptions(t, "/bin/zsh", fetch)
	rec, err := Apply(o)
	if err != nil {
		t.Fatal(err)
	}
	rc, config := filepath.Join(o.Home, ".zshrc"), filepath.Join(o.Home, ".config", "starship.toml")
	if rec.Starship != filepath.Join(o.BinDir, "starship") || len(rec.Fonts) != 3 || rec.RC != rc || rec.Config != config || !rec.ConfigCreated {
		t.Errorf("record = %+v", rec)
	}
	if body, _ := os.ReadFile(rc); !strings.Contains(string(body), shell.PromptMarker+"\n"+`eval "$(starship init zsh)"`) {
		t.Errorf("rc = %q", body)
	}
	body, _ := os.ReadFile(config)
	if !strings.HasPrefix(string(body), "# preset "+presetPlain+"\n") || !strings.Contains(string(body), "[custom.proveo]") {
		t.Errorf("config = %q, want the plain preset then the segment", body)
	}
	if len(*fetched) != 2 {
		t.Errorf("fetched %v, want the starship archive and the font archive", *fetched)
	}

	o.LookPath = func(string) (string, error) { return rec.Starship, nil }
	again, err := Apply(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(*fetched) != 2 {
		t.Errorf("a second Apply downloaded again: %v", *fetched)
	}
	if b, _ := os.ReadFile(rc); strings.Count(string(b), "starship init") != 1 {
		t.Errorf("a second Apply added a second init line: %q", b)
	}
	if b, _ := os.ReadFile(config); strings.Count(string(b), "[custom.proveo]") != 1 {
		t.Errorf("a second Apply added a second segment: %q", b)
	}
	if again.Starship != rec.Starship || len(again.Fonts) != 3 || again.RC != rc {
		t.Errorf("the second Apply forgot what the first installed: %+v", again)
	}

	if err := Undo(o); err != nil {
		t.Fatal(err)
	}
	for _, gone := range append([]string{rec.Starship, recordPath(o.StateDir)}, rec.Fonts...) {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Undo left %s", gone)
		}
	}
	if b, _ := os.ReadFile(config); string(b) != "# preset "+presetPlain+"\n" {
		t.Errorf("Undo left the config as %q, want the seeded preset without the segment", b)
	}
	if b, _ := os.ReadFile(rc); strings.Contains(string(b), "starship") {
		t.Errorf("Undo left the init line: %q", b)
	}
}

func TestApplyOnAHostThatHasItAllAddsOnlyTheSegment(t *testing.T) {
	fetch, fetched := pinTo(t, starshipArchive(t, "x"), fontArchive(t))
	o := kitOptions(t, "/usr/bin/fish", fetch)
	o.LookPath = func(string) (string, error) { return "/usr/local/bin/starship", nil }
	fonts := filepath.Join(o.Home, ".local", "share", "fonts")
	rc := filepath.Join(o.Home, ".config", "fish", "config.fish")
	config := filepath.Join(o.Home, ".config", "starship.toml")
	const theirRC, theirConfig = "set -gx EDITOR vim\nstarship init fish | source\n", "format = \"$directory$custom\"\n"
	for path, body := range map[string]string{filepath.Join(fonts, "HackNerdFont-Regular.ttf"): "", rc: theirRC, config: theirConfig} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	asked := false
	o.DrawsGlyphs = func() (bool, bool) { asked = true; return true, true }
	rec, err := Apply(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(*fetched) != 0 {
		t.Errorf("a host with starship and a Nerd Font downloaded %v", *fetched)
	}
	if rec.Starship != "" || len(rec.Fonts) != 0 || rec.RC != "" || rec.ConfigCreated {
		t.Errorf("record claims installs proveo did not make: %+v", rec)
	}
	if asked {
		t.Error("the glyph question was asked though no config was created")
	}
	if b, _ := os.ReadFile(rc); string(b) != theirRC {
		t.Errorf("the operator's rc changed: %q", b)
	}
	b, _ := os.ReadFile(config)
	if !strings.HasPrefix(string(b), theirConfig) || !strings.Contains(string(b), "[custom.proveo]") {
		t.Errorf("config = %q, want the operator's file plus the segment", b)
	}
	if fi, _ := os.Stat(config); fi.Mode().Perm() != 0o600 {
		t.Errorf("the config's mode changed to %v", fi.Mode().Perm())
	}

	if err := Undo(o); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(config); string(b) != theirConfig {
		t.Errorf("Undo did not restore the operator's config byte for byte: %q", b)
	}
	if b, _ := os.ReadFile(rc); string(b) != theirRC {
		t.Errorf("Undo removed a line the operator wrote: %q", b)
	}
	if _, err := os.Stat(filepath.Join(fonts, "HackNerdFont-Regular.ttf")); err != nil {
		t.Error("Undo removed a font proveo did not install")
	}
}

func TestTheGlyphAnswerPicksThePreset(t *testing.T) {
	for _, c := range []struct {
		name  string
		draws func() (bool, bool)
		want  string
	}{
		{"icons", func() (bool, bool) { return true, true }, presetIcons},
		{"an empty box", func() (bool, bool) { return false, true }, presetPlain},
		{"nobody to ask", nil, presetPlain},
		{"a question that could not be asked", func() (bool, bool) { return true, false }, presetPlain},
	} {
		fetch, _ := pinTo(t, starshipArchive(t, "x"), fontArchive(t))
		o := kitOptions(t, "/bin/bash", fetch)
		o.DrawsGlyphs = c.draws
		if _, err := Apply(o); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(o.Home, ".config", "starship.toml")); !strings.HasPrefix(string(b), "# preset "+c.want+"\n") {
			t.Errorf("%s: config starts %q, want the %s preset", c.name, strings.SplitN(string(b), "\n", 2)[0], c.want)
		}
	}
}

// Undo says each removal that succeeded as a fact and each that failed as a warning: a config it could not
// write is never "removed".
func TestUndoSaysOnlyWhatItRemoved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	fetch, _ := pinTo(t, starshipArchive(t, "#!/bin/sh\n"), fontArchive(t))
	o := kitOptions(t, "/bin/zsh", fetch)
	rec, err := Apply(o)
	if err != nil {
		t.Fatal(err)
	}
	if !Installed(o) {
		t.Fatal("Installed is false right after Apply")
	}
	configDir := filepath.Join(o.Home, ".config")
	if err := os.Chmod(configDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o755) })
	facts := map[string][]string{}
	var warned []string
	o.Fact = func(key, value string, detail ...string) { facts[key] = append([]string{value}, detail...) }
	o.Warn = func(text string, detail ...string) { warned = append(warned, text+"|"+strings.Join(detail, "|")) }
	if err := Undo(o); err == nil {
		t.Error("Undo returned no error for a config it could not write")
	}
	rc := filepath.Join(o.Home, ".zshrc")
	for key, want := range map[string][]string{
		"rc":       {rc},
		"starship": {rec.Starship},
		"font":     {FontFamily, "3 files in " + filepath.Dir(rec.Fonts[0])},
	} {
		if got := facts[key]; strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("fact %s = %q, want %q", key, got, want)
		}
	}
	if got, ok := facts["config"]; ok {
		t.Errorf("Undo said it removed the segment from a config it could not write: %q", got)
	}
	config := filepath.Join(configDir, "starship.toml")
	if len(warned) != 1 || !strings.HasPrefix(warned[0], config+" — proveo segment not removed|") || !strings.Contains(warned[0], "permission denied") {
		t.Errorf("warned %q", warned)
	}
	if err := os.Chmod(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !Installed(o) {
		t.Error("Installed is false while the segment stays in the config")
	}
	if err := Undo(o); err != nil || Installed(o) {
		t.Errorf("a second Undo = %v, Installed = %v; want the segment gone", err, Installed(o))
	}
}

// Apply says what it found as facts init prints in one block, and a shell it has no line for as a
// warning.
func TestApplySaysWhatItFoundAsFactsAndAShellWithNoLineAsAWarning(t *testing.T) {
	fetch, _ := pinTo(t, starshipArchive(t, "#!/bin/sh\n"), fontArchive(t))
	o := kitOptions(t, "/bin/zsh", fetch)
	facts := map[string][]string{}
	var warned []string
	o.Fact = func(key, value string, detail ...string) { facts[key] = append([]string{value}, detail...) }
	o.Warn = func(text string, detail ...string) { warned = append(warned, text) }
	if _, err := Apply(o); err != nil {
		t.Fatal(err)
	}
	rc, config := filepath.Join(o.Home, ".zshrc"), filepath.Join(o.Home, ".config", "starship.toml")
	for key, want := range map[string][]string{
		"starship": {StarshipVersion, "installed at " + filepath.Join(o.BinDir, "starship")},
		"rc":       {rc, "added `eval \"$(starship init zsh)\"`"},
		"config":   {config, "created from starship's " + presetPlain + " preset", "holds the proveo segment"},
	} {
		if got := facts[key]; strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("fact %s = %q, want %q", key, got, want)
		}
	}
	if got := facts["font"]; len(got) != 2 || got[0] != FontFamily || !strings.HasPrefix(got[1], "3 files in ") {
		t.Errorf("fact font = %q", got)
	}
	if len(warned) != 0 {
		t.Errorf("warned %q on a shell with a line", warned)
	}

	csh := kitOptions(t, "/bin/csh", fetch)
	csh.LookPath = func(string) (string, error) { return "/usr/local/bin/starship", nil }
	csh.Warn = func(text string, detail ...string) { warned = append(warned, text+"|"+strings.Join(detail, "|")) }
	if _, err := Apply(csh); err != nil {
		t.Fatal(err)
	}
	if len(warned) != 1 || warned[0] != "csh — no starship line known|start starship from its startup file yourself" {
		t.Errorf("a shell with no starship line warned %q", warned)
	}
}
