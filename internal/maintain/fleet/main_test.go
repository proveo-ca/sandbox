// SPEC: _spec/internal/maintain/image-build-deploy.puml
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is the checkout root, relative to this package.
var repoRoot = filepath.Join("..", "..", "..")

func noEnv(string) string { return "" }

func TestFleetMainRefusesAMissingVerbOrTarget(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"build"}, {"ship", "all"}, {"-print=true", "build", "all", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, noEnv, repoRoot, &stdout, &stderr); code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if len(lines) != 1 || !strings.HasPrefix(lines[0], "usage: fleet ") {
			t.Errorf("run(%q) printed %q, want one usage line", args, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("run(%q) wrote %q to stdout", args, stdout.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-print=true", "build", "nope"}, noEnv, repoRoot, &stdout, &stderr); code != 1 {
		t.Errorf("run(build nope) = %d, want 1", code)
	}
	for _, want := range []string{`unknown target "nope"`, "base, base-node, base-node-browser, claudecode", "opencode-browser"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("run(build nope) stderr lacks %q:\n%s", want, stderr.String())
		}
	}
	if stdout.Len() != 0 {
		t.Errorf("run(build nope) printed a plan: %q", stdout.String())
	}
}

func TestFleetMainTakesTheArgvMiseRenders(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	args := []string{"-tag=rc1", "-no-cache", "-print", "build", "claudecode"}
	if code := run(args, noEnv, repoRoot, &stdout, &stderr); code != 0 {
		t.Fatalf("run(%q) = %d, want 0; stderr:\n%s", args, code, stderr.String())
	}
	want := "# serial claudecode\nimagebuild claudecode --tag rc1 --no-cache\ndocker image inspect proveo/claudecode:rc1\n"
	if stdout.String() != want {
		t.Errorf("run(%q) printed\n%s\nwant\n%s", args, stdout.String(), want)
	}
}
