// SPEC: _spec/internal/shell/setup-path.puml
package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/shell"
)

// setupHost is isolatedHost with the running proveo in ~/.proveo/bin, off PATH, under shell sh; it returns
// that directory and the temp HOME.
func setupHost(t *testing.T, sh string) (dir, home string) {
	t.Helper()
	isolatedHost(t)
	home = os.Getenv("HOME")
	dir = filepath.Join(home, ".proveo", "bin")
	runningAs(t, filepath.Join(dir, "proveo"))
	t.Setenv("SHELL", sh)
	return dir, home
}

func setup(t *testing.T, args ...string) (stderr string, err error) {
	t.Helper()
	stdout, stderr, err := proveoOut(t, append([]string{"setup"}, args...)...)
	if stdout != "" {
		t.Errorf("setup wrote %q to stdout", stdout)
	}
	return stderr, err
}

func TestSetupAddsTheDirAndSaysANewShellPicksItUp(t *testing.T) {
	dir, home := setupHost(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	stderr, err := setup(t)
	if want := "ok: ~/.proveo/bin — added to PATH in ~/.zshrc\n    a new shell picks it up\n"; err != nil || stderr != want {
		t.Errorf("setup = %q, %v; want %q", stderr, err, want)
	}
	if b, _ := os.ReadFile(rc); string(b) != (shell.Shell{Name: "zsh"}).Block(dir) {
		t.Errorf("the rc holds %q", b)
	}

	stderr, err = setup(t)
	if want := "warn: ~/.proveo/bin — not on this shell's PATH\n    ~/.zshrc adds it; a new shell picks it up\n"; err != nil || stderr != want {
		t.Errorf("a second setup = %q, %v; want %q", stderr, err, want)
	}
	if b, _ := os.ReadFile(rc); strings.Count(string(b), shell.Marker) != 1 {
		t.Errorf("a second setup wrote a second line: %q", b)
	}
}

func TestSetupWritesThisDirsLineUnderAnotherDirsMarker(t *testing.T) {
	dir, home := setupHost(t, "/bin/bash")
	rc := shell.Shell{Name: "bash"}.RCFile(runtime.GOOS, home)
	other := shell.Shell{Name: "bash"}.Block(filepath.Join(home, "go", "bin"))
	writeFile(t, rc, other)
	if stderr, err := setup(t); err != nil || !strings.HasPrefix(stderr, "ok: ~/.proveo/bin — added to PATH in ") {
		t.Errorf("setup over another dir's marker = %q, %v", stderr, err)
	}
	if b, _ := os.ReadFile(rc); string(b) != other+(shell.Shell{Name: "bash"}).Block(dir) {
		t.Errorf("the rc holds %q, want the other dir's block, then this dir's", b)
	}
}

func TestSetupReadsAnRcLineThatNamesTheDirFromHome(t *testing.T) {
	_, home := setupHost(t, "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	hand := "export PATH=\"$HOME/.proveo/bin:$PATH\"\n"
	writeFile(t, rc, hand)
	if stderr, err := setup(t); err != nil || !strings.HasPrefix(stderr, "warn: ~/.proveo/bin — not on this shell's PATH\n") {
		t.Errorf("setup over a $HOME line for this dir = %q, %v", stderr, err)
	}
	if b, _ := os.ReadFile(rc); string(b) != hand {
		t.Errorf("setup repeated a line the rc already has: %q", b)
	}
}

func TestSetupWithTheDirOnPathWritesNothingWhateverTheShell(t *testing.T) {
	for _, sh := range []string{"/bin/zsh", "/bin/csh", ""} {
		dir, home := setupHost(t, sh)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if stderr, err := setup(t); err != nil || stderr != "ok: ~/.proveo/bin — already on PATH\n" {
			t.Errorf("SHELL=%q: setup = %q, %v", sh, stderr, err)
		}
		if entries, _ := os.ReadDir(home); len(entries) != 0 {
			t.Errorf("SHELL=%q: setup wrote into HOME: %v", sh, entries)
		}
	}
}

func TestSetupPrintNamesTheLineWithAbsolutePathsAndWritesNothing(t *testing.T) {
	dir, home := setupHost(t, "/bin/zsh")
	stderr, err := setup(t, "--print")
	want := "warn: " + dir + " — not on PATH\n    add to " + filepath.Join(home, ".zshrc") + `: export PATH="` + dir + `:$PATH"` + "\n"
	if err != nil || stderr != want {
		t.Errorf("setup --print = %q, %v; want %q", stderr, err, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Error("setup --print wrote the rc")
	}
}

func TestSetupForCshWarnsWithTheLineToAddAndExitsZero(t *testing.T) {
	dir, home := setupHost(t, "/bin/csh")
	stderr, err := setup(t)
	if want := "warn: ~/.proveo/bin — not on PATH\n    add to ~/.cshrc: setenv PATH \"" + dir + ":$PATH\"\n"; err != nil || stderr != want {
		t.Errorf("setup = %q, %v; want %q", stderr, err, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".cshrc")); !os.IsNotExist(err) {
		t.Error("setup wrote a csh rc")
	}
}

func TestSetupWithAnUnknownShellFailsOnceAndNamesTheDir(t *testing.T) {
	for sh, why := range map[string]string{"": "$SHELL is not set", "/usr/bin/nu": "nu is not a shell setup knows"} {
		setupHost(t, sh)
		stderr, err := setup(t)
		var rep backend.Reported
		want := "error: ~/.proveo/bin — not on PATH\n    " + why + "\n    add it to PATH in your shell's startup file\n"
		if !errors.As(err, &rep) || stderr != want {
			t.Errorf("SHELL=%q: setup = %q, %v; want %q, Reported", sh, stderr, err, want)
		}
	}
}

func TestSetupThatCannotWriteTheRcFailsWithTheLineToAdd(t *testing.T) {
	dir, home := setupHost(t, "/bin/zsh")
	if err := os.MkdirAll(filepath.Join(home, ".zshrc"), 0o755); err != nil {
		t.Fatal(err)
	}
	stderr, err := setup(t)
	var rep backend.Reported
	want := "error: ~/.zshrc — not written\n    open ~/.zshrc: is a directory\n    add it yourself: export PATH=\"" + dir + ":$PATH\"\n"
	if !errors.As(err, &rep) || stderr != want {
		t.Errorf("setup = %q, %v; want %q, Reported", stderr, err, want)
	}
}
