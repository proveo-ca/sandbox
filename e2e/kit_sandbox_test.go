//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestKitSandboxNamesOnlyTheRunsOwnSandbox(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	main := filepath.Join(tmp, "main")
	state := "/operator/.local/state/proveo/egress/proveo-1-2/sbx/home-files"
	operator := "/operator/Projects/repo"

	type sandbox struct {
		Name       string   `json:"name"`
		Workspaces []string `json:"workspaces"`
	}
	cases := []struct {
		name   string
		before []string
		dirs   []string
		listed []sandbox
		want   string
	}{
		{"a home-files workspace outside temp does not disown it", nil, []string{work},
			[]sandbox{{"proveo-claudecode-1a2b3c4d", []string{work, state + ":ro"}}}, "proveo-claudecode-1a2b3c4d"},
		{"a read-only primary workspace still holds dir", nil, []string{work},
			[]sandbox{{"proveo-claudecode-1a2b3c4d", []string{work + ":ro"}}}, "proveo-claudecode-1a2b3c4d"},
		{"an operator's sandbox is never named", nil, []string{work},
			[]sandbox{{"proveo-claudecode-9f9f9f9f", []string{operator, work}}}, ""},
		{"a sandbox on the temp root itself is never named", nil, []string{work},
			[]sandbox{{"proveo-claudecode-9f9f9f9f", []string{os.TempDir()}}}, ""},
		{"a sandbox that existed before the run is never named", []string{"proveo-claudecode-1a2b3c4d"}, []string{work},
			[]sandbox{{"proveo-claudecode-1a2b3c4d", []string{work}}}, ""},
		{"another target's sandbox is never named", nil, []string{work},
			[]sandbox{{"proveo-cecli-1a2b3c4d", []string{work}}}, ""},
		{"a linked worktree's clone is found through its main worktree", nil, []string{work, main},
			[]sandbox{
				{"proveo-claudecode-0000aaaa", []string{filepath.Join(tmp, "other")}},
				{"proveo-claudecode-1a2b3c4d", []string{main}},
			}, "proveo-claudecode-1a2b3c4d"},
		{"a new test sandbox that holds no dir of this run is never named", nil, []string{work},
			[]sandbox{{"proveo-claudecode-1a2b3c4d", []string{filepath.Join(tmp, "other")}}}, ""},
		{"a scoped --input under the sandbox's workspace is held", nil, []string{filepath.Join(work, "apps")},
			[]sandbox{{"proveo-claudecode-1a2b3c4d", []string{work}}}, "proveo-claudecode-1a2b3c4d"},
		{"two new test sandboxes, neither holding dir, name none", nil, []string{work},
			[]sandbox{
				{"proveo-claudecode-0000aaaa", []string{filepath.Join(tmp, "a")}},
				{"proveo-claudecode-1a2b3c4d", []string{filepath.Join(tmp, "b")}},
			}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			listing, err := json.Marshal(map[string][]sandbox{"sandboxes": c.listed})
			if err != nil {
				t.Fatal(err)
			}
			before := map[string]bool{}
			for _, n := range c.before {
				before[n] = true
			}
			if got := pickKitSandbox(listing, before, "claudecode", c.dirs); got != c.want {
				t.Errorf("pickKitSandbox(%s) = %q, want %q", listing, got, c.want)
			}
		})
	}
}

func TestKitDirsAddsOnlyALinkedWorktreesMain(t *testing.T) {
	t.Parallel()
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	base := t.TempDir()
	main := filepath.Join(base, "main")
	if err := os.MkdirAll(filepath.Join(main, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(main, "init", "-q", ".")
	git(main, "commit", "-q", "--allow-empty", "-m", "seed")
	wt := filepath.Join(base, "wt")
	git(main, "worktree", "add", "-q", wt)

	resolved := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	if got := kitDirs(wt); !slices.ContainsFunc(got, func(d string) bool { return resolved(d) == resolved(main) }) {
		t.Errorf("kitDirs(%s) = %v, want it to name the main worktree %s its clone mounts", wt, got, main)
	}
	for _, dir := range []string{main, filepath.Join(main, "apps")} {
		for _, d := range kitDirs(dir) {
			if resolved(d) != resolved(dir) {
				t.Errorf("kitDirs(%s) added %s; only a linked worktree's main belongs there — a checkout root would hold every test sandbox under it", dir, d)
			}
		}
	}
}

func TestAnApprovalIsAnsweredOnlyWhileItWaits(t *testing.T) {
	t.Parallel()
	prompt := "sbx wants to allow 3 hosts\n[A]pprove all · [R]eview each · [N]o\n"
	for _, c := range []struct {
		name   string
		screen string
		last   string
		want   bool
	}{
		{"waiting at the bottom", prompt + "\n\n\n", "", true},
		{"the screen already answered", prompt, prompt, false},
		{"answered and scrolled past", prompt + "approved\nstarting agent\nseeding\nready\n", "", false},
		{"no question", "agent: ready\n", "", false},
	} {
		if got := awaitingApproval(c.screen, c.last); got != c.want {
			t.Errorf("awaitingApproval(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
