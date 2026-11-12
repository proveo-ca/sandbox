// SPEC: _spec/internal/ui/step-lines.puml
package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
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
		{"a first install with every action now", everything, applyFixture(), []string{
			"download sbx " + sbx.Release, "install sbx into /opt/proveo-test-prefix",
			"add /opt/proveo-test-prefix/bin to PATH",
			"sign in to Docker (sbx login)", "run sbx's first-run wizard (sbx setup)",
			"bind the network baseline: balanced", "install the prompt kit", "set the sandbox git identity",
			"connect GitHub", "verify sbx (sbx diagnose)"}},
		{"a revisit with everything later still verifies", initDecisions{Install: installSkip}, applyFixture(),
			[]string{"verify sbx (sbx diagnose)"}},
		{"print-only PATH is not a step", initDecisions{Install: installSkip, AddPath: false, Login: true}, applyFixture(),
			[]string{"sign in to Docker (sbx login)", "verify sbx (sbx diagnose)"}},
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
		stepHand:   {"install sbx into /opt/proveo-test-prefix", actSignIn, actWizard, "bind the network baseline: balanced"},
		stepSilent: {actPrompt, actGit, actGitHub},
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
		step("sign in to Docker", "sbx login — signing in to Docker", rowSignIn, stepHand, boom),
		step("install the prompt kit", "", rowPrompt, stepSilent, nil),
		step("verify sbx", "", labelVerify, stepQuiet, nil),
	}, func(label string) { heard = append(heard, label) })
	if got := strings.Join(heard, "|"); got != rowInstall+"|" {
		t.Errorf("heard %q; a step that failed must not be reported as run", got)
	}
	var failed stepErr
	if !errors.Is(err, boom) || !errors.As(err, &failed) || failed.label != rowSignIn || !slices.Equal(failed.rest, []string{rowPrompt, labelVerify}) {
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
