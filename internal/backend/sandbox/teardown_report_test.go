//go:build !windows

// SPEC: _spec/internal/backend/teardown.puml, _spec/_plans/output-readability.puml
package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// ending is what a fake sbx answers at the end of a run.
type ending struct {
	row      string // sb's `sbx ls` row; "" lists no sb
	probe    string // the clone-mode probe's shell body
	snapshot string // the snapshot's shell body
	lift     string // the output lift's shell body
	bundle   string // the bundle's shell body
	save     string // the state save's shell body
	reflog   string // the sandbox reflog's shell body
	rm       string // the removal's shell body; {gone} names the file that takes sb off the listing
	alone    bool   // sb is the only sandbox listed, so its removal leaves a header-only table
}

// endingSbx puts an sbx on PATH that answers e and logs every call; it returns the log's path.
func endingSbx(t *testing.T, e ending) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	gone := filepath.Join(bin, "gone")
	row := ""
	if e.row != "" {
		row = "[ -e '" + gone + "' ] || printf '%s\\n' '" + e.row + "'; "
	}
	if !e.alone {
		row += "printf 'bystander claude running\\n'; "
	}
	or := func(body, def string) string {
		if body == "" {
			body = def
		}
		return strings.ReplaceAll(body, "{gone}", "'"+gone+"'")
	}
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> '" + calls + "'\n" +
		"case \"$*\" in\n" +
		"  ls*) printf 'SANDBOX AGENT STATUS\\n'; " + row + ";;\n" +
		"  */run/sandbox/source*) " + or(e.probe, "echo clone") + " ;;\n" +
		"  *'git add -A'*) " + or(e.snapshot, "exit 0") + " ;;\n" +
		"  *'tar -cf -'*) " + or(e.lift, "exit 3") + " ;;\n" +
		"  *'bundle create'*) " + or(e.bundle, "exit 4") + " ;;\n" +
		"  *proveo_sync_state*) " + or(e.save, "exit 0") + " ;;\n" +
		"  *'log -g'*) " + or(e.reflog, "exit 0") + " ;;\n" +
		"  rm*) " + strings.ReplaceAll(or(e.rm, "touch {gone}"), "{gone}", "'"+gone+"'") + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, sbx.Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	orig := receiptDir
	t.Cleanup(func() { receiptDir = orig })
	receiptDir = func() string { return bin }
	return calls
}

// agentBundle is a bundle of a repository whose main holds one commit.
func agentBundle(t *testing.T) string {
	t.Helper()
	agent := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main", "."}, {"commit", "-q", "--allow-empty", "-m", "the agent's work"}} {
		gitIn(t, agent, args...)
	}
	bundle := filepath.Join(t.TempDir(), "agent.bundle")
	gitIn(t, agent, "bundle", "create", "-q", bundle, "--branches")
	return bundle
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Env = gitEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// endingTeardown is a teardown of sandbox sb, mounted at /work unless in mounts it elsewhere.
func endingTeardown(in Input, failed bool, lines ...string) *teardown {
	now := time.Now()
	cfg := sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: "/work"}}}
	if in.RepoRoot != "" {
		cfg.Mounts = []sbx.Mount{{Host: in.RepoRoot}}
	}
	in.CloneKnown = true
	return &teardown{in: in, cfg: cfg, failed: failed, started: now, ended: now,
		lastOutput: func() []string { return lines }, commitHome: func() error { return nil }}
}

func TestACloneEndingPrintsWhatCameHomeAsOneBlockThenTheRemoval(t *testing.T) {
	bundle := agentBundle(t)
	host := t.TempDir()
	gitIn(t, host, "init", "-q", "-b", "main", ".")
	endingSbx(t, ending{row: "sb claude stopped", snapshot: "echo 'proveo snapshot committed on main'", bundle: "cat '" + bundle + "'"})
	captureSteps(t)
	screen, log := captured(t)
	td := endingTeardown(Input{Clone: true, RepoRoot: host, Sid: "proveo-1-2"}, false)
	td.run(context.Background())

	got := screen.String()
	block := "branches: main (1 new commit)\n" +
		"          under refs/proveo/proveo-1-2/\n" +
		"          includes proveo's commit of uncommitted work\n"
	if !strings.Contains(got, block) {
		t.Errorf("what came home is not one block:\n%s\nwant:\n%s", got, block)
	}
	if !strings.HasSuffix(got, "ok: sb — removed\n") {
		t.Errorf("the run does not close on the sandbox's removal:\n%s", got)
	}
	if strings.Count(got, "------ results ------") != 1 || strings.Contains(got, "------ ide ------") {
		t.Errorf("results is drawn once and a removed sandbox draws no ide block:\n%s", got)
	}
	if strings.Contains(got, "proveo adopt") || strings.Contains(got, "git log --oneline") {
		t.Errorf("the review and adopt commands belong to the run log:\n%s", got)
	}
	if !strings.Contains(log.String(), "adopt: `proveo adopt proveo/proveo-1-2/main`") {
		t.Errorf("the run log lacks the adopt command:\n%s", log.String())
	}
	if strings.Index(got, block) > strings.Index(got, stepState+" — up to") {
		t.Errorf("the block prints once the carry ends, before the later steps:\n%s", got)
	}
}

func TestAPushedBranchPrintsAfterWhatTheCloneCarried(t *testing.T) {
	bundle := agentBundle(t)
	host := t.TempDir()
	gitIn(t, host, "init", "-q", "-b", "main", ".")
	endingSbx(t, ending{row: "sb claude stopped", bundle: "cat '" + bundle + "'",
		reflog: "printf 'origin/feat/x@{9999999999}\\tupdate by push\\n'"})
	captureSteps(t)
	screen, _ := captured(t)
	td := endingTeardown(Input{Clone: true, RepoRoot: host, Sid: "proveo-1-2"}, false)
	td.run(context.Background())
	got := screen.String()
	branches, pushed := strings.Index(got, "branches: main (1 new commit)"), strings.Index(got, "pushed:   origin/feat/x")
	if branches < 0 || pushed < 0 || pushed < branches {
		t.Errorf("the pushed row follows output, .env, branches and _spec, though its step ran first:\n%s", got)
	}
}

func TestABlockPrintsWhatCameHomeInOneOrderWhicheverStepFoundItFirst(t *testing.T) {
	screen, _ := captured(t)
	b := NewBlock(ResultsHeading)
	b.Fact(ui.Fact{Key: "pushed", Value: "origin/a"}, ui.Fact{Value: "origin/b"})
	b.Fact(ui.Fact{Key: "history", Value: "kept"})
	b.Fact(ui.Fact{Key: "transcript", Value: "t.jsonl"})
	b.Fact(ui.Fact{Key: ".env", Value: "merged A"})
	b.Fact(ui.Fact{Key: "branches", Value: "main (1 new commit)", Detail: []string{"under refs/proveo/s/"}}, ui.Fact{Value: "feat/x (2 new commits)"})
	b.Fact(ui.Fact{Key: "output", Value: "~/w/reports"})
	b.Print()
	var keys []string
	for _, l := range strings.Split(screen.String(), "\n") {
		if k, _, ok := strings.Cut(l, ":"); ok && !strings.HasPrefix(l, " ") {
			keys = append(keys, k)
		} else if strings.Contains(l, "origin/b") || strings.Contains(l, "feat/x") {
			keys = append(keys, "+"+strings.TrimSpace(l))
		}
	}
	want := "output .env branches +feat/x (2 new commits) pushed +origin/b transcript history"
	if strings.Join(keys, " ") != want {
		t.Errorf("rows in order %q, want %q:\n%s", strings.Join(keys, " "), want, screen.String())
	}
}

func TestARefWithNoNewCommitsStaysInTheLog(t *testing.T) {
	_, log := captured(t)
	leads := map[string]int{"proveo/s/main": 0, "proveo/s/feat/x": 2, "proveo/s/proveo/stash-0": 1}
	rows := branchFacts([]string{"proveo/s/main", "proveo/s/proveo/stash-0", "proveo/s/feat/x"}, "proveo/s/", "refs/proveo/s", "", leads)
	var got []string
	for _, r := range rows {
		got = append(got, r.Key+"|"+r.Value+"|"+strings.Join(r.Detail, ";"))
	}
	want := []string{"branches|feat/x (2 new commits)|", "|proveo/stash-0 (1 new commit)|under refs/proveo/s/"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("branch rows:\n%s\nwant the agent's branch first, then proveo's ref, and no row for a ref with no new commit:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(log.String(), "proveo/s/main holds no commit a local branch lacks") {
		t.Errorf("the run log does not name the ref it left off the screen:\n%s", log.String())
	}
	if rows := branchFacts([]string{"proveo/s/main"}, "proveo/s/", "refs/proveo/s", "main", map[string]int{}); len(rows) != 1 || rows[0].Value != "no new commits" {
		t.Errorf("no ref ahead: rows = %+v, want no new commits", rows)
	}
}

func TestAFailedRunKeepsTheSandboxWithItsFixAndTheIDEBlock(t *testing.T) {
	endingSbx(t, ending{row: "sb claude running"})
	captureSteps(t)
	screen, log := captured(t)
	td := endingTeardown(Input{}, true, "✓ sandbox sb attaching", "agent: fatal", "error: agent exited with code 3")
	td.run(context.Background())

	got := screen.String()
	kept := "warn: sb — kept for diagnosis\n    `proveo rm --stop sb` carries its work home and removes it\n"
	if !strings.Contains(got, kept) {
		t.Errorf("the kept line and its one fix:\n%s\nwant:\n%s", got, kept)
	}
	for _, want := range []string{"------ ide ------", "ssh:       sb.sbx", "           `sbx setup ssh` once", "jetbrains: File → Remote Development → SSH → sb.sbx",
		"toolbox:   jetbrains://gateway/ssh/environment?", "vs code:   code --remote ssh-remote+sb.sbx /work", "project:   /work",
		"           IDE saves write the host checkout directly"} {
		if !strings.Contains(got, want) {
			t.Errorf("the ide block lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "------ ide ------") < strings.Index(got, kept) {
		t.Errorf("the ide block follows the kept line:\n%s", got)
	}
	for _, off := range []string{"sbx exec -it", "agent: fatal", "last output", "/logs/"} {
		if strings.Contains(got, off) {
			t.Errorf("%q belongs to the run log alone:\n%s", off, got)
		}
	}
	for _, want := range []string{"last output from the agent:", "  agent: fatal", "`sbx exec -it sb bash` looks inside"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the run log lacks %q:\n%s", want, log.String())
		}
	}
	if !td.keepHome || td.kept != "the run failed" {
		t.Errorf("keepHome %v, kept %q; want the sandbox kept because the run failed", td.keepHome, td.kept)
	}
}

func TestAnAgentOnTheAlternateScreenLeavesItsLastLinesUnderTheKeptLine(t *testing.T) {
	endingSbx(t, ending{row: "sb claude stopped"})
	captureSteps(t)
	lines := []string{"one", "two", "three", "four", "five", "six", "Error: provider returned 401", "error: agent exited with code 1"}
	for _, alt := range []bool{false, true} {
		screen, _ := captured(t)
		td := endingTeardown(Input{}, true, lines...)
		td.altScreen = func() bool { return alt }
		td.run(context.Background())
		got := screen.String()
		tail := "warn: sb — kept for diagnosis\n    three\n    four\n    five\n    six\n    Error: provider returned 401\n    `proveo rm sb`"
		if alt != strings.Contains(got, tail) {
			t.Errorf("alternate screen %v: the last five lines past sbx's own error line print under the kept line only then:\n%s", alt, got)
		}
		if strings.Contains(got, "    error: agent exited") {
			t.Errorf("sbx's own error line is already on screen:\n%s", got)
		}
	}
}

func TestAFailedRunNamesTheTranscriptItsStateSaveCopiedHome(t *testing.T) {
	home := t.TempDir()
	tr := filepath.Join(home, ".claude", "projects", "p", "s.jsonl")
	endingSbx(t, ending{row: "sb claude stopped", save: "mkdir -p '" + filepath.Dir(tr) + "' && echo '{}' > '" + tr + "'"})
	captureSteps(t)
	screen, _ := captured(t)
	td := endingTeardown(Input{Target: "claudecode", HomeRoot: home}, true)
	td.cfg.Env = []string{sbx.StateHomeVar + "=" + home}
	td.started, td.ended = time.Now().Add(-time.Minute), time.Now().Add(time.Minute)
	td.run(context.Background())
	got := screen.String()
	at := strings.Index(got, "transcript: "+tr)
	if at < 0 {
		t.Fatalf("no transcript fact; the state save copies the transcript home, so the lookup follows it:\n%s", got)
	}
	if at < strings.Index(got, stepState+" — up to") || at > strings.Index(got, "warn: sb — kept for diagnosis") {
		t.Errorf("the transcript fact prints with what came home, after the state save and before the kept line:\n%s", got)
	}
}

func TestAFailedRunWhoseSandboxIsGoneClosesOnThatAndPrintsNoIDEBlock(t *testing.T) {
	endingSbx(t, ending{})
	captureSteps(t)
	screen, _ := captured(t)
	td := endingTeardown(Input{}, true, "error: agent exited with code 3")
	td.run(context.Background())
	got := screen.String()
	if !strings.HasSuffix(got, "warn: sb — not listed by sbx\n") || strings.Contains(got, "------ ide") || strings.Contains(got, "proveo rm") {
		t.Errorf("a gone sandbox gets no kept line, no fix and no ide block:\n%s", got)
	}
	if td.keepHome || td.kept != "" {
		t.Errorf("keepHome %v, kept %q for a sandbox that is gone", td.keepHome, td.kept)
	}
}

func TestARemovalThatFailedClosesOnAKeptLineWithItsError(t *testing.T) {
	endingSbx(t, ending{row: "sb claude stopped", rm: "echo 'daemon busy' >&2; exit 1"})
	captureSteps(t)
	screen, _ := captured(t)
	td := endingTeardown(Input{}, false)
	td.run(context.Background())
	got := screen.String()
	want := "warn: sb — kept: its removal failed\n    exit status 1: daemon busy\n    `proveo rm sb` carries its work home and removes it\n"
	if !strings.Contains(got, want) || !strings.Contains(got, "------ ide ------") {
		t.Errorf("a failed removal:\n%s\nwant:\n%s", got, want)
	}
	if strings.Count(got, "daemon busy") != 1 {
		t.Errorf("the removal's error printed more than once:\n%s", got)
	}
	if !td.keepHome || td.kept != "its removal failed" {
		t.Errorf("keepHome %v, kept %q", td.keepHome, td.kept)
	}
}

func TestAFailedRemovalsLogLineEndsOnWhatSbxSaid(t *testing.T) {
	for _, c := range []struct{ rm, want string }{
		{"exit 1", "sandbox removal failed (exit status 1)\n"},
		{"echo 'daemon busy' >&2; exit 1", "sandbox removal failed (exit status 1): daemon busy\n"},
	} {
		endingSbx(t, ending{row: "sb claude stopped", rm: c.rm})
		_, log := captured(t)
		if err := removeSandbox(context.Background(), "sb"); err == nil {
			t.Fatalf("%s: removeSandbox = nil", c.rm)
		}
		if got := log.String(); !strings.HasSuffix(got, c.want) {
			t.Errorf("%s: run log %q, want it to end %q", c.rm, got, c.want)
		}
	}
}

func TestARemovalThatFailedOnASandboxSbxNoLongerListsSaysSo(t *testing.T) {
	endingSbx(t, ending{row: "sb claude stopped", rm: "touch {gone}; echo 'daemon busy' >&2; exit 1"})
	captureSteps(t)
	screen, _ := captured(t)
	td := endingTeardown(Input{}, false)
	td.run(context.Background())
	got := screen.String()
	if !strings.Contains(got, "warn: sb — not listed by sbx\n    its removal failed: exit status 1: daemon busy\n") || strings.Contains(got, "------ ide") {
		t.Errorf("a removal that failed on a sandbox sbx no longer lists:\n%s", got)
	}
}

func TestAnOutputLiftThatFailsKeepsTheSandbox(t *testing.T) {
	host := t.TempDir()
	gitIn(t, host, "init", "-q", "-b", "main", ".")
	endingSbx(t, ending{row: "sb claude stopped", lift: "echo 'tar: reports: Cannot open' >&2; exit 1"})
	captureSteps(t)
	screen, log := captured(t)
	td := endingTeardown(Input{Clone: true, RepoRoot: host, Sid: "proveo-1-2", OutputDir: filepath.Join(host, "reports")}, false)
	td.run(context.Background())
	got := screen.String()
	for _, want := range []string{"warn: output — not lifted\n    exit status 1: tar: reports: Cannot open\n",
		"warn: sb — kept: the output is only in it\n    `proveo rm sb` carries its work home and removes it\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("a failed lift:\n%s\nwant:\n%s", got, want)
		}
	}
	if strings.Contains(got, "tar -C") || !strings.Contains(log.String(), "tar -C") {
		t.Errorf("the by-hand tar recipe belongs to the run log: `proveo rm` lifts again")
	}
	if td.kept != "the output is only in it" {
		t.Errorf("kept = %q", td.kept)
	}
}

func TestRetiringASandboxWhoseOutputDidNotLiftKeepsItAndNamesProveoRm(t *testing.T) {
	for _, c := range []struct{ row, fix string }{
		{"sb claude stopped", "`proveo rm sb` carries its work home and removes it"},
		{"sb claude running", "`proveo rm --stop sb` carries its work home and removes it"},
	} {
		host := t.TempDir()
		gitIn(t, host, "init", "-q", "-b", "main", ".")
		calls := endingSbx(t, ending{row: c.row, lift: "echo 'tar: reports: Cannot open' >&2; exit 1"})
		captured(t)
		in := Input{Clone: true, RepoRoot: host, Sid: "proveo-1-2", OutputDir: filepath.Join(host, "reports")}
		err := retireSandbox(in, sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: host}}}, quietReport)
		if err == nil || err.Error() != "the output is only in it — sb kept; "+c.fix {
			t.Errorf("%s: retire = %v; want the sandbox kept and the fix that lifts the output again", c.row, err)
		}
		if log := callsOf(t, calls); strings.Contains(log, "rm --force sb") {
			t.Errorf("%s: the retire removed a sandbox whose output is only in it:\n%s", c.row, log)
		}
	}
}

func TestARetiresUnsavedResumeStateReadsAsTeardownsDoes(t *testing.T) {
	home := t.TempDir()
	endingSbx(t, ending{row: "sb claude stopped", save: "echo 'vm asleep' >&2; exit 2"})
	screen, _ := captured(t)
	cfg := sbx.RunConfig{Name: "sb", Env: []string{sbx.StateHomeVar + "=" + home}}
	if err := retireSandbox(Input{}, cfg, quietReport); err != nil {
		t.Fatalf("retire = %v", err)
	}
	want := "warn: resume state — not saved\n    exit status 2: vm asleep\n"
	if got := screen.String(); !strings.Contains(got, want) || strings.Contains(got, "not preserved") {
		t.Errorf("the retire's unsaved state:\n%s\nwant teardown's item:\n%s", got, want)
	}
}

func TestACloneModeNobodyCanReadNamesTheSandboxOnceInItsKeptLine(t *testing.T) {
	endingSbx(t, ending{row: "sb claude stopped", probe: "echo 'notice: sbx update available'"})
	captureSteps(t)
	screen, _ := captured(t)
	td := endingTeardown(Input{RepoRoot: t.TempDir()}, false)
	td.in.CloneKnown = false
	td.run(context.Background())
	want := "warn: sb — kept: cannot tell whether it holds a clone, so its work may be only there: unreadable clone mode \"notice: sbx update available\"\n"
	if got := screen.String(); !strings.Contains(got, want) {
		t.Errorf("the kept line names the sandbox once:\n%s\nwant:\n%s", got, want)
	}
}

func TestTheRetainedEngineClosesOnACheckAndPrintsNoIDEBlock(t *testing.T) {
	endingSbx(t, ending{row: "sb opencode stopped"})
	captureSteps(t)
	screen, log := captured(t)
	td := endingTeardown(Input{Target: "opencode"}, false)
	td.run(context.Background())
	got := screen.String()
	if !strings.HasSuffix(got, "ok: retained engine sb — holds OpenCode's history\n") || strings.Contains(got, "------ ide") || strings.Contains(got, "sbx rm") {
		t.Errorf("the retained engine's closing line:\n%s", got)
	}
	if !strings.Contains(log.String(), "`sbx rm --force sb` deletes it and its history") {
		t.Errorf("the run log lacks the engine's removal:\n%s", log.String())
	}
}

func TestAnOperatorChosenRemovalClosesOnTheRemovalAndLogsTheForce(t *testing.T) {
	endingSbx(t, ending{row: "sb claude stopped"})
	captureSteps(t)
	screen, log := captured(t)
	td := endingTeardown(Input{}, false)
	td.forced = true
	td.run(context.Background())
	if got := screen.String(); !strings.HasSuffix(got, "ok: sb — removed\n") || strings.Contains(got, "deleting the session") {
		t.Errorf("a removal the operator chose:\n%s", got)
	}
	if !strings.Contains(log.String(), "sbx rm --force sb — deleting the session") {
		t.Errorf("the run log lacks the forced removal:\n%s", log.String())
	}
}

func TestTheAgentsEndIsTheFirstResultLineOnlyWhenNothingElseSaidIt(t *testing.T) {
	exitErr := func(code int) error {
		err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("exit %d: %v", code, err)
		}
		return ee
	}
	stopped := backend.Stopped{Signal: syscall.SIGTERM}
	desktop := errors.New("sbx setup ssh: exit status 1: no ssh")
	for _, c := range []struct {
		name     string
		result   error
		forced   bool
		sbxSaid  bool
		want     string
		mark     string
		reported bool
	}{
		{"a clean exit", nil, false, false, "", "", false},
		{"a forwarded stop is a ● host fact", stopped, false, false, "claudecode — stopped by SIGTERM", "host", false},
		{"sbx printed its own error line", exitErr(3), false, true, "", "", false},
		{"sbx said nothing", exitErr(3), false, false, "sbx run — exited with code 3", "fail", false},
		{"the operator chose removal", exitErr(1), true, false, "", "", false},
		{"the operator chose removal and sbx exited 0", context.Canceled, true, false, "", "", true},
		{"a failure that is not sbx's exit", desktop, false, false, "sbx setup ssh: exit status 1: no ssh", "fail", true},
	} {
		lines := []string{"error: agent exited with code 3"}
		end, said := agentEnd("claudecode", c.result, c.forced, c.sbxSaid, lines)
		if said != (c.want != "") || end.Text != c.want {
			t.Errorf("%s: agentEnd = %q, %v; want %q", c.name, end.Text, said, c.want)
		}
		if mark := map[bool]string{true: "host"}[end.Host] + map[bool]string{true: "fail"}[end.Fail]; mark != c.mark {
			t.Errorf("%s: mark %q, want %q", c.name, mark, c.mark)
		}
		if c.name == "a forwarded stop is a ● host fact" && (len(end.Log) != 1 || !strings.Contains(end.Log[0], "normal exit")) {
			t.Errorf("%s: the run log keeps that teardown runs as on a normal exit: %+v", c.name, end)
		}
		var rp backend.Reported
		if got := runResult(c.result, said && end.Fail, c.forced, lines); errors.As(got, &rp) != c.reported {
			t.Errorf("%s: runResult = %v; Reported %v, want %v", c.name, got, !c.reported, c.reported)
		}
	}
	var ae backend.ExitError
	if got := runResult(exitErr(1), false, false, []string{"error: agent exited with code 3"}); !errors.As(got, &ae) || ae.Code != 3 {
		t.Errorf("sbx's own exit stays the agent's code: %v", got)
	}
}

func TestTheNoCredentialHintIsOneItemWithItsRemedies(t *testing.T) {
	it := noCredentialItem([]string{
		"no credential reached the agent — the likeliest reason it exited before saying anything",
		"  ANTHROPIC_API_KEY — an API key",
		"      sbx secret set anthropic",
		"  a store entry can already exist while holding an EMPTY value: `sbx secret ls` shows",
		"  the name either way, and `sbx secret set` overwrites it",
	})
	if !it.Warn || it.Text != "no credential reached the agent" {
		t.Errorf("item = %+v", it)
	}
	if want := []string{"the likeliest reason it exited before saying anything", "ANTHROPIC_API_KEY — an API key", "sbx secret set anthropic"}; strings.Join(it.Detail, "|") != strings.Join(want, "|") {
		t.Errorf("detail = %q, want %q", it.Detail, want)
	}
	if len(it.Log) != 1 || !strings.Contains(it.Log[0], "EMPTY value") {
		t.Errorf("log = %q, want the store note", it.Log)
	}
}

func TestAKeptClonesIDEBlockIsOneFactBlockWithEveryCaution(t *testing.T) {
	screen, _ := captured(t)
	in := Input{Clone: true, RepoRoot: "/w/hotfix", Sid: "proveo-1-2", CloneSource: workspace.WorktreeSource{Main: "/w/main", Ref: "dev"}}
	PrintBlock(IDEHeading, ideFacts(in, sbx.RunConfig{Name: "sb", Mounts: []sbx.Mount{{Host: "/w/hotfix"}}}), nil)
	got := screen.String()
	for _, want := range []string{
		"------ ide ------",
		"project:   /w/hotfix\n           a local Open of this path edits the host checkout, not the clone\n",
		"           DISPOSABLE CLONE: commit IDE edits before the sandbox goes; they never reach the host checkout\n",
		"           carry IDE commits home first: `git -C /w/hotfix fetch",
		"refs/proveo/proveo-1-2/*",
		"           a linked worktree of /w/main inside the clone; `sbx ls` lists /w/main as the workspace\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the kept clone's ide block lacks %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "as the workspace") {
		t.Errorf("a kept run ends on the project fact's last line:\n%s", got)
	}
}

func TestTheFixForAKeptSandboxStopsItOnlyWhileItRuns(t *testing.T) {
	if got := keptFix("sb", true); got != "`proveo rm --stop sb` carries its work home and removes it" {
		t.Errorf("running: %q", got)
	}
	if got := keptFix("sb", false); got != "`proveo rm sb` carries its work home and removes it" {
		t.Errorf("stopped: %q", got)
	}
}

func TestABlockPrintsOnceAndARowAfterItPrintsAtOnce(t *testing.T) {
	screen, _ := captured(t)
	b := NewBlock(ResultsHeading)
	b.Fact(ui.Fact{Role: ui.RoleStore, Key: "output", Value: "~/w/reports"})
	b.Item(Item{Warn: true, Text: ".env — not merged home"})
	b.Fact(ui.Fact{Role: ui.RoleStore, Key: "branches", Value: "no new commits"})
	if screen.String() != "" {
		t.Fatalf("a block printed before Print:\n%s", screen.String())
	}
	b.Print()
	b.Print()
	b.Item(Item{Ok: true, Text: "sb — removed"})
	want := "\n  ------ results ------\noutput:   ~/w/reports\nbranches: no new commits\nwarn: .env — not merged home\nok: sb — removed\n"
	if got := screen.String(); got != want {
		t.Errorf("block:\n%q\nwant:\n%q", got, want)
	}
}

func TestAFailedCreateThatLeftASandboxSaysSoAndHowToRemoveIt(t *testing.T) {
	listing(t, true, sbx.Listed{Name: "sb", Status: "stopped"})
	it, ok := leftByCreate("sb")
	if !ok || !it.Warn || it.Text != "sb — kept: its create failed" || len(it.Detail) != 1 || it.Detail[0] != "`sbx rm --force sb` removes it" {
		t.Errorf("leftByCreate = %+v, %v", it, ok)
	}
	listing(t, true, sbx.Listed{Name: "other", Status: "running"})
	if _, ok := leftByCreate("sb"); ok {
		t.Error("a create that left no sandbox printed a kept line")
	}
	listing(t, false)
	if _, ok := leftByCreate("sb"); ok {
		t.Error("no listing proves nothing; it printed a kept line")
	}
}

// A sandbox kept because its work did not come home asks sbx first, as every kept line does.
func TestAStrandedSandboxSbxNoLongerListsClosesOnTheGoneLine(t *testing.T) {
	host := t.TempDir()
	gitIn(t, host, "init", "-q", "-b", "main", ".")
	endingSbx(t, ending{row: "sb claude stopped", lift: "touch {gone}; echo 'tar: reports: Cannot open' >&2; exit 1"})
	captureSteps(t)
	screen, _ := captured(t)
	td := endingTeardown(Input{Clone: true, RepoRoot: host, Sid: "proveo-1-2", OutputDir: filepath.Join(host, "reports")}, false)
	td.run(context.Background())
	got := screen.String()
	if !strings.HasSuffix(got, "warn: sb — not listed by sbx\n    the output is only in it\n") || strings.Contains(got, "------ ide") || strings.Contains(got, "sb — kept") {
		t.Errorf("a stranded sandbox sbx no longer lists closes on the gone line:\n%s", got)
	}
	if td.keepHome || td.kept != "" {
		t.Errorf("keepHome %v, kept %q for a sandbox that is gone", td.keepHome, td.kept)
	}
}
