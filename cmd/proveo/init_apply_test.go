// SPEC: _spec/internal/ui/step-lines.puml
package main

import (
	"bytes"
	"errors"
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
			"bind the network baseline: balanced", "set the sandbox git identity",
			"install the prompt kit", "connect GitHub", "verify sbx (sbx diagnose)"}},
		{"a revisit with everything later still verifies", initDecisions{Install: installSkip}, applyFixture(),
			[]string{"verify sbx (sbx diagnose)"}},
		{"print-only PATH is not a step", initDecisions{Install: installSkip, AddPath: false, Login: true}, applyFixture(),
			[]string{"sign in to Docker (sbx login)", "verify sbx (sbx diagnose)"}},
		{"a package manager owns placement, so no PATH step", initDecisions{Install: installPackaged, AddPath: true},
			applyCtx{plan: sbx.Plan{Packaged: &sbx.Packaged{Asset: "sbx.deb"}}, applied: sbx.Plan{}},
			[]string{"download the distro package", "install the distro package", "verify sbx (sbx diagnose)"}},
	} {
		if got := stepNames(applyPlan(c.d, c.ctx)); !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestOnlyStepsThatKeepTheTerminalGetALiveLine(t *testing.T) {
	everything := initDecisions{Install: installTarball, AddPath: true, Login: true, Wizard: true,
		Baseline: "balanced", Git: true, Prompt: true, Gh: true}
	live := []string{}
	for _, s := range applyPlan(everything, applyFixture()) {
		if s.name != "" && !s.hands {
			live = append(live, s.name)
		}
	}
	want := []string{"download sbx " + sbx.Release, "add /opt/proveo-test-prefix/bin to PATH", "verify sbx (sbx diagnose)"}
	if !slices.Equal(live, want) {
		t.Errorf("steps behind a live line = %q, want %q: every other one gives the terminal away", live, want)
	}
}

func TestAnApplyNumbersItsListedStepsAndStopsAtTheFirstFailure(t *testing.T) {
	var term bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&term)
	boom := errors.New("sbx login: exit status 1")
	var ran []string
	step := func(name string, hands bool, err error) applyStep {
		return applyStep{name, hands, func() error { ran = append(ran, name+"!"); return err }}
	}
	var heard []string
	err := runApply([]applyStep{
		step("download sbx", false, nil),
		step("", false, nil),
		step("sign in to Docker", true, boom),
		step("verify sbx", false, nil),
	}, func(name string) { heard = append(heard, name) })
	if got := strings.Join(heard, "|"); got != "download sbx|" {
		t.Errorf("heard %q; a step that failed must not be reported as run", got)
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the failing step's", err)
	}
	if got := strings.Join(ran, " "); got != "download sbx! ! sign in to Docker!" {
		t.Errorf("ran %q; an unlisted step must run, and nothing may run after a failure", got)
	}
	out := term.String()
	for _, want := range []string{"step 1/3 · download sbx", "step 2/3 · sign in to Docker"} {
		if !strings.Contains(out, want) {
			t.Errorf("the terminal lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "verify sbx") {
		t.Errorf("a step after the failure was announced:\n%s", out)
	}
}
