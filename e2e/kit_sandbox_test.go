//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestKitSandboxNamesOnlyTheRunsOwnSandbox(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	main := filepath.Join(tmp, "main")
	home, _ := os.UserHomeDir()
	state := filepath.Join(home, ".local", "state", "proveo", "egress", "proveo-1-2", "sbx", "home-files")
	operator := filepath.Join(home, "Projects", "repo")

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
		{"the only new test sandbox is named when none holds dir", nil, []string{work},
			[]sandbox{{"proveo-claudecode-1a2b3c4d", []string{filepath.Join(tmp, "other")}}}, "proveo-claudecode-1a2b3c4d"},
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
