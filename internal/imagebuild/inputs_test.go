// SPEC: _spec/tests/stale-local-image.puml
package imagebuild_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/proveo-ca/proveo/internal/imagebuild"
	"github.com/proveo-ca/proveo/internal/ui"
)

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for rel, body := range files {
		write(t, root, rel, body, 0o644)
	}
	if out, err := exec.Command("git", "-C", root, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	return root
}

func write(t *testing.T, root, rel, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func digest(t *testing.T, root string) string {
	t.Helper()
	d, err := imagebuild.InputsDigest(root)
	if err != nil {
		t.Fatalf("InputsDigest: %v", err)
	}
	return d
}

func TestInputsDigestFollowsTheWorkingTreeUnderDefsAndPackages(t *testing.T) {
	t.Parallel()
	root := gitRepo(t, map[string]string{
		".gitignore":                  "node_modules/\n",
		"defs/base/Dockerfile":        "FROM scratch\n",
		"packages/lib/entrypoint.sh":  "echo one\n",
		"cmd/proveo/main.go":          "package main\n",
		"defs/dash/node_modules/x.js": "ignored\n",
	})
	before := digest(t, root)
	if again := digest(t, root); again != before {
		t.Fatalf("two reads of one tree disagree: %s vs %s", before, again)
	}
	for _, c := range []struct {
		name string
		edit func()
		same bool
	}{
		{"a file outside defs/ and packages/", func() { write(t, root, "cmd/proveo/main.go", "package main // edited\n", 0o644) }, true},
		{"a gitignored file", func() { write(t, root, "defs/dash/node_modules/x.js", "changed\n", 0o644) }, true},
		{"an uncommitted edit", func() { write(t, root, "packages/lib/entrypoint.sh", "echo two\n", 0o644) }, false},
		{"an untracked file", func() { write(t, root, "defs/subagents/new.md", "new\n", 0o644) }, false},
		{"an exec bit", func() { write(t, root, "defs/base/Dockerfile", "FROM scratch\n", 0o755) }, false},
		{"a deleted file", func() { _ = os.Remove(filepath.Join(root, "defs/subagents/new.md")) }, false},
	} {
		prev := digest(t, root)
		c.edit()
		if got := digest(t, root); (got == prev) != c.same {
			t.Errorf("%s: digest unchanged = %v, want %v", c.name, got == prev, c.same)
		}
	}
}

func TestInputsArgsStampOnlyAGitCheckout(t *testing.T) {
	t.Parallel()
	if args := imagebuild.InputsArgs(t.TempDir()); args != nil {
		t.Errorf("a tree outside git has no digest to stamp; got %q", args)
	}
	if args := imagebuild.InputsArgs(""); args != nil {
		t.Errorf("no repo root has no digest to stamp; got %q", args)
	}
	root := gitRepo(t, map[string]string{"defs/base/Dockerfile": "FROM scratch\n"})
	args := imagebuild.InputsArgs(root)
	if len(args) != 2 || args[0] != "--label" || args[1] != imagebuild.InputsLabel+"="+digest(t, root) {
		t.Errorf("InputsArgs = %q, want --label %s=<digest>", args, imagebuild.InputsLabel)
	}
}

func TestPinArgsResolveEveryPinTheFleetBuildPasses(t *testing.T) {
	t.Parallel()
	b := &imagebuild.Builder{
		Getenv: func(k string) string { return pinned[k] },
		Out:    io.Discard, UI: ui.New(io.Discard),
		Fetch: func(string) ([]byte, error) { t.Fatal("an overridden pin must not reach the network"); return nil, nil },
	}
	for target, want := range map[string][]string{
		"hermes": {"--build-arg", "HERMES_AGENT_VERSION=v9.9.4"},
		"codex":  {"--build-arg", "CODEX_VERSION=9.9.3"},
		"base":   nil,
	} {
		got, err := b.PinArgs(target)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("PinArgs(%s) = %q, %v; want %q", target, got, err, want)
		}
	}
	if _, err := b.PinArgs("no-such-target"); err == nil {
		t.Error("PinArgs of an unknown target must fail")
	}
}
