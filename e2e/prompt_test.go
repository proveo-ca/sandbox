//go:build e2e && !windows

// SPEC: _spec/internal/results/result-store.puml

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/results"
)

// TestThePromptCommandStartsNoProcess runs `proveo prompt` with sbx, git and docker
// replaced by scripts that log every call.
func TestThePromptCommandStartsNoProcess(t *testing.T) {
	proveoBin := buildProveo(t)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	for _, tool := range []string{"sbx", "git", "docker", "tmux", "starship"} {
		writeFile(t, filepath.Join(bin, tool), []byte("#!/bin/sh\necho \""+tool+" $*\" >> '"+calls+"'\n"))
		if err := os.Chmod(filepath.Join(bin, tool), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	state := t.TempDir()
	repo := results.Canonical(t.TempDir())
	mkdirAll(t, filepath.Join(repo, ".git"))
	mkdirAll(t, filepath.Join(repo, "apps", "web"))
	dir := results.Dir(state)
	for _, e := range []results.Entry{
		{Kind: results.Kept, Key: "proveo-claudecode-abc", Repo: repo},
		{Kind: results.Carried, Key: "proveo-1-2", Repo: repo, Refs: []results.Ref{{Name: "proveo/proveo-1-2/feat", Lead: 2}, {Name: "proveo/proveo-1-2/fix", Lead: 1}}},
	} {
		if err := results.Put(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	run := func(wd string) (string, time.Duration) {
		t.Helper()
		cmd := exec.Command(proveoBin, "prompt")
		cmd.Dir = wd
		cmd.Env = []string{"PATH=" + bin, "HOME=" + t.TempDir(), "PROVEO_STATE_ROOT=" + state}
		start := time.Now()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("proveo prompt: %v\n%s", err, out)
		}
		return string(out), time.Since(start)
	}
	out, took := run(filepath.Join(repo, "apps", "web"))
	if out != "1 kept · 2 to adopt\n" {
		t.Errorf("proveo prompt printed %q, want one line", out)
	}
	if took > 2*time.Second {
		t.Errorf("proveo prompt took %s; a shell runs it before every prompt", took)
	}
	if out, _ := run(t.TempDir()); out != "" {
		t.Errorf("proveo prompt outside a repository printed %q, want nothing", out)
	}
	if b, err := os.ReadFile(calls); err == nil && len(b) > 0 {
		t.Errorf("proveo prompt started a process:\n%s", strings.TrimSpace(string(b)))
	}
}
