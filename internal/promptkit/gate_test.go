// SPEC: _spec/internal/promptkit/prompt-kit.puml
package promptkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/shell"
)

// TestEachShellParsesTheBlockProveoAppends runs every shell that is installed over its own snippet.
func TestEachShellParsesTheBlockProveoAppends(t *testing.T) {
	ran := 0
	for name, check := range map[string][]string{
		"bash": {"-n"},
		"zsh":  {"-n"},
		"fish": {"--no-execute"},
	} {
		bin, err := exec.LookPath(name)
		if err != nil {
			t.Logf("%s is not installed here; its snippet is not checked", name)
			continue
		}
		sh, _ := shell.Detect(bin)
		rc := filepath.Join(t.TempDir(), "rc")
		if err := os.WriteFile(rc, []byte("# the operator's own lines\n"+sh.PromptBlock()), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bin, append(check, rc)...).CombinedOutput(); err != nil {
			t.Errorf("%s refuses the block proveo appends: %v\n%s", name, err, out)
		}
		ran++
	}
	if ran == 0 {
		t.Skip("no bash, zsh or fish on this host")
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]|\\\[|\\\]|%\{|%\}`)

// TestStarshipDrawsTheSegmentInARepositoryAndNothingOutsideOne runs the real starship over proveo's block.
func TestStarshipDrawsTheSegmentInARepositoryAndNothingOutsideOne(t *testing.T) {
	starship, err := exec.LookPath("starship")
	if err != nil {
		t.Skip("starship is not installed here")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed here")
	}
	config := filepath.Join(t.TempDir(), "starship.toml")
	if err := os.WriteFile(config, []byte(WithSegment("")), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	said := filepath.Join(bin, "said")
	fake := "#!/bin/sh\n[ \"$1\" = prompt ] && cat '" + said + "' 2>/dev/null\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "proveo"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	repo, outside := t.TempDir(), t.TempDir()
	if out, err := exec.Command(git, "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	module := func(dir string) string {
		t.Helper()
		cmd := exec.Command(starship, "module", "custom.proveo")
		cmd.Dir = dir
		cmd.Env = []string{"STARSHIP_CONFIG=" + config, "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"HOME=" + t.TempDir(), "TERM=xterm-256color", "STARSHIP_CACHE=" + t.TempDir()}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("starship module custom.proveo: %v", err)
		}
		return strings.TrimSpace(ansi.ReplaceAllString(string(out), ""))
	}
	if err := os.WriteFile(said, []byte("1 kept · 2 to adopt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := module(repo); got != "1 kept · 2 to adopt" {
		t.Errorf("in a repository starship drew %q, want the segment", got)
	}
	if got := module(outside); got != "" {
		t.Errorf("outside a repository starship drew %q, want nothing", got)
	}
	if err := os.WriteFile(said, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := module(repo); got != "" {
		t.Errorf("with nothing left, starship drew %q, want nothing", got)
	}
}
