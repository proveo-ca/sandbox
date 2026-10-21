// SPEC: _spec/cmd/proveo/usage.puml, _spec/internal/schedule/schedule.puml
package main

import (
	"os"
	"strings"
	"testing"

	fuzzyfinder "github.com/ktr0731/go-fuzzyfinder"

	"github.com/proveo-ca/proveo/internal/schedule"
)

func TestOfferReturnsThePickedRow(t *testing.T) {
	orig := pickIndex
	t.Cleanup(func() { pickIndex = orig })
	pickIndex = func(string, []string) (int, error) { return 1, nil }
	got, err := offer([]menuChoice{{Name: "a"}, {Name: "b", Attach: true}}, "run> ", "no target selected")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "b" || !got.Attach {
		t.Errorf("picked %+v", got)
	}
}

func TestOfferAbortNamesTheCommand(t *testing.T) {
	orig := pickIndex
	t.Cleanup(func() { pickIndex = orig })
	pickIndex = func(string, []string) (int, error) { return 0, fuzzyfinder.ErrAbort }
	_, err := offer([]menuChoice{{Name: "a"}}, "attach> ", "no task selected")
	if err == nil || err.Error() != "no task selected" {
		t.Fatalf("abort: %v", err)
	}
}

func TestChoiceLabelsAlignVerbAndName(t *testing.T) {
	t.Parallel()
	labels := choiceLabels([]menuChoice{
		{Verb: "attach", Name: "muse", Detail: "● running now"},
		{Verb: "run", Name: "cursor", Detail: "proveo/cursor:latest"},
	})
	if len(labels) != 2 || !strings.HasPrefix(labels[1], "run     cursor  ") {
		t.Fatalf("labels = %#v", labels)
	}
}

func TestRunChoicesPutRunningTasksBeforeHarnesses(t *testing.T) {
	t.Parallel()
	got := runChoices(
		[]schedule.RunningTask{{Job: "muse", Summary: "● running now"}},
		map[string]string{"cursor": "proveo/cursor:latest", "hermes": "proveo/hermes:latest"},
	)
	if len(got) != 3 || got[0].Name != "muse" || !got[0].Attach || got[1].Name != "cursor" || got[2].Name != "hermes" {
		t.Fatalf("%+v", got)
	}
}

func TestAliasChoicesEndWithStart(t *testing.T) {
	t.Parallel()
	got := aliasChoices("cursor", []schedule.RunningTask{{Job: "muse", Summary: "● running now"}})
	if len(got) != 2 || !got[0].Attach || got[1].Verb != "start" || got[1].Name != "cursor" || got[1].Attach {
		t.Fatalf("%+v", got)
	}
}

func TestAttachPrintsRunningTasks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PROVEO_HOME", home)
	body := "jobs:\n  sample-task:\n    target: hermes\n    prompt_file: /tmp/p.md\n    at: [\"mon 18:50\"]\n  idle-task:\n    target: cursor\n    prompt_file: /tmp/p.md\n    at: [\"tue 09:00\"]\n"
	if err := os.WriteFile(schedule.Path(home), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := tmuxHasSession
	t.Cleanup(func() { tmuxHasSession = orig })
	tmuxHasSession = func(name string) bool { return name == schedule.SessionName("sample-task") }

	cmd := attachCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(""))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "sample-task") || !strings.Contains(got, "proveo attach sample-task") || strings.Contains(got, "idle-task") {
		t.Fatalf("running list:\n%s", got)
	}
}

func TestRunWithNoTargetListsHarnesses(t *testing.T) {
	t.Setenv("PROVEO_HOME", t.TempDir())
	root := rootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"run"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"nothing running", "cursor", "proveo/cursor:latest"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}
