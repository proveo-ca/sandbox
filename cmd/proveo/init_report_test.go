// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml, _spec/_plans/output-readability.puml
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/gitidentity"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// spared is a failing check a run does not need, as the e2e stub reports it.
const spared = `{"checks":[{"name":"virtualisation","status":"pass","message":"supported"},` +
	`{"name":"SSH client config","status":"fail","message":"an unmanaged sandbox Host stanza can shadow the managed configuration",` +
	`"detail":"/home/op/.ssh/config:5: Host","hint":"Remove or rename the reported Host *.sbx stanza, then run sbx setup ssh"}]}`

// blocker adds a failing row a run needs.
const blocker = `{"checks":[{"name":"Daemon","status":"fail","message":"not running","detail":"no socket","hint":"Start Docker Sandboxes"}]}`

// testReport is a report on a darwin host the stub sbx answers for.
func testReport(t *testing.T, o initOptions) *initReport {
	t.Helper()
	host := sbx.Host{OS: "darwin", Arch: "arm64"}
	plan := sbx.Plan{Prefix: filepath.Join(t.TempDir(), "prefix")}
	return newInitReport(o, host, []sbx.Prereq{{Name: "Apple silicon", OK: true}}, plan)
}

// lastLine is the last non-blank line of out.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines[len(lines)-1]
}

// headings counts each section heading drawn in plain output.
func headings(out string) map[string]int {
	n := map[string]int{}
	for _, m := range regexp.MustCompile(`(?m)^\s*-{6} (\S+) -{6}$`).FindAllStringSubmatch(out, -1) {
		n[m[1]]++
	}
	return n
}

// The sbx facts print once, after the sbx steps that make them true.
func TestTheSbxBlockPrintsAfterTheSbxStepsAndNamesTheHostAndItsSbx(t *testing.T) {
	isolatedInitHost(t)
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	d := initDecisions{Install: installSkip, Login: true}
	if err := runApply(applyPlan(d, applyCtx{plan: rep.applied, applied: rep.applied, rep: rep}), nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	login, host := strings.Index(got, "sbx login — signing in to Docker"), regexp.MustCompile(`host:\s+darwin/arm64`).FindStringIndex(got)
	if login < 0 || host == nil || host[0] < login {
		t.Fatalf("the sbx facts must follow the hand line of the step that makes them true:\n%s", got)
	}
	for _, want := range []string{`(?m)^\s+prerequisites met: Apple silicon$`, `(?m)^sbx:\s+0\.50\.0$`, `(?m)^network: sbx's global policy allows listed hosts only$`} {
		if !regexp.MustCompile(want).MatchString(got) {
			t.Errorf("the sbx block lacks %s:\n%s", want, got)
		}
	}
	for _, gone := range []string{"proveo pins", "already installed", "step ", "on PATH"} {
		if strings.Contains(got, gone) {
			t.Errorf("the sbx block still says %q:\n%s", gone, got)
		}
	}
	if n := headings(got); n["sbx"] != 1 {
		t.Errorf("headings = %v, want sbx once", n)
	}
}

// A revisit re-runs verify into ONE readiness set; with no sbx step it prints
// no second sbx block, an sbx step it does run lands under a redrawn "sbx", and
// it prints only the facts that changed: no second host, no second git block.
func TestARevisitPrintsOneReadinessSetAndItsSbxLinesUnderSbx(t *testing.T) {
	isolatedInitHost(t)
	t.Setenv("STUB_DIAGNOSE", spared)
	t.Setenv("GIT_AUTHOR_NAME", "Ada")
	t.Setenv("GIT_AUTHOR_EMAIL", "ada@example.invalid")
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rec := &initRecord{}
	ctx := applyCtx{plan: rep.applied, applied: rep.applied, rep: rep}
	for _, d := range []initDecisions{{Install: installSkip, Login: true, Git: true}, {Install: installSkip}} {
		if err := runApply(applyPlan(d, ctx), rec.did); err != nil {
			t.Fatal(err)
		}
	}
	ui.Section(ui.SectionCredentials)
	ui.Asyncf("codex — signing out")
	t.Setenv("STUB_POLICY_ALLOWED", "true")
	if err := runApply(applyPlan(initDecisions{Install: installSkip, Login: true, Git: true}, ctx), rec.did); err != nil {
		t.Fatal(err)
	}
	if err := rep.close(rec, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if n := strings.Count(got, "SSH client config — "); n != 1 {
		t.Errorf("the readiness printed %d times, want once:\n%s", n, got)
	}
	signOut, again := strings.Index(got, "codex — signing out"), strings.LastIndex(got, "sbx login — signing in to Docker")
	if again < signOut || !strings.Contains(got[signOut:again], "------ sbx ------") {
		t.Errorf("a revisit's sbx step must land under a redrawn sbx heading:\n%s", got)
	}
	for re, want := range map[string]int{`host:\s+darwin/arm64`: 1, `sbx:\s+0\.50\.0`: 1, `identity:\s+Ada`: 1, `network: sbx's global policy allows every host`: 1} {
		if n := len(regexp.MustCompile(re).FindAllString(got, -1)); n != want {
			t.Errorf("%s printed %d times, want %d: a revisit prints only what changed:\n%s", re, n, want, got)
		}
	}
	if !strings.Contains(got[again:], "network: sbx's global policy allows every host") {
		t.Errorf("the fact the revisit changed must print after its step:\n%s", got)
	}
	if n := headings(got); n["git"] != 1 {
		t.Errorf("headings = %v, want git once: the revisit's git step changed nothing:\n%s", n, got)
	}
}

// Two forward passes through the credentials page print one fact set, the latest.
func TestTheCredentialsBlockPrintsOnceFromTheLatestPass(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.sbxSaid = true
	rep.setCredentials([]ui.Fact{{Role: ui.RoleStore, Key: "codex", Value: "login held by sbx as openai"}}, nil)
	rep.setCredentials([]ui.Fact{{Role: ui.RoleStore, Key: "opencode", Value: "key stored as opencode"}}, nil)
	_ = rep.close(&initRecord{}, nil)
	got := out.String()
	if strings.Contains(got, "codex") || strings.Count(got, "opencode: key stored as opencode") != 1 || headings(got)["credentials"] != 1 {
		t.Errorf("want one credentials block, from the latest pass:\n%s", got)
	}
}

// F3: the result lists what waits, then the readiness items, and closes on the verdict.
func TestTheResultListsWhatWaitsAndClosesOnTheVerdict(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.sbxSaid = true
	items, _ := checkItems([]sbx.Check{sshClientConfig})
	rep.readiness, rep.ready = items, true
	rec := &initRecord{}
	rec.leaveUnchosen(initDecisions{Login: true, Wizard: true})
	rec.leave(labelCredentials)
	if err := rep.close(rec, nil); err != nil {
		t.Fatalf("close = %v, want nil on success", err)
	}
	got := out.String()
	results := got[strings.Index(got, "------ results ------"):]
	for _, re := range []string{`(?m)^later: GitHub$`, `(?m)^\s+git identity$`, `(?m)^\s+prompt$`, `(?m)^\s+credentials$`,
		`(?m)^warn: SSH client config — an unmanaged sandbox Host stanza`, `(?m)^\s+/home/op/\.ssh/config:5: Host$`,
		`(?m)^\s+a run does not need this check; it affects IDE attach`} {
		if !regexp.MustCompile(re).MatchString(results) {
			t.Errorf("the result lacks %s:\n%s", re, results)
		}
	}
	if last := lastLine(got); last != "ok: host — ready to run" {
		t.Errorf("the last line = %q, want the verdict", last)
	}
	for _, gone := range []string{"done:", "next:", "proveo init`", "does not manage", "checks passed"} {
		if strings.Contains(got, gone) {
			t.Errorf("the result still says %q:\n%s", gone, got)
		}
	}
}

// F3 and UNREACHED: a run blocker closes on one × host line, names what never ran, and main prints nothing more.
func TestARunBlockerClosesOnOneHostVerdictAndNamesWhatNeverRan(t *testing.T) {
	isolatedInitHost(t)
	t.Setenv("STUB_DIAGNOSE", blocker)
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rec := &initRecord{}
	err := runApply(applyPlan(initDecisions{Install: installSkip}, applyCtx{plan: rep.applied, applied: rep.applied, rep: rep}), rec.did)
	var failed stepErr
	if !errors.As(err, &failed) {
		t.Fatalf("runApply = %v, want the verify step's failure", err)
	}
	rec.unreached(failed.rest...)
	rec.unreached(labelCredentials, labelOperator)
	ret := rep.close(rec, err)
	if code, ok := exitCode(ret); !ok || code != 1 {
		t.Errorf("close = %v, want Reported (exit 1, main prints nothing)", ret)
	}
	got := out.String()
	if last := lastLine(got); last != "error: host — not ready to run" {
		t.Errorf("the last line = %q", last)
	}
	if n := strings.Count(got, "error: host"); n != 1 {
		t.Errorf("%d × host lines, want one:\n%s", n, got)
	}
	for _, re := range []string{`(?m)^error: Daemon — not running$`, `(?m)^\s+Start Docker Sandboxes$`, `(?m)^not run: credentials$`, `(?m)^\s+operator name$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the result lacks %s:\n%s", re, got)
		}
	}
}

// P8 and F3: a quit closes on ONE ⚠ line, with the verdict a finished verify reached dim under it.
func TestAQuitClosesOnOneWarningWithTheVerdictDim(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.sbxSaid, rep.ready = true, true
	steps := []initStep{
		func(choiceui.Step) (stepResult, error) { return stepNext, nil },
		func(choiceui.Step) (stepResult, error) { return stepNext, nil },
		func(choiceui.Step) (stepResult, error) { return stepQuit, nil },
	}
	ret := rep.close(&initRecord{}, runInitSteps(true, steps))
	if code, ok := exitCode(ret); !ok || code != 1 {
		t.Errorf("close = %v, want exit 1 with nothing printed by main", ret)
	}
	got := out.String()
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "warn: init — quit at page 3/3\n    host ready to run") {
		t.Errorf("want the quit as the closing line and the verdict dim under it:\n%s", got)
	}
	if strings.Contains(got, "ok: host") || strings.Contains(got, "error:") {
		t.Errorf("a quit printed a second verdict or a ×:\n%s", got)
	}
}

// F3: every ending after a passed verify keeps "host ready to run" as its last dim line.
func TestEveryEndingAfterAPassedVerifyKeepsTheVerdictLast(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{stepErr{label: labelCredentials, err: errors.New("form broke")}, "error: credentials — failed\n    form broke\n    host ready to run"},
		{stepErr{label: labelOperator, err: errors.New("operator.yml unreadable")}, "error: operator name — failed\n    operator.yml unreadable\n    host ready to run"},
		{stepErr{label: labelCredentials, err: handoffErr{err: errors.New("signal: interrupt"), interrupted: true}},
			"warn: init — stopped at credentials\n    Ctrl+C ended it\n    host ready to run"},
		{stepErr{label: rowGh, err: handoffErr{err: errors.New("exit status 1")}}, "warn: init — stopped at GitHub\n    host ready to run"},
		{errors.New("the setup form broke"), "error: init — stopped\n    the setup form broke\n    host ready to run"},
	} {
		out := captureUI(t)
		rep := testReport(t, initOptions{})
		rep.sbxSaid, rep.ready = true, true
		_ = rep.close(&initRecord{}, tc.err)
		if got := out.String(); !strings.HasSuffix(strings.TrimRight(got, "\n"), tc.want) {
			t.Errorf("%v: want the ending, then the verdict dim and last:\n%s", tc.err, got)
		}
	}
}

// An error that names no step, such as the setup page's own form error or a prefix it chose that
// has no plan, closes on "× init — stopped" with the error dim, and main prints nothing more.
func TestAnErrorWithNoStepClosesOnInitStopped(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.sbxSaid = true
	ret := rep.close(&initRecord{}, errors.New("no Docker Sandboxes build for plan9/arm"))
	if code, ok := exitCode(ret); !ok || code != 1 {
		t.Errorf("close = %v, want Reported (exit 1, main prints nothing)", ret)
	}
	if got := out.String(); !regexp.MustCompile(`-{6} results -{6}\nerror: init — stopped\n    no Docker Sandboxes build for plan9/arm\n*\z`).MatchString(got) {
		t.Errorf("want the fallback ending with the error dim:\n%s", got)
	}
}

// HAND-OFF FAILURE: the program's own error stands; init ends on where it stopped, with no × and no restated error.
func TestAHandOffFailureEndsOnWhereInitStopped(t *testing.T) {
	isolatedInitHost(t)
	t.Setenv("STUB_LOGIN_EXIT", "1")
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rec := &initRecord{}
	err := runApply(applyPlan(initDecisions{Install: installSkip, Login: true, Wizard: true}, applyCtx{plan: rep.applied, applied: rep.applied, rep: rep}), rec.did)
	var failed stepErr
	if errors.As(err, &failed) {
		rec.unreached(failed.rest...)
	}
	ret := rep.close(rec, err)
	if code, ok := exitCode(ret); !ok || code != 1 {
		t.Errorf("close = %v, want Reported", ret)
	}
	got := out.String()
	if last := lastLine(got); last != "warn: init — stopped at sign in" {
		t.Errorf("the last line = %q:\n%s", last, got)
	}
	if strings.Contains(got, "error:") || strings.Contains(got, "exit status") {
		t.Errorf("init repeated the program's failure:\n%s", got)
	}
	if !regexp.MustCompile(`(?m)^not run: sbx setup\n\s+sbx diagnose$`).MatchString(got) {
		t.Errorf("the chosen steps the failure kept from running are not listed:\n%s", got)
	}
	if !regexp.MustCompile(`host:\s+darwin/arm64`).MatchString(got) {
		t.Errorf("the sbx block an early stop left unprinted did not print:\n%s", got)
	}
}

// never is a page that must not run once init stopped.
func never(t *testing.T) initStep {
	return func(choiceui.Step) (stepResult, error) {
		t.Error("a page ran after init stopped")
		return stepNext, nil
	}
}

// HAND-OFF FAILURE: a failed `gh auth login` ends init where it stopped; the git block it gathered
// prints, and verify and the pages after setup are named as never run.
func TestAFailedGhAuthLoginEndsInitOnWhereItStopped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing gh is a shell")
	}
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.sbxSaid = true
	rec := &initRecord{}
	g, _ := stubAuth(nil, true, "", errors.New("no session"), func() error { return handOff(exec.Command("sh", "-c", "exit 1")) })
	setup := func(choiceui.Step) (stepResult, error) {
		rep.openGit()
		rep.gitBlock().Fact(ui.Fact{Role: ui.RoleHost, Key: "identity", Value: "Ada <ada@ex>"})
		err := runApply([]applyStep{
			{name: actGitHub, label: rowGh, kind: stepSilent, run: func() error {
				return applyGitHub(ghStage{auth: *g, tty: true, out: rep.gitBlock(), setenv: func(string, string) error { return nil }})
			}},
			{run: func() error { rep.printGit(); return nil }},
			{name: "verify sbx (sbx diagnose)", label: labelVerify, kind: stepQuiet, run: func() error {
				t.Error("verify ran after a failed sign-in")
				return nil
			}},
		}, rec.did)
		var failed stepErr
		if errors.As(err, &failed) {
			rec.unreached(failed.rest...)
		}
		return stepNext, err
	}
	err := runInitSteps(false, rec.pages([]initPage{{run: setup}, {label: labelCredentials, run: never(t)}, {label: labelOperator, run: never(t)}}))
	ret := rep.close(rec, err)
	if code, ok := exitCode(ret); !ok || code != 1 {
		t.Errorf("close = %v, want Reported (exit 1, main prints nothing)", ret)
	}
	got := out.String()
	if last := lastLine(got); last != "warn: init — stopped at GitHub" {
		t.Errorf("the last line = %q:\n%s", last, got)
	}
	for _, re := range []string{`(?m)^identity: Ada <ada@ex>$`, `(?m)^not run: sbx diagnose\n\s+credentials\n\s+operator name$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the result lacks %s:\n%s", re, got)
		}
	}
	if strings.Contains(got, "error:") || strings.Contains(got, "did not complete") || strings.Contains(got, "exit status") {
		t.Errorf("init repeated gh's failure:\n%s", got)
	}
}

// UNREACHED: a Ctrl+C that ends a sign-in on the credentials page names the rows answered after it
// and the operator page as never run.
func TestACredentialsPageStopNamesWhatItKeptFromRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT to a child is a unix test")
	}
	isolatedInitHost(t)
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.sbxSaid = true
	rec := &initRecord{}
	slots := []credSlot{
		{Label: "claudecode", Kind: kindSignIn, Store: "anthropic"},
		{Label: "codex", Kind: kindSignIn, Store: "openai", Held: true},
		{Label: "cursor", Kind: kindSignIn, Store: "cursor"},
		{Label: "anthropic", Kind: kindKey, Store: "anthropic"},
		{Label: "groq", Kind: kindKey, Store: "groq"},
	}
	rows := []choiceui.Row{
		{Options: []string{notSignedIn, signInNow}, Selected: 1},
		{Options: []string{signedIn, signInAgain, signOut}, Selected: 2},
		{Options: []string{notSignedIn, signInNow}},
		{Field: true, Value: "sk-ant"},
		{Field: true},
	}
	ctrlC := func(argv []string) error {
		if argv[1] == "run" {
			return handOff(exec.Command("sh", "-c", "kill -INT $$"))
		}
		return nil
	}
	held := func(string) (bool, bool) { return true, true }
	credentials := func(choiceui.Step) (stepResult, error) {
		_, _, err := applyCredentials(slots, rows, ctrlC, held, rec)
		return stepNext, stopAt(labelCredentials, err)
	}
	err := runInitSteps(false, rec.pages([]initPage{{run: func(choiceui.Step) (stepResult, error) { return stepNext, nil }},
		{label: labelCredentials, run: credentials}, {label: labelOperator, run: never(t)}}))
	ret := rep.close(rec, err)
	if code, ok := exitCode(ret); !ok || code != 130 {
		t.Errorf("close = %v (%d), want exit 130", ret, code)
	}
	got := out.String()
	if !regexp.MustCompile(`(?m)^not run: codex sign-out\n\s+anthropic key\n\s+operator name$`).MatchString(got) {
		t.Errorf("the result does not name what the stop kept from running:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "warn: init — stopped at credentials\n    Ctrl+C ended it") {
		t.Errorf("want the stop and its cause last:\n%s", got)
	}
}

// UNREACHED: a distro package this release does not ship fails the install step, and the chosen
// steps after it are named as never run.
func TestAMissingDistroPackageStopsAtTheInstallAndNamesWhatNeverRan(t *testing.T) {
	calls := isolatedInitHost(t)
	out := captureUI(t)
	host := sbx.Host{OS: "linux", Arch: "amd64", Distro: "rocky", Version: "9"}
	rep := newInitReport(initOptions{}, host, nil, sbx.Plan{})
	rec := &initRecord{}
	d := initDecisions{Install: installPackaged, Login: true}
	rec.leaveUnchosen(d)
	err := runApply(applyPlan(d, applyCtx{applied: appliedPlan(sbx.Plan{}, true), host: host, rep: rep}), rec.did)
	var failed stepErr
	if !errors.As(err, &failed) || failed.label != rowInstall {
		t.Fatalf("runApply = %v, want the install step's failure", err)
	}
	rec.unreached(failed.rest...)
	_ = rep.close(rec, err)
	got := out.String()
	for _, re := range []string{`(?m)^not run: sign in\n\s+sbx diagnose$`,
		`(?m)^error: sbx install — failed\n\s+this release ships no distro package for linux/amd64 · rocky 9$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the result lacks %s:\n%s", re, got)
		}
	}
	if b, _ := os.ReadFile(calls); strings.Contains(string(b), "login") {
		t.Errorf("sbx login ran after the install failed:\n%s", b)
	}
}

// --force over an sbx outside the prefix: the sbx block says which sbx this shell's PATH finds.
func TestTheSbxBlockNamesAnotherSbxThisShellsPathFindsFirst(t *testing.T) {
	isolatedInitHost(t)
	out := captureUI(t)
	prefix := filepath.Join(t.TempDir(), "prefix")
	bin := filepath.Join(prefix, "bin", "sbx")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'sbx version: v0.49.2 cafe'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep := newInitReport(initOptions{}, sbx.Host{OS: "darwin", Arch: "arm64"}, nil, sbx.Plan{Prefix: prefix, Bin: bin})
	rep.printSbx()
	got := out.String()
	first, _ := exec.LookPath(sbx.Binary)
	for _, re := range []string{`(?m)^sbx:\s+0\.49\.2$`,
		`(?m)^warn: sbx — this shell's PATH finds ` + regexp.QuoteMeta(ui.Tilde(first)) + `\n\s+sbx 0\.50\.0\n\s+a run in this shell uses it$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the sbx block lacks %s:\n%s", re, got)
		}
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if items := rep.shadowItems(); len(items) != 0 {
		t.Errorf("the prefix's sbx first on PATH = %+v, want no item", items)
	}
}

// A Ctrl+C reaches the program init handed the terminal to, not init; init then ends cleanly on
// where it stopped and exits 130.
func TestACtrlCDuringAHandOffEndsInitCleanlyWith130(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT to a child is a unix test")
	}
	err := handOff(exec.Command("sh", "-c", "kill -INT $$"))
	if !interrupted(err) {
		t.Fatalf("handOff = %#v, want a hand-off a Ctrl+C ended", err)
	}
	if err := handOff(exec.Command("sh", "-c", "exit 3")); interrupted(err) || errDetail(err) != nil {
		t.Errorf("an ordinary failure = %#v, want a hand-off failure whose output was the program's own", err)
	}
	if err := handOff(exec.Command(filepath.Join(t.TempDir(), "absent"))); err == nil || errDetail(err) == nil {
		t.Errorf("a program that never started = %v, want proveo's own error with its detail", err)
	}
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.sbxSaid = true
	ret := rep.close(&initRecord{}, stepErr{label: rowSignIn, err: handOff(exec.Command("sh", "-c", "kill -INT $$"))})
	if code, ok := exitCode(ret); !ok || code != 130 {
		t.Errorf("close = %v (%d), want exit 130 with nothing printed by main", ret, code)
	}
	if got := out.String(); !strings.HasSuffix(strings.TrimRight(got, "\n"), "warn: init — stopped at sign in\n    Ctrl+C ended it") {
		t.Errorf("want the stop and its cause:\n%s", got)
	}
}

// A failure proveo itself found closes on its step's ×, with the error dim and split at " — ".
func TestAFailureProveoFoundClosesOnItsStep(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.sbxSaid = true
	ret := rep.close(&initRecord{}, stepErr{label: rowInstall, err: errors.New("/usr/local is not writable by this user — re-run with `sudo -E`")})
	if _, ok := exitCode(ret); !ok {
		t.Errorf("close = %v, want Reported", ret)
	}
	if got := out.String(); !strings.HasSuffix(strings.TrimRight(got, "\n"), "error: sbx install — failed\n    /usr/local is not writable by this user\n    re-run with `sudo -E`") {
		t.Errorf("want the step's ×, then the error dim:\n%s", got)
	}
}

// A blocked prerequisite ends on its × item; no result block repeats it.
func TestABlockedPrerequisiteEndsOnItsItem(t *testing.T) {
	isolatedInitHost(t)
	out := captureUI(t)
	stop := []sbx.Prereq{{Name: "e2fsprogs", Blocks: sbx.BlocksInstall, Detail: "mkfs.ext4 — the release's installer refuses to run without it", Fix: "sudo dnf install e2fsprogs"}}
	rep := newInitReport(initOptions{}, sbx.Host{OS: "linux", Arch: "amd64"}, stop, sbx.Plan{})
	ret := rep.close(nil, blockedErr{stop})
	if _, ok := exitCode(ret); !ok {
		t.Errorf("close = %v, want Reported", ret)
	}
	got := out.String()
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "error: e2fsprogs — not met\n    mkfs.ext4\n    sudo dnf install e2fsprogs") {
		t.Errorf("want the prerequisite's × last, its why left out:\n%s", got)
	}
	if strings.Contains(got, "results") || strings.Contains(got, "refuses") {
		t.Errorf("a blocked prerequisite printed a result or its why:\n%s", got)
	}
}

// P1: each subject's heading is drawn once in an init that runs to its end.
func TestEachInitHeadingIsDrawnOnce(t *testing.T) {
	isolatedInitHost(t)
	t.Setenv("STUB_DIAGNOSE", spared)
	t.Setenv("GIT_AUTHOR_NAME", "Ada")
	t.Setenv("GIT_AUTHOR_EMAIL", "ada@example.invalid")
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rec := &initRecord{}
	d := initDecisions{Install: installSkip, Login: true, Wizard: true, Git: true}
	if err := runApply(applyPlan(d, applyCtx{plan: rep.applied, applied: rep.applied, rep: rep}), rec.did); err != nil {
		t.Fatal(err)
	}
	rep.setCredentials([]ui.Fact{{Role: ui.RoleStore, Key: "codex", Value: "login held by sbx as openai"}}, nil)
	rep.setOperator("Ada", nil)
	if err := rep.close(rec, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	want := map[string]int{"sbx": 1, "git": 1, "credentials": 1, "operator": 1, "results": 1}
	if n := headings(got); !mapsEqual(n, want) {
		t.Errorf("headings = %v, want %v:\n%s", n, want, got)
	}
	for _, gone := range []string{"execution", "starting", "secrets", "workspace", "------ run "} {
		if strings.Contains(got, gone) {
			t.Errorf("init drew the %q heading:\n%s", gone, got)
		}
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// P3: the git block holds the identity, GitHub's token and sandbox Git's access as one aligned block.
func TestTheGitBlockHoldsIdentityGitHubAndAccess(t *testing.T) {
	out := captureUI(t)
	block := sandbox.NewBlock(gitHeading)
	applyGitIdentity(gitStage{
		resolve:  func() gitidentity.Identity { return gitidentity.Identity{Name: "Ada", Email: "ada@ex"} },
		lookPath: func(string) (string, error) { return "/usr/bin/git", nil },
		out:      block,
	})
	g, _ := stubAuth(map[string]string{"GH_TOKEN": "tok"}, true, "", nil, nil)
	if err := applyGitHub(ghStage{auth: *g, out: block, setenv: func(string, string) error { return nil },
		secretSet: func(string, string) error { return nil },
		access: func() error {
			block.Fact(ui.Fact{Role: ui.RoleHost, Key: "access", Value: "ssh"})
			return nil
		}}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("the git block printed before its steps ended: %q", out.String())
	}
	block.Print()
	got := out.String()
	for _, want := range []string{"identity: Ada <ada@ex>\n", "github:   token stored in sbx\n", "access:   ssh\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("the git block lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "proxy") {
		t.Errorf("a why reached the screen:\n%s", got)
	}
}

// NO TTY: a question init could not ask keeps one dim how-to; --yes, an operator act, keeps none.
func TestOnlyAnInitWithNoTerminalKeepsAHowToUnderWhatItCouldNotAsk(t *testing.T) {
	for _, noTerminal := range []bool{true, false} {
		out := captureUI(t)
		applyGitIdentity(gitStage{
			resolve:  func() gitidentity.Identity { return gitidentity.Identity{} },
			lookPath: func(string) (string, error) { return "/usr/bin/git", nil },
			setGlobal: func(string, string) error {
				t.Error("an unasked identity was written")
				return nil
			},
			noTerminal: noTerminal,
		})
		asked := askedByFlag
		if noTerminal {
			asked = askedNoTerminal
		}
		rec := &initRecord{}
		rec.leave(labelCredentials, asked.howTo("`proveo init` from a terminal provisions them")...)
		rep := testReport(t, initOptions{yes: !noTerminal})
		rep.asked, rep.sbxSaid = asked, true
		rep.setOperator("Executor", asked.howTo("`proveo init` from a terminal changes it"))
		_ = rep.close(rec, nil)
		got := out.String()
		for _, howTo := range []string{noIdentityHowTo, "`proveo init` from a terminal provisions them", "`proveo init` from a terminal changes it"} {
			if strings.Contains(got, howTo) != noTerminal {
				t.Errorf("noTerminal=%v: how-to %q present=%v:\n%s", noTerminal, howTo, !noTerminal, got)
			}
		}
		if !strings.Contains(got, "warn: git identity — not set") || !strings.Contains(got, "name: Executor") {
			t.Errorf("noTerminal=%v: the identity's ⚠ or the name's fact is missing:\n%s", noTerminal, got)
		}
	}
}

// P7: the operator's name is a fact, never a ✓ line.
func TestTheOperatorsNameIsAFact(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.setOperator("Ada", nil)
	rep.printCredentials()
	if got := out.String(); !strings.Contains(got, "name: Ada\n") || strings.Contains(got, "ok:") || strings.Contains(got, "address you") {
		t.Errorf("the operator block = %q, want one name fact", got)
	}
}

// --print prints the credentials and operator blocks, and keeps its paths absolute.
func TestPrintShowsTheCredentialsAndOperatorBlocks(t *testing.T) {
	isolatedInitHost(t)
	out := captureUI(t)
	host := sbx.DetectHost()
	dir := t.TempDir()
	if _, err := sbx.PlanFor(host, filepath.Join(dir, "p"), dir); err != nil {
		t.Skipf("no sbx build for %s/%s", host.OS, host.Arch)
	}
	if err := doInit(initOptions{printOnly: true, prefix: filepath.Join(dir, "p")}); err != nil {
		t.Fatalf("doInit(--print) = %v", err)
	}
	got := out.String()
	n := headings(got)
	for _, h := range []string{"sbx", "git", "credentials", "operator"} {
		if n[h] != 1 {
			t.Errorf("--print drew %q %d times:\n%s", h, n[h], got)
		}
	}
	for _, want := range []string{"host: " + host.OS + "/" + host.Arch, "download https://github.com/docker/sbx-releases", "gh auth login",
		"git config --get user.name", "name: Executor", filepath.Join(dir, "p")} {
		if !strings.Contains(got, want) {
			t.Errorf("--print lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "this host carries") || strings.Contains(got, "------ run") || strings.Contains(got, "------ secrets") {
		t.Errorf("--print kept a BEFORE line:\n%s", got)
	}
}

// The MSI's install line names its installer, and its ✓ says the MSI owns where sbx lives.
func TestAWindowsInstallNamesItsInstallerAndWhoOwnsItsPlace(t *testing.T) {
	plan := windowsPlan(t)
	var line string
	for _, s := range applyPlan(initDecisions{Install: installTarball}, applyCtx{plan: plan, applied: plan}) {
		if s.kind == stepHand {
			line = s.line
		}
	}
	if line != "sbx "+sbx.Release+" — installing with msiexec.exe" {
		t.Errorf("the MSI's hand line = %q", line)
	}
	rep := testReport(t, initOptions{})
	if err := placeRelease(sbx.Plan{Note: plan.Note}, "", "abc", false, rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.sbxItems) != 1 || rep.sbxItems[0].Text != "sbx "+sbx.Release+" — installed" ||
		!slices.Equal(rep.sbxItems[0].Detail, []string{"the MSI owns its install location and puts sbx on PATH itself", "sha256 abc", "no published digest covers this asset"}) {
		t.Errorf("the MSI's install item = %+v", rep.sbxItems)
	}
	t.Setenv("PATH", t.TempDir())
	if err := verify(sbx.Binary, rep); err != nil || len(rep.readiness) != 1 || rep.readiness[0].Text != "sbx — not on this shell's PATH" {
		t.Errorf("verify of an sbx this process cannot find = %v %+v, want the item that a new shell finds it", err, rep.readiness)
	}
}

// A tarball install's ✓ restates neither the version nor the bin the "sbx:" fact names above it,
// and names the version the prefix held before.
func TestATarballInstallsTickSaysOnlyWhatTheFactsDoNot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub installer is a shell")
	}
	isolatedInitHost(t)
	out := captureUI(t)
	prefix := filepath.Join(t.TempDir(), "prefix")
	bin := filepath.Join(prefix, "bin", "sbx")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'sbx version: v0.47.1 cafe'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := sbx.Plan{Prefix: prefix, Bin: bin, Steps: []sbx.Step{{What: "place sbx",
		Argv: []string{"sh", "-c", `printf '#!/bin/sh\necho "sbx version: v%s beef"\n' "$RELEASE" > "$BIN"`},
		Env:  []string{"RELEASE=" + sbx.Release, "BIN=" + bin}}}}
	rep := newInitReport(initOptions{}, sbx.Host{OS: "darwin", Arch: "arm64"}, nil, plan)
	if err := placeRelease(plan, t.TempDir(), "abc", true, rep); err != nil {
		t.Fatal(err)
	}
	rep.printSbx()
	got := out.String()
	for _, re := range []string{`(?m)^sbx:\s+` + regexp.QuoteMeta(sbx.Release) + `\n\s+` + regexp.QuoteMeta(bin) + `$`,
		`(?m)^ok: sbx — installed\n    sha256 abc\n    matches the release's provenance\n    replaced 0\.47\.1$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the sbx block lacks %s:\n%s", re, got)
		}
	}
	if n := strings.Count(got, bin); n != 1 {
		t.Errorf("the bin is named %d times, want once, under the sbx fact:\n%s", n, got)
	}
}

// A passing prerequisite is dim detail under host; an unmet one is an item with what it looked for and its fix.
func TestAPassingPrerequisiteIsDetailAndAnUnmetOneIsAnItem(t *testing.T) {
	checks := []sbx.Prereq{
		{Name: "e2fsprogs", OK: true, Detail: "mkfs.ext4 — the release's installer refuses to run without it"},
		{Name: "SELinux", Blocks: sbx.BlocksNothing, Detail: "enforcing — the release ships an AppArmor profile", Fix: "`sudo setenforce 0` briefly"},
	}
	if got := metPrereqs(checks); !slices.Equal(got, []string{"e2fsprogs"}) {
		t.Errorf("met = %q", got)
	}
	items := prereqItems(checks)
	if len(items) != 1 || !items[0].Warn || items[0].Text != "SELinux — not met" || !slices.Equal(items[0].Detail, []string{"enforcing", "`sudo setenforce 0` briefly"}) {
		t.Errorf("items = %+v", items)
	}
}

// PATH: each outcome is one item; the print-only line stays absolute so it can be pasted.
func TestThePathStepSaysWhatChangedAsOneItem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PATH", "/usr/bin:/bin")
	dir := filepath.Join(home, ".docker", "sbx", "bin")
	if it := ensureSbxOnPath(dir, false); !it.Warn || it.Text != "~/.docker/sbx/bin — not on PATH" ||
		len(it.Detail) != 1 || !strings.HasPrefix(it.Detail[0], "add to ~/.zshrc: ") || !strings.Contains(it.Detail[0], dir) {
		t.Errorf("print only = %+v", it)
	}
	if it := ensureSbxOnPath(dir, true); !it.Ok || it.Text != "~/.docker/sbx/bin — added to PATH in ~/.zshrc" {
		t.Errorf("added = %+v", it)
	}
	if it := ensureSbxOnPath(dir, true); !it.Warn || it.Text != "~/.docker/sbx/bin — not on this shell's PATH" {
		t.Errorf("an rc that already adds it = %+v, want the shell that still lacks it named", it)
	}
	t.Setenv("PATH", dir)
	if it := ensureSbxOnPath(dir, true); it.Text != "" {
		t.Errorf("a dir on PATH = %+v, want nothing", it)
	}
}

// F2: what left the screen for good is in `proveo init --help`.
func TestFlagHelpCarriesTheHowTosThatLeftTheScreen(t *testing.T) {
	cmd := initCmd()
	for _, want := range []string{"`proveo init` again offers what was left for later", "`proveo run` lists the harnesses",
		"reinstall sbx with --force", "`source <rc>`", "`proveo uninstall` takes it out again", "starship preset nerd-font-symbols"} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("init's help lacks %q", want)
		}
	}
	if !strings.Contains(cmd.Flags().Lookup("force").Usage, "broken or half-installed") ||
		!strings.Contains(cmd.Flags().Lookup("yes").Usage, "wait for `proveo init` on a terminal") {
		t.Error("--force or --yes lost the how-to that left the screen")
	}
	if yes := cmd.Flags().Lookup("yes").Usage; !strings.Contains(yes, "`git config --global user.name` and `user.email`") {
		t.Errorf("--yes asks for no git identity, so its help must carry the identity's how-to: %q", yes)
	}
}
