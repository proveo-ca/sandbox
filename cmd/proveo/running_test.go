// SPEC: _spec/cmd/proveo/usage.puml, _spec/internal/schedule/schedule.puml
package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/schedule"
	"github.com/proveo-ca/proveo/internal/ui"
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
	pickIndex = func(string, []string) (int, error) { return -1, choiceui.ErrListClosed }
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
	var out, errOut strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(""))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if got != "sample-task: running now\n" {
		t.Fatalf("the running list is one fact per task, valued as `proveo schedule` values it, with no dot of its own:\n%q", got)
	}
	if errOut.String() != "    `proveo attach <task>` opens one\n" {
		t.Errorf("with no terminal to ask on, one dim how-to goes to stderr: %q", errOut.String())
	}
}

const attachJobs = "jobs:\n  sample-task:\n    target: hermes\n    prompt_file: /tmp/p.md\n    at: [\"mon 18:50\"]\n" +
	"  idle-task:\n    target: cursor\n    prompt_file: /tmp/p.md\n"

// stubTmux puts a tmux first on PATH that logs its arguments and its TMUX, then exits with code; it
// returns the log, and makes tmux see the sessions up and a terminal on stdin.
func stubTmux(t *testing.T, code int, up ...string) (calls string) {
	t.Helper()
	bin := t.TempDir()
	calls = filepath.Join(bin, "calls")
	script := "#!/bin/sh\nprintf '%s TMUX=%s\\n' \"$*\" \"$TMUX\" >> '" + calls + "'\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	prevHas, prevTerm := tmuxHasSession, stdinTerminal
	t.Cleanup(func() { tmuxHasSession, stdinTerminal = prevHas, prevTerm })
	tmuxHasSession = func(name string) bool { return slices.Contains(up, name) }
	stdinTerminal = func() bool { return true }
	return calls
}

func TestAttachToATaskThatIsNotRunningSaysWhichWayItIsNot(t *testing.T) {
	for _, c := range []struct {
		name, body, task, want string
	}{
		{"a job not running", attachJobs, "sample-task", "error: sample-task — not running\n    no tmux session proveo-sched-sample-task\n"},
		{"a name schedule.yml lacks", attachJobs, "nosuch", "error: nosuch — no such job\n    jobs: idle-task, sample-task\n"},
		{"no schedule.yml", "", "sample-task", "error: ~/.proveo/schedule.yml — not found\n    `proveo schedule --help` shows its format\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			scheduleHome(t, c.body, schedule.Tick{})
			calls := stubTmux(t, 0)
			stdout, stderr, err := proveoOut(t, "attach", c.task)
			if code, ok := exitCode(err); !ok || code != 1 || stderr != c.want || stdout != "" {
				t.Errorf("attach %s = %v, stderr\n%s\nwant one × with one dim line, Reported:\n%s", c.task, err, stderr, c.want)
			}
			if b, _ := os.ReadFile(calls); len(b) > 0 {
				t.Errorf("tmux ran for a task that is not up: %s", b)
			}
		})
	}
}

func TestAttachToARunningSessionScheduleYmlLacksStillAttaches(t *testing.T) {
	for name, body := range map[string]string{"a job renamed mid-run": attachJobs, "no schedule.yml": ""} {
		t.Run(name, func(t *testing.T) {
			scheduleHome(t, body, schedule.Tick{})
			calls := stubTmux(t, 0, schedule.SessionName("renamed"))
			_, stderr, err := proveoOut(t, "attach", "renamed")
			b, _ := os.ReadFile(calls)
			if err != nil || stderr != "" || !strings.HasPrefix(string(b), "attach -t proveo-sched-renamed ") {
				t.Errorf("attach of a running session = %v, stderr %q, tmux %q; want tmux to take it and proveo to say nothing", err, stderr, b)
			}
		})
	}
}

func TestAFailedTmuxHandOffEndsOnStoppedAndNoCross(t *testing.T) {
	scheduleHome(t, attachJobs, schedule.Tick{})
	stubTmux(t, 1, schedule.SessionName("sample-task"))
	_, stderr, err := proveoOut(t, "attach", "sample-task")
	if code, ok := exitCode(err); !ok || code != 1 || stderr != "warn: attach — stopped at tmux\n" {
		t.Errorf("a hand-off tmux ended with 1 = %v, stderr %q; want one ⚠ after tmux's own words, exit 1", err, stderr)
	}
}

func TestAttachWithoutATerminalRefusesBeforeTmux(t *testing.T) {
	scheduleHome(t, attachJobs, schedule.Tick{})
	calls := stubTmux(t, 0, schedule.SessionName("sample-task"))
	stdinTerminal = func() bool { return false }
	_, stderr, err := proveoOut(t, "attach", "sample-task")
	if code, ok := exitCode(err); !ok || code != 1 || stderr != "error: sample-task — not attached\n    tmux needs a terminal\n" {
		t.Errorf("attach with no terminal = %v, stderr %q", err, stderr)
	}
	if b, _ := os.ReadFile(calls); len(b) > 0 {
		t.Errorf("tmux ran with no terminal: %s", b)
	}
}

func TestReadOnlyAttachInsideTmuxNamesTheKeysThatLeave(t *testing.T) {
	scheduleHome(t, attachJobs, schedule.Tick{})
	calls := stubTmux(t, 0, schedule.SessionName("sample-task"))
	t.Setenv("TMUX", "/tmp/tmux-test/default,1,0")
	_, stderr, err := proveoOut(t, "attach", "-r", "sample-task")
	if err != nil || stderr != "tmux — nested read-only client\n    the prefix twice, then d, detaches\n" {
		t.Errorf("attach -r inside tmux = %v, stderr %q; want one host item with the keys dim", err, stderr)
	}
	if b, _ := os.ReadFile(calls); string(b) != "attach -r -t proveo-sched-sample-task TMUX=\n" {
		t.Errorf("tmux = %q, want a nested read-only client with TMUX unset", b)
	}
	t.Setenv("TMUX", "")
	_, stderr, _ = proveoOut(t, "attach", "-r", "sample-task")
	if stderr != "" {
		t.Errorf("attach -r outside tmux printed %q; only a nested client names its keys", stderr)
	}
}

func TestClosingTheAttachPickerIsAWarningAndExitsOne(t *testing.T) {
	orig := pickIndex
	t.Cleanup(func() { pickIndex = orig })
	pickIndex = func(string, []string) (int, error) { return -1, choiceui.ErrListClosed }
	out := captureUI(t)
	err := attachPicked([]schedule.RunningTask{{Job: "sample-task", Summary: "● running now"}}, false)
	if code, ok := exitCode(err); !ok || code != 1 || err.Error() != "no task selected" || out.String() != "warn: attach — no task selected\n" {
		t.Errorf("closing the picker = %v, %q; want one ⚠ in the picker's words, exit 1", err, out.String())
	}
}

func TestAttachWithNothingRunningSaysSoOnStderr(t *testing.T) {
	for name, body := range map[string]string{
		"no task up":      "jobs:\n  idle-task:\n    target: cursor\n    prompt_file: /tmp/p.md\n    at: [\"tue 09:00\"]\n",
		"no schedule.yml": "",
	} {
		home := t.TempDir()
		t.Setenv("PROVEO_HOME", home)
		if body != "" {
			if err := os.WriteFile(schedule.Path(home), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		orig := tmuxHasSession
		tmuxHasSession = func(string) bool { return false }
		cmd := attachCmd()
		var out, errOut strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetIn(strings.NewReader(""))
		err := cmd.Execute()
		tmuxHasSession = orig
		if err != nil || out.String() != "" || errOut.String() != "nothing running\n" {
			t.Errorf("%s: attach = %v, stdout %q, stderr %q; want an empty stdout and the note on stderr", name, err, out.String(), errOut.String())
		}
	}
}

func TestLsPrintsOneAlignedFactPerTargetAndNoHeading(t *testing.T) {
	var b strings.Builder
	writeTargets(ui.New(&b), map[string]string{"cursor": "proveo/cursor:latest", "claudecode-browser": "proveo/claudecode-browser:latest"})
	want := "claudecode-browser: proveo/claudecode-browser:latest\ncursor:             proveo/cursor:latest\n"
	if b.String() != want {
		t.Errorf("writeTargets =\n%q\nwant\n%q", b.String(), want)
	}
	t.Setenv("PROVEO_DEFS_DIR", "")
	root := rootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetArgs([]string{"ls"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\ncursor:") || strings.Contains(out.String(), "targets") || strings.HasPrefix(out.String(), "\n") {
		t.Errorf("proveo ls writes its rows, and only its rows, to stdout:\n%s", out.String())
	}
}

func TestTheRunListingKeepsItsRunningAndTargetsHeadings(t *testing.T) {
	var b strings.Builder
	writeRunLists(&b, []schedule.RunningTask{{Job: "muse", Value: "running now"}}, map[string]string{"cursor": "proveo/cursor:latest"})
	want := "\n  ------ running ------\nmuse: running now\n\n  ------ targets ------\ncursor: proveo/cursor:latest\n"
	if b.String() != want {
		t.Errorf("writeRunLists =\n%q\nwant\n%q", b.String(), want)
	}
}

func TestProjectsPrintsOnlyThePathsAScopeTakes(t *testing.T) {
	repo := t.TempDir()
	for path, body := range map[string]string{
		"package.json":          `{"workspaces": ["apps/*"]}`,
		"apps/web/package.json": "{}",
		"apps/api/package.json": "{}",
		"apps/notes/README.md":  "not a member",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(repo)
	cmd := projectsCmd()
	var out, errOut strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "apps/api\napps/web\n" || errOut.String() != "" {
		t.Errorf("proveo projects = stdout %q, stderr %q; want the paths, one per line, and nothing else", out.String(), errOut.String())
	}

	empty := t.TempDir()
	t.Chdir(empty)
	cmd = projectsCmd()
	out.Reset()
	errOut.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "" || !strings.HasPrefix(errOut.String(), "no monorepo sub-projects under ") || strings.Contains(errOut.String(), "(") {
		t.Errorf("no members = stdout %q, stderr %q; want an empty stdout and a note that names where it looked", out.String(), errOut.String())
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
