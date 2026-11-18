// SPEC: _spec/tests/testing-strategy.puml
package main

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/maintain"
)

func noEnv(string) string { return "" }

func neverPick(t *testing.T) func([]string) ([]string, error) {
	return func([]string) ([]string, error) {
		t.Error("the checklist opened")
		return nil, errors.New("unexpected")
	}
}

func TestE2EMainPrintsThePassesItWouldRun(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	args := []string{"-print", "-full", "-no-images", "-run=TestHello", "cursor"}
	if code := run(args, noEnv, ".", &stdout, &stderr, neverPick(t)); code != 0 {
		t.Fatalf("run(%q) = %d; stderr:\n%s", args, code, stderr.String())
	}
	want := "# suite\nPROVEO_IDLE_TEST=1 PROVEO_TOOLCHAIN_TEST=1 go test -tags=e2e ./e2e/ -count=1 -timeout 150m -run TestHello\n" +
		"# idle\nPROVEO_IDLE_TEST=1 PROVEO_IDLE_TARGETS=cursor PROVEO_IDLE_FOR=45s go test -tags=e2e -count=1 -timeout 30m ./e2e/ -run=^TestAgentSurvivesIdleAtPrompt$ -v\n" +
		"# ladder:cursor\nPROVEO_LADDER_TEST=1 PROVEO_LADDER_TARGET=cursor go test -tags=e2e -count=1 -timeout 30m ./e2e/ -run=^TestSandboxLadder$ -v\n"
	if stdout.String() != want {
		t.Errorf("run(%q) printed\n%s\nwant\n%s", args, stdout.String(), want)
	}

	stdout.Reset()
	if code := run([]string{"-print"}, noEnv, ".", &stdout, &stderr, neverPick(t)); code != 0 || !strings.HasPrefix(stdout.String(), "# image gate\n") {
		t.Errorf("run(-print) = %d, printed\n%s\nwant the image gate first", code, stdout.String())
	}
}

func TestE2EMainRefusesAnUnknownAgentOrFlag(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"-print", "-ladder", "hermes"}, {"-nope"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, noEnv, ".", &stdout, &stderr, neverPick(t)); code != 2 || stdout.Len() != 0 {
			t.Errorf("run(%q) = %d with stdout %q, want 2 and nothing printed", args, code, stdout.String())
		}
	}
}

func TestTheChecklistOpensOnlyWhenAnAgentPassHasNoAgentNamed(t *testing.T) {
	t.Parallel()
	ticked := func(all []string) ([]string, error) { return all[1:3], nil }
	for _, c := range []struct {
		name  string
		o     maintain.E2EOptions
		print bool
		want  []string
	}{
		{"suite alone", maintain.E2EOptions{}, false, nil},
		{"named", maintain.E2EOptions{Ladder: true, Agents: []string{"codex"}}, false, []string{"codex"}},
		{"print", maintain.E2EOptions{Full: true}, true, nil},
		{"ladder, none named", maintain.E2EOptions{Ladder: true}, false, []string{"codex", "cursor"}},
		{"idle, none named", maintain.E2EOptions{Idle: true}, false, []string{"codex", "cursor"}},
	} {
		o := c.o
		opened := false
		pick := func(all []string) ([]string, error) {
			opened = true
			if !slices.Equal(all, maintain.E2EAgents) {
				t.Errorf("%s: the checklist offered %q, want %q", c.name, all, maintain.E2EAgents)
			}
			return ticked(all)
		}
		if err := resolveAgents(&o, c.print, pick); err != nil {
			t.Fatal(err)
		}
		if wantOpen := c.want != nil && len(c.o.Agents) == 0; opened != wantOpen || !slices.Equal(o.Agents, c.want) {
			t.Errorf("%s: opened=%v agents=%q, want opened=%v agents=%q", c.name, opened, o.Agents, wantOpen, c.want)
		}
	}
	o := maintain.E2EOptions{Ladder: true}
	cancel := func([]string) ([]string, error) { return nil, errors.New("cancelled: no e2e pass ran") }
	if err := resolveAgents(&o, false, cancel); err == nil {
		t.Error("a cancelled checklist ran the passes")
	}
}
