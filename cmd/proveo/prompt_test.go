// SPEC: _spec/internal/results/result-store.puml
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/results"
)

func TestTheLeftLineCountsWhatThisRepositoryWasLeft(t *testing.T) {
	state := t.TempDir()
	repo := results.Canonical(t.TempDir())
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "apps", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := results.Dir(state)
	for _, e := range []results.Entry{
		{Kind: results.Kept, Key: "proveo-claudecode-abc", Repo: repo},
		{Kind: results.Carried, Key: "proveo-1-2", Repo: repo, Refs: []results.Ref{{Name: "proveo/proveo-1-2/feat", Lead: 2}}},
		{Kind: results.Kept, Key: "proveo-hermes-def", Repo: results.Canonical(t.TempDir())},
	} {
		if err := results.Put(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := leftLine(state, sub), "1 kept · 1 to adopt"; got != want {
		t.Errorf("leftLine in the repository = %q, want %q", got, want)
	}
	if got := leftLine(state, t.TempDir()); got != "" {
		t.Errorf("leftLine outside a repository = %q, want nothing", got)
	}
	if got := leftLine(t.TempDir(), sub); got != "" {
		t.Errorf("leftLine with an empty store = %q, want nothing", got)
	}
}

func TestThePromptCommandIsHiddenAndPrintsOneLine(t *testing.T) {
	state := t.TempDir()
	repo := results.Canonical(t.TempDir())
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := results.Put(results.Dir(state), results.Entry{Kind: results.Kept, Key: "proveo-claudecode-abc", Repo: repo}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVEO_STATE_ROOT", state)
	t.Chdir(repo)
	cmd := promptCmd()
	if !cmd.Hidden {
		t.Error("the prompt command shows in help; it is a shell's interface, not an operator's")
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "1 kept\n" {
		t.Errorf("output = %q, want one line", got)
	}
	found := false
	for _, c := range rootCmd().Commands() {
		found = found || strings.HasPrefix(c.Use, "prompt")
	}
	if !found {
		t.Error("the root command does not carry prompt")
	}
}
