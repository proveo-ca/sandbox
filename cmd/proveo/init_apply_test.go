// SPEC: _spec/internal/ui/step-lines.puml
package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

func applyFixture() applyCtx {
	plan := sbx.Plan{Prefix: "/opt/proveo-test-prefix", Bin: "/opt/proveo-test-prefix/bin/sbx"}
	return applyCtx{plan: plan, applied: plan}
}

func TestTheApplyPlanListsWhatEnterRunsInTheOrderItRuns(t *testing.T) {
	everything := initDecisions{Install: installTarball, AddPath: true, Login: true, Wizard: true,
		Baseline: "balanced", Git: true, Prompt: true, Gh: true}
	for _, c := range []struct {
		name string
		d    initDecisions
		ctx  applyCtx
		want []string
	}{
		{"a first install with every action now; sbx setup waits for the result", everything, applyFixture(), []string{
			"download sbx " + sbx.Release, "install sbx into /opt/proveo-test-prefix",
			"add /opt/proveo-test-prefix/bin to PATH", actLogin,
			"bind the network baseline: balanced", "install the prompt kit", "set the sandbox git identity",
			"connect GitHub", "verify sbx (sbx diagnose)"}},
		{"a revisit with everything later still verifies", initDecisions{Install: installSkip}, applyFixture(),
			[]string{"verify sbx (sbx diagnose)"}},
		{"print-only PATH is not a step", initDecisions{Install: installSkip, AddPath: false, Login: true}, applyFixture(),
			[]string{actLogin, "verify sbx (sbx diagnose)"}},
		{"a package manager owns placement, so no PATH step", initDecisions{Install: installPackaged, AddPath: true},
			applyCtx{plan: sbx.Plan{Packaged: &sbx.Packaged{Asset: "sbx.deb"}}, applied: sbx.Plan{}},
			[]string{"download the distro package", "install the distro package", "verify sbx (sbx diagnose)"}},
		{"the MSI names how it installs", initDecisions{Install: installTarball},
			applyCtx{plan: windowsPlan(t), applied: windowsPlan(t)},
			[]string{"download sbx " + sbx.Release, "install sbx with msiexec.exe", "verify sbx (sbx diagnose)"}},
	} {
		if got := stepNames(applyPlan(c.d, c.ctx)); !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// The PATH step is named only when the prefix holds sbx, or will once the tarball lands.
func TestThePathStepIsListedOnlyWhenThePrefixWillHoldSbx(t *testing.T) {
	prefix := t.TempDir()
	plan := sbx.Plan{Prefix: prefix, Bin: filepath.Join(prefix, "bin", "sbx")}
	keep := initDecisions{Install: installSkip, AddPath: true}
	if got := stepNames(applyPlan(keep, applyCtx{plan: plan, applied: plan})); slices.Contains(got, "add "+filepath.Dir(plan.Bin)+" to PATH") {
		t.Errorf("an sbx kept outside the empty prefix earned a PATH step: %q", got)
	}
	tarball := initDecisions{Install: installTarball, AddPath: true}
	if got := stepNames(applyPlan(tarball, applyCtx{plan: plan, applied: plan})); !slices.Contains(got, "add "+filepath.Dir(plan.Bin)+" to PATH") {
		t.Errorf("a tarball install into the prefix lost its PATH step: %q", got)
	}
	if err := os.MkdirAll(filepath.Dir(plan.Bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := stepNames(applyPlan(keep, applyCtx{plan: plan, applied: plan})); !slices.Contains(got, "add "+filepath.Dir(plan.Bin)+" to PATH") {
		t.Errorf("a prefix that holds sbx lost its PATH step: %q", got)
	}
}

func windowsPlan(t *testing.T) sbx.Plan {
	t.Helper()
	plan, err := sbx.PlanFor(sbx.Host{OS: "windows", Arch: "amd64"}, "", "/tmp/dl")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestEachStepShowsTheWayItsKindDoes(t *testing.T) {
	everything := initDecisions{Install: installTarball, AddPath: true, Login: true, Wizard: true,
		Baseline: "balanced", Git: true, Prompt: true, Gh: true}
	kinds := map[stepKind][]string{}
	for _, s := range applyPlan(everything, applyFixture()) {
		if s.name != "" {
			kinds[s.kind] = append(kinds[s.kind], s.name)
		}
	}
	for kind, want := range map[stepKind][]string{
		stepLive:   {"download sbx " + sbx.Release, "add /opt/proveo-test-prefix/bin to PATH"},
		stepHand:   {"install sbx into /opt/proveo-test-prefix", "bind the network baseline: balanced"},
		stepSilent: {actLogin, actPrompt, actGit, actGitHub},
		stepQuiet:  {"verify sbx (sbx diagnose)"},
	} {
		if !slices.Equal(kinds[kind], want) {
			t.Errorf("kind %d = %q, want %q", kind, kinds[kind], want)
		}
	}
}

// F1: a line that stays on screen names its program and no place; the live frames and the run
// log keep the numbers.
func TestAHandedStepPrintsItsProgramAndNoPlace(t *testing.T) {
	var term bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&term)
	boom := errors.New("exit status 1")
	var ran []string
	step := func(name, line, label string, kind stepKind, err error) applyStep {
		return applyStep{name: name, line: line, label: label, kind: kind, run: func() error { ran = append(ran, name+"!"); return err }}
	}
	var heard []string
	err := runApply([]applyStep{
		step("download sbx", "download sbx", rowInstall, stepLive, nil),
		step("", "", "", stepLive, nil),
		step("sign in to Docker", "sbx login — signing in to Docker", rowLogin, stepHand, boom),
		step("install the prompt kit", "", rowPrompt, stepSilent, nil),
		step("verify sbx", "", labelVerify, stepQuiet, nil),
	}, func(label string) { heard = append(heard, label) })
	if got := strings.Join(heard, "|"); got != rowInstall+"|" {
		t.Errorf("heard %q; a step that failed must not be reported as run", got)
	}
	var failed stepErr
	if !errors.Is(err, boom) || !errors.As(err, &failed) || failed.label != rowLogin || !slices.Equal(failed.rest, []string{rowPrompt, labelVerify}) {
		t.Errorf("err = %#v, want the failing step's, its label and what never ran", err)
	}
	if got := strings.Join(ran, " "); got != "download sbx! ! sign in to Docker!" {
		t.Errorf("ran %q; an unlisted step must run, and nothing may run after a failure", got)
	}
	out := term.String()
	for _, want := range []string{"download sbx\n", "sbx login — signing in to Docker\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "step ") || strings.Contains(out, "verify sbx") {
		t.Errorf("a line that stays carries its place, or a later step was announced:\n%s", out)
	}
}

// A quiet step leaves nothing in plain output, and a silent one nothing anywhere: verify's
// result and the git block print under their own headings.
func TestQuietAndSilentStepsLeaveNoPlainLine(t *testing.T) {
	var term bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&term)
	if err := runApply([]applyStep{
		{name: "set the sandbox git identity", label: rowGit, kind: stepSilent, run: func() error { return nil }},
		{name: "verify sbx (sbx diagnose)", label: labelVerify, kind: stepQuiet, run: func() error { return nil }},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if term.Len() != 0 {
		t.Errorf("plain output = %q, want nothing", term.String())
	}
}

// REVISIT: the PATH step a visit settled builds no step on a revisit that changed nothing,
// and runs again when the revisit now asks to write the rc.
func TestARevisitRerunsThePathStepOnlyToWriteTheRC(t *testing.T) {
	isolatedInitHost(t)
	t.Setenv("SHELL", "/bin/zsh")
	prefix := filepath.Join(t.TempDir(), "prefix")
	bin := filepath.Join(prefix, "bin", "sbx")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := sbx.Plan{Prefix: prefix, Bin: bin}
	visit := func(d initDecisions) (initDecisions, []string, string) {
		out := captureUI(t)
		rep := newInitReport(initOptions{}, sbx.Host{OS: "darwin", Arch: "arm64"}, nil, plan)
		rep.sbxSaid = true
		steps := applyPlan(d, applyCtx{plan: plan, applied: plan, rep: rep})
		if err := runApply(steps, (&initRecord{}).did); err != nil {
			t.Fatal(err)
		}
		return settle(d, steps, plan), stepNames(steps), out.String()
	}
	pathStep := "add " + filepath.Dir(bin) + " to PATH"

	d, names, got := visit(initDecisions{Install: installSkip, AddPath: true})
	if !slices.Contains(names, pathStep) || !strings.Contains(got, "added to PATH") {
		t.Fatalf("visit 1 ran %q and said %q, want the PATH step to write the rc", names, got)
	}
	if _, names, got = visit(carried(d, &d)); slices.Contains(names, pathStep) || strings.Contains(got, "PATH") {
		t.Errorf("a revisit that changed nothing ran %q and said %q, want no PATH step", names, got)
	}

	t.Setenv("HOME", t.TempDir())
	d, _, _ = visit(initDecisions{Install: installSkip})
	again := carried(d, &d)
	again.AddPath = true
	if _, names, got = visit(again); !slices.Contains(names, pathStep) || !strings.Contains(got, "added to PATH") {
		t.Errorf("a revisit that now asks to write the rc ran %q and said %q, want the PATH step", names, got)
	}
}

// THE SBX STEPS RUN ONLY WITH WORK: `sbx login` takes the terminal only while sbx diagnose says
// Docker is signed out, and a second diagnose decides its outcome.
func TestTheLoginStepSkipsWhenSbxReportsSignedIn(t *testing.T) {
	calls := isolatedInitHost(t)
	t.Setenv("STUB_DIAGNOSE", `{"checks":[{"name":"Authentication","status":"pass","message":"authenticated"}]}`)
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	if err := runApply(applyPlan(initDecisions{Install: installSkip, Login: true},
		applyCtx{plan: rep.applied, applied: rep.applied, rep: rep}), nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(calls); !strings.Contains(string(b), "diagnose --json") || regexp.MustCompile(`(?m)^login`).Match(b) {
		t.Errorf("want a diagnose and no `sbx login` on a signed-in host; sbx saw:\n%s", b)
	}
	if strings.Contains(out.String(), "sbx login") {
		t.Errorf("a signed-in host printed a login:\n%s", out.String())
	}

	for _, c := range []struct {
		name          string
		before, after sbx.Auth
		tty           bool
		logins        int
		says          string
	}{
		{"signed in", sbx.AuthIn, sbx.AuthIn, true, 0, ""},
		{"diagnose gave no answer", sbx.AuthUnknown, sbx.AuthUnknown, true, 0, ""},
		{"signed out, then in", sbx.AuthOut, sbx.AuthIn, true, 1, "ok: Docker login — signed in"},
		{"signed out, and still", sbx.AuthOut, sbx.AuthOut, true, 1, "warn: Docker login — not signed in\n    " + loginFix},
		{"signed out, with no terminal", sbx.AuthOut, sbx.AuthOut, false, 0, ""},
	} {
		out := captureUI(t)
		rep := testReport(t, initOptions{})
		logins := 0
		err := applyLogin(loginStage{
			auth:    func() sbx.Auth { return c.before },
			confirm: func() sbx.Auth { return c.after },
			login:   func() error { logins++; return nil },
			tty:     c.tty,
			rep:     rep,
		})
		if err != nil || logins != c.logins {
			t.Errorf("%s: err %v, %d logins, want %d", c.name, err, logins, c.logins)
		}
		if got := strings.TrimSpace(strings.ReplaceAll(out.String(), "------ sbx ------", "")); got != c.says {
			t.Errorf("%s: said %q, want %q", c.name, got, c.says)
		}
		if unchecked := len(rep.sbxItems) == 1 && rep.sbxItems[0].Text == "Docker login — not checked"; unchecked != (c.before == sbx.AuthUnknown) {
			t.Errorf("%s: sbx items = %+v", c.name, rep.sbxItems)
		}
		if rep.loginWarned != strings.HasPrefix(c.says, "warn:") {
			t.Errorf("%s: the report heard the login warning = %v; the result must not repeat it", c.name, rep.loginWarned)
		}
	}
}

// THE SBX STEPS RUN ONLY WITH WORK: a hand-off prints a blank row and its frame, the program's own
// output, then a blank row for the step's outcome.
func TestAHandOffIsFramed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the child is a shell")
	}
	out := captureUI(t)
	c := command("sh", "-c", "echo sbx-says-hi")
	c.Stdout, c.Stderr = out, out
	if err := handFramed(c, "Docker not signed in", "sbx's screen", "init resumes when it exits"); err != nil {
		t.Fatal(err)
	}
	want := "\n" + ui.FrameLine(0, "sh -c echo sbx-says-hi", "Docker not signed in", "sbx's screen", "init resumes when it exits") +
		"\nsbx-says-hi\n\n"
	if got := out.String(); got != want {
		t.Errorf("the hand-off printed\n%q\nwant\n%q", got, want)
	}
	if !strings.HasPrefix(ui.FrameLine(0, "sbx login", "Docker not signed in", "sbx's screen", "init resumes when it exits"), "── sbx login ── ") {
		t.Error("the frame names the program by its base name first")
	}
	if err := handFramed(exec.Command(filepath.Join(t.TempDir(), "absent"))); err == nil || interrupted(err) {
		t.Errorf("a program that never started = %v, want proveo's own error", err)
	}
}
