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
	for _, args := range [][]string{nil, {"build"}, {"deploy"}, {"ship", "all"}, {"-print=true", "build", "all", "extra"},
		{"-cli", "test", "all"}, {"-cli", "test"}} {
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

func TestFleetMainPutsTheCLIFirstWhenAsked(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args  []string
		first string
		has   []string
	}{
		{[]string{"-print", "-cli", "build"}, "# cli\ngo install -trimpath -ldflags=-s -w ./cmd/proveo ./cmd/proveo-entrypoint\n", nil},
		{[]string{"-print", "-cli", "build", "claudecode"}, "# cli\ngo install ", []string{"# serial claudecode\nimagebuild claudecode --tag local\n"}},
		{[]string{"-print", "-cli", "-cli-version=v0.3.2", "deploy"}, "# cli\n", []string{"git tag v0.3.2\n", "wrangler deploy --cwd apps/cli\n"}},
		{[]string{"-print", "-cli", "deploy", "claudecode"}, "# cli\n", []string{"git tag <patch>\n", "# serial claudecode\n", "# after every wave\n"}},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(c.args, noEnv, repoRoot, &stdout, &stderr); code != 0 {
			t.Fatalf("run(%q) = %d, want 0; stderr:\n%s", c.args, code, stderr.String())
		}
		got := stdout.String()
		if !strings.HasPrefix(got, c.first) {
			t.Errorf("run(%q) printed\n%s\nwant it to start with %q", c.args, got, c.first)
		}
		for _, want := range c.has {
			if !strings.Contains(got, want) {
				t.Errorf("run(%q) printed\n%s\nwant it to hold %q", c.args, got, want)
			}
		}
		if len(c.has) == 0 && strings.Contains(got, "# serial") {
			t.Errorf("run(%q) printed an image wave without a target:\n%s", c.args, got)
		}
	}
}

func TestFleetMainTestWithoutATargetIsTheImageGate(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"-print", "test"}, {"-print", "test", "all"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, noEnv, repoRoot, &stdout, &stderr); code != 0 {
			t.Fatalf("run(%q) = %d; stderr:\n%s", args, code, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "# image gate\ngoreleaser release --snapshot --clean\n") {
			t.Errorf("run(%q) printed\n%s\nwant the image gate", args, stdout.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-print", "test", "codex"}, noEnv, repoRoot, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "-run ^TestImageCodex$ ./internal/imagetest/") || strings.Contains(stdout.String(), "goreleaser") {
		t.Errorf("run(test codex) = %d, printed\n%s\nwant the codex suite alone", code, stdout.String())
	}
}
