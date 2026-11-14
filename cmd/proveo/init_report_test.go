// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml
package main

import (
	"bytes"
	"errors"
	"fmt"
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
	"github.com/proveo-ca/proveo/internal/termtext"
	"github.com/proveo-ca/proveo/internal/ui"
)

// spared is a failing check a run does not need, as the e2e stub reports it.
const spared = `{"checks":[{"name":"virtualisation","status":"pass","message":"supported"},` +
	`{"name":"SSH client config","status":"fail","message":"an unmanaged sandbox Host stanza can shadow the managed configuration",` +
	`"detail":"/home/op/.ssh/config:5: Host","hint":"Remove or rename the reported Host *.sbx stanza, then run sbx setup ssh"}]}`

// blocker adds a failing row a run needs.
const blocker = `{"checks":[{"name":"Daemon","status":"fail","message":"not running","detail":"no socket","hint":"Start Docker Sandboxes"}]}`

// testReport is a report on a darwin host the stub sbx answers for, whose setup probe finds nothing to import.
func testReport(t *testing.T, o initOptions) *initReport {
	t.Helper()
	host := sbx.Host{OS: "darwin", Arch: "arm64"}
	plan := sbx.Plan{Prefix: filepath.Join(t.TempDir(), "prefix")}
	rep := newInitReport(o, host, []sbx.Prereq{{Name: "Apple silicon", OK: true}}, plan)
	rep.probe = func() (sbx.SetupOffer, error) { return sbx.SetupOffer{Known: true}, nil }
	return rep
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

// closedHead matches the closed checklist's heading.
var closedHead = regexp.MustCompile(`(?m)^── proveo init ── .+ ──+$`)

// cardUI makes ui.Default a cols-wide terminal in the safe glyph tier and returns what it draws.
func cardUI(t *testing.T, cols int) *bytes.Buffer {
	t.Helper()
	t.Setenv("PROVEO_GLYPHS", "")
	var out bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.NewTerminal(&out, cols)
	return &out
}

// recordedWalk is the report and record a default walk on the recorded host leaves: sbx 0.50.0 kept
// at ~/.docker/sbx/bin/sbx, Docker signed in, two checks that need the operator, git identity
// written, GitHub and the prompt kit later, codex and opencode held, anthropic imported, and the
// default operator name.
func recordedWalk(t *testing.T) (*initReport, *initRecord) {
	t.Helper()
	isolatedInitHost(t)
	bin := filepath.Join(os.Getenv("HOME"), ".docker", "sbx", "bin", "sbx")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'sbx version: v0.50.0 deadbeef'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	rep := newInitReport(initOptions{}, framesHost, framesPrereqs, sbx.Plan{Prefix: filepath.Dir(filepath.Dir(bin)), Bin: bin})
	rep.probe = func() (sbx.SetupOffer, error) { return sbx.SetupOffer{Known: true}, nil }
	rep.installed = "0.50.0"
	rep.heard(append(slices.Clone(framesDiag), sbx.Check{Name: sbx.AuthenticationCheck, Status: "pass", Message: "authenticated"}), nil)
	_ = verifyWith(bin, rep, rep.diag, nil)
	rec := &initRecord{}
	rec.row(rowInstall, choiceui.MarkLater, "kept 0.50.0")
	rec.row(rowLogin, choiceui.MarkDone, loginSignedIn)
	rec.row(rowGit, choiceui.MarkDone, "Ada Example <ada@example.invalid> · git's global config")
	rec.row(rowGh, choiceui.MarkLater, "later")
	rec.row(rowPrompt, choiceui.MarkLater, "later")
	anthropic := credSlot{Label: "anthropic", Kind: kindKey, Store: "anthropic", Held: true}
	rep.setCredentials(credsHeld{
		slots: []credSlot{
			{Label: "claudecode", Kind: kindSignIn, Store: "anthropic"},
			{Label: "codex", Kind: kindSignIn, Store: "openai", Held: true},
			{Label: "cursor", Kind: kindSignIn, Store: "cursor"},
			{Label: "opencode", Kind: kindSubscription, Store: "opencode", Held: true},
			anthropic,
			{Label: "openai", Kind: kindKey, Store: "openai"},
			{Label: "cursor", Kind: kindKey, Store: "cursor"},
		},
		did: map[string]closedRow{slotKey(anthropic): {mark: choiceui.MarkDone, label: "anthropic",
			outcome: storedText(anthropic, []choiceui.Row{{Field: true}}, 0, []string{"stored now"})}},
	})
	rep.setOperator(operatorSaid{name: "Executor", asked: true, defaulted: true})
	return rep, rec
}

// TheClosedChecklist: the result prints once, after the card: its heading, one row per step in run
// order with its mark at 2, its label at 4 and its outcome at 18, then each check still ! with its
// evidence, fix and note at 6, and the verdict last.
func TestTheClosedChecklistPrintsOnceInRunOrder(t *testing.T) {
	out := cardUI(t, 80)
	rep, rec := recordedWalk(t)
	if err := rep.close(rec, nil); err != nil {
		t.Fatal(err)
	}
	got := termtext.StripANSI(out.String())
	want := `── proveo init ── darwin/arm64 · sbx 0.50.0 ────────────────────────────────────

  – sbx install   kept 0.50.0 · ~/.docker/sbx/bin/sbx
  ✓ Docker login  signed in
  ✓ git identity  Ada Example <ada@example.invalid> · git's global config
  – GitHub        later
  – prompt kit    later
  ✓ codex         login held by sbx as openai
  ✓ opencode      token stored as opencode
  ✓ anthropic     key imported from this host's env
  – not held      claudecode, cursor sign-ins · openai, cursor keys
  ✓ operator      Executor (default)
  – sbx setup     nothing to import

  ! Binary version — update available: v9.9.9
      Download the latest release: https://example.invalid/sbx/v9.9.9

  ! SSH client config — an unmanaged sandbox Host stanza can shadow the
      managed configuration
      /home/op/.ssh/config:5: Host
      Remove or rename the reported Host *.sbx stanza, then run sbx setup ssh
      a run does not need this check; it affects IDE attach and Hermes
      Desktop, which connect with ` + "`ssh <name>.sbx`" + `

  ✓ host — ready to run
`
	if got != want {
		t.Errorf("the closed checklist at 80 columns:\n%s\nwant:\n%s", got, want)
	}
	if n := len(closedHead.FindAllString(got, -1)); n != 1 || len(headings(got)) != 0 {
		t.Errorf("headings: %d closed, %v sections; want the closed checklist's alone, once", n, headings(got))
	}
	for _, l := range strings.Split(got, "\n") {
		if w := len([]rune(l)); w > 80 {
			t.Errorf("a line is %d columns wide at 80: %q", w, l)
		}
	}
}

// An outcome wider than its room wraps at " · " back to column 18, and drops the separator there.
func TestAnOutcomeWrapsAtItsSeparatorsBackToColumn18(t *testing.T) {
	out := cardUI(t, 60)
	printRow(closedRow{mark: choiceui.MarkLater, label: "not held", outcome: "claudecode, cursor sign-ins · anthropic, openai, cursor keys"})
	want := "  – not held      claudecode, cursor sign-ins\n" +
		"                  anthropic, openai, cursor keys\n"
	if got := termtext.StripANSI(out.String()); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// The plain form, `--yes` with a pipe or no terminal, prints "<tag><label> — <outcome>" on one line,
// the detail indented 4, the same heading, and no escape codes.
func TestThePlainChecklistCarriesNoEscapes(t *testing.T) {
	out := captureUI(t)
	rep, rec := recordedWalk(t)
	rec.rows[rowGit] = rowState{mark: choiceui.MarkNeeds, outcome: "not set"}
	rep.say(rowGit, sandbox.Item{Warn: true, Text: "git identity — not set", Detail: []string{noIdentityHowTo}})
	if err := rep.close(rec, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.ContainsAny(got, "\x1b\r") {
		t.Errorf("plain output carries a terminal control:\n%q", got)
	}
	for _, re := range []string{
		`(?m)^── proveo init ── darwin/arm64 · sbx 0\.50\.0 ──+$`,
		`(?m)^sbx install — kept 0\.50\.0 · ~/\.docker/sbx/bin/sbx$`,
		`(?m)^ok: Docker login — signed in$`,
		`(?m)^warn: git identity — not set\n    ` + regexp.QuoteMeta(noIdentityHowTo) + `$`,
		`(?m)^GitHub — later$`,
		`(?m)^not held — claudecode, cursor sign-ins · openai, cursor keys$`,
		`(?m)^ok: operator — Executor \(default\)$`,
		`(?m)^sbx setup — nothing to import$`,
		`(?m)^warn: SSH client config — an unmanaged sandbox Host stanza can shadow the managed configuration\n    /home/op/\.ssh/config:5: Host$`,
	} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("plain output lacks %s:\n%s", re, got)
		}
	}
	if last := lastLine(got); last != "ok: host — ready to run" {
		t.Errorf("the last line = %q, want the verdict", last)
	}
	if n := strings.Count(got, "git identity"); n != 1 {
		t.Errorf("the result names git identity %d times, want once: its item restates its row:\n%s", n, got)
	}
}

// A step's items join its row when they restate it, and stand as needs-you items when they do not;
// a ✓ item says nothing a row does not.
func TestAStepsItemsJoinItsRowOrStandAlone(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rec := &initRecord{}
	rec.row(rowGh, choiceui.MarkNeeds, "gh is not on PATH")
	block := rep.block(rowGh)
	block.Item(sandbox.Item{Warn: true, Text: "gh — not on PATH", Detail: []string{"install it from " + ghInstallDocs}})
	block.Item(sandbox.Item{Warn: true, Text: "GitHub access — settings unreadable", Detail: []string{"bad json"}})
	rep.say(rowPath, sandbox.Item{Ok: true, Text: "~/.docker/sbx/bin — added to PATH in ~/.zshrc", Detail: []string{"a new shell picks it up"}})
	rec.row(rowPath, choiceui.MarkDone, "added to PATH in ~/.zshrc")
	if err := rep.close(rec, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, re := range []string{
		`(?m)^warn: GitHub — gh is not on PATH\n    install it from https://cli\.github\.com/$`,
		`(?m)^warn: GitHub access — settings unreadable\n    bad json$`,
		`(?m)^ok: PATH — added to PATH in ~/\.zshrc$`,
	} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the result lacks %s:\n%s", re, got)
		}
	}
	if strings.Contains(got, "gh — not on PATH") || strings.Contains(got, "a new shell") {
		t.Errorf("an item that restates its row, or a ✓ item, printed apart:\n%s", got)
	}
}

// A revisit judges the host into ONE readiness set, and each row prints once, as its latest run left it.
func TestARevisitPrintsOneReadinessSetAndEachRowOnce(t *testing.T) {
	isolatedInitHost(t)
	t.Setenv("STUB_DIAGNOSE", spared)
	t.Setenv("GIT_AUTHOR_NAME", "Ada")
	t.Setenv("GIT_AUTHOR_EMAIL", "ada@example.invalid")
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rec := &initRecord{}
	ctx := applyCtx{plan: rep.applied, applied: rep.applied, rep: rep}
	for _, d := range []initDecisions{{Install: installSkip, Baseline: sbx.BaselineBalanced, Git: true}, {Install: installSkip}} {
		if err := runApply(streamView{}, applyPlan(d, ctx), rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("STUB_POLICY_ALLOWED", "true")
	if err := runApply(streamView{}, applyPlan(initDecisions{Install: installSkip, Baseline: sbx.BaselineBalanced, Git: true}, ctx), rec, nil); err != nil {
		t.Fatal(err)
	}
	if err := rep.close(rec, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if n := strings.Count(got, "SSH client config — "); n != 1 {
		t.Errorf("the readiness printed %d times, want once:\n%s", n, got)
	}
	for re, want := range map[string]int{`(?m)^ok: network — balanced$`: 1, `(?m)^ok: git identity — Ada <ada@example\.invalid>$`: 1} {
		if n := len(regexp.MustCompile(re).FindAllString(got, -1)); n != want {
			t.Errorf("%s printed %d times, want %d:\n%s", re, n, want, got)
		}
	}
	if n := len(closedHead.FindAllString(got, -1)); n != 1 || len(headings(got)) != 0 {
		t.Errorf("want the closed checklist's heading once and no section heading:\n%s", got)
	}
}

// Two forward passes through the credentials page leave one set of rows, the latest.
func TestTheCredentialsRowsComeFromTheLatestPass(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.setCredentials(credsHeld{slots: []credSlot{{Label: "codex", Kind: kindSignIn, Store: "openai", Held: true}}})
	rep.setCredentials(credsHeld{slots: []credSlot{{Label: "opencode", Kind: kindSubscription, Store: "opencode", Held: true}}})
	_ = rep.close(&initRecord{}, nil)
	got := out.String()
	if strings.Contains(got, "codex") || strings.Count(got, "ok: opencode — token stored as opencode") != 1 {
		t.Errorf("want the latest pass's rows alone:\n%s", got)
	}
}

// F3: the result says what waits, then the readiness items, and closes on the verdict.
func TestTheResultSaysWhatWaitsAndClosesOnTheVerdict(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{skipGh: true})
	items, _ := checkItems([]sbx.Check{sshClientConfig})
	rep.readiness, rep.ready, rep.judged = items, true, true
	rec := &initRecord{}
	rec.leaveUnchosen(initDecisions{Login: true, Wizard: true})
	if err := rep.close(rec, nil); err != nil {
		t.Fatalf("close = %v, want nil on success", err)
	}
	got := out.String()
	for _, re := range []string{`(?m)^GitHub — later · --skip-gh$`, `(?m)^git identity — later$`, `(?m)^prompt kit — later$`,
		`(?m)^warn: SSH client config — an unmanaged sandbox Host stanza`, `(?m)^\s+/home/op/\.ssh/config:5: Host$`,
		`(?m)^\s+a run does not need this check; it affects IDE attach`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the result lacks %s:\n%s", re, got)
		}
	}
	if last := lastLine(got); last != "ok: host — ready to run" {
		t.Errorf("the last line = %q, want the verdict", last)
	}
	for _, gone := range []string{"done:", "next:", "later:", "not run:", "proveo init`", "does not manage", "checks passed", "credentials"} {
		if strings.Contains(got, gone) {
			t.Errorf("the result still says %q:\n%s", gone, got)
		}
	}
}

// F3 and UNREACHED: a run blocker closes on one × host line, names what never ran as rows, and main
// prints nothing more.
func TestARunBlockerClosesOnOneHostVerdictAndNamesWhatNeverRan(t *testing.T) {
	isolatedInitHost(t)
	t.Setenv("STUB_DIAGNOSE", blocker)
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rec := &initRecord{}
	err := runApply(streamView{}, applyPlan(initDecisions{Install: installSkip}, applyCtx{plan: rep.applied, applied: rep.applied, rep: rep}), rec, nil)
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
	for _, re := range []string{`(?m)^error: Daemon — not running$`, `(?m)^\s+Start Docker Sandboxes$`, `(?m)^credentials — not run$`, `(?m)^operator — not run$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the result lacks %s:\n%s", re, got)
		}
	}
}

// P8 and F3: a quit closes on ONE ! line, with the verdict a finished verify reached dim under it.
func TestAQuitClosesOnOneWarningWithTheVerdictDim(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.ready = true
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
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "\n\nwarn: init — quit at page 3/3\n    host ready to run") {
		t.Errorf("want the quit as the closing line after a blank row and the verdict dim under it:\n%s", got)
	}
	if strings.Contains(got, "ok: host") || strings.Contains(got, "error:") {
		t.Errorf("a quit printed a second verdict or a ×:\n%s", got)
	}
}

// A quit on page 1 lists the checks page 1 showed as !, as a quit on a later page does.
func TestAQuitOnPageOneListsTheChecksItShowed(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.heard([]sbx.Check{sshClientConfig}, nil)
	_ = rep.close(&initRecord{}, fmt.Errorf("%w at page 1/4", errInitLeft))
	got := out.String()
	at, quit := strings.Index(got, "warn: SSH client config — an unmanaged sandbox Host stanza"), strings.Index(got, "warn: init — quit at page 1/4")
	if at < 0 || quit < at {
		t.Errorf("want the ! check page 1 showed, then the quit:\n%s", got)
	}
}

// A walk that ends with no verdict from sbx diagnose still closes on its readiness line, last.
func TestAWalkSbxGaveNoVerdictClosesOnReadinessNotChecked(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	_ = verifyWith(sbx.Binary+"-absent", rep, nil, errors.New("sbx diagnose: unexpected output"))
	if err := rep.close(&initRecord{}, nil); err != nil {
		t.Fatalf("close = %v, want nil", err)
	}
	got := out.String()
	if last := lastLine(got); last != "warn: host — readiness not checked" {
		t.Errorf("the last line = %q, want the readiness line:\n%s", last, got)
	}
	if !strings.Contains(got, "warn: sbx diagnose — gave no answer") {
		t.Errorf("the result lost why it has no verdict:\n%s", got)
	}
}

// An item about a credential its row names joins that row: the row takes the worse mark, the
// item's words and its detail, and no item stands for it apart.
func TestACredentialItemJoinsTheRowItNames(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.ready = true
	anthropic := credSlot{Label: "anthropic", Kind: kindKey, Store: "anthropic", Held: true}
	rep.setCredentials(credsHeld{slots: []credSlot{anthropic},
		did:   map[string]closedRow{slotKey(anthropic): {mark: choiceui.MarkDone, label: "anthropic", outcome: "key stored as anthropic"}},
		items: []sandbox.Item{{Warn: true, Text: "anthropic — stored, not spendable", Detail: []string{"the provider answered HTTP 401"}}}})
	_ = rep.close(&initRecord{}, nil)
	got := out.String()
	if !regexp.MustCompile(`(?m)^warn: anthropic — key stored as anthropic · stored, not spendable\n    the provider answered HTTP 401$`).MatchString(got) {
		t.Errorf("the item did not join its row:\n%s", got)
	}
	if strings.Count(got, "anthropic — ") != 1 {
		t.Errorf("the credential reads twice:\n%s", got)
	}
}

// The result heads its output: a blank row sits above the heading only under lines init printed
// before it, such as a hand-off the card lent the terminal to.
func TestTheHeadingTakesABlankRowOnlyUnderEarlierLines(t *testing.T) {
	for _, c := range []struct {
		name   string
		before func(rep *initReport)
		blank  bool
	}{
		{"first line", func(*initReport) {}, false},
		{"under a step line", func(*initReport) { ui.Asyncf("step 1/2 · install sbx") }, true},
		{"after a hand-off on the card", func(rep *initReport) { rep.handed = true }, true},
	} {
		out := captureUI(t)
		rep := testReport(t, initOptions{})
		rep.ready = true
		c.before(rep)
		_ = rep.close(&initRecord{}, nil)
		got := out.String()
		head := regexp.MustCompile(`(?m)^── proveo init ── `).FindStringIndex(got)
		if head == nil {
			t.Fatalf("%s: no heading:\n%s", c.name, got)
		}
		if blank := strings.HasSuffix(got[:head[0]], "\n\n") || head[0] == 1 && got[0] == '\n'; blank != c.blank {
			t.Errorf("%s: a blank row above the heading = %v, want %v:\n%q", c.name, blank, c.blank, got)
		}
	}
}

// F3: every ending after a passed verify keeps "host ready to run" as its last dim line; a failure no
// row names keeps its error on the closing line.
func TestEveryEndingAfterAPassedVerifyKeepsTheVerdictLast(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{stepErr{label: labelCredentials, err: errors.New("form broke")}, "error: init — stopped at credentials\n    form broke\n    host ready to run"},
		{stepErr{label: labelOperator, err: errors.New("operator.yml unreadable")}, "error: init — stopped at operator\n    operator.yml unreadable\n    host ready to run"},
		{stepErr{label: labelCredentials, err: handoffErr{err: errors.New("signal: interrupt"), interrupted: true}},
			"warn: init — stopped at credentials\n    Ctrl+C ended it\n    host ready to run"},
		{stepErr{label: rowGh, err: handoffErr{err: errors.New("exit status 1")}}, "warn: init — stopped at GitHub\n    host ready to run"},
		{errors.New("the setup form broke"), "error: init — stopped\n    the setup form broke\n    host ready to run"},
	} {
		out := captureUI(t)
		rep := testReport(t, initOptions{})
		rep.ready = true
		_ = rep.close(&initRecord{}, tc.err)
		if got := out.String(); !strings.HasSuffix(strings.TrimRight(got, "\n"), tc.want) {
			t.Errorf("%v: want the ending, then the verdict dim and last:\n%s", tc.err, got)
		}
	}
}

// An error that names no step, such as page 1's own form error or a prefix it chose that has no
// plan, closes on "× init — stopped" with the error dim, and main prints nothing more.
func TestAnErrorWithNoStepClosesOnInitStopped(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	ret := rep.close(&initRecord{}, errors.New("no Docker Sandboxes build for plan9/arm"))
	if code, ok := exitCode(ret); !ok || code != 1 {
		t.Errorf("close = %v, want Reported (exit 1, main prints nothing)", ret)
	}
	if got := out.String(); !regexp.MustCompile(`── proveo init ── darwin/arm64 ──+\n\n\nerror: init — stopped\n    no Docker Sandboxes build for plan9/arm\n*\z`).MatchString(got) {
		t.Errorf("want the heading, then the fallback ending with the error dim:\n%q", got)
	}
}

// HAND-OFF FAILURE: the program's own error stands; init ends on where it stopped, with no × and no
// restated error, and the steps after it read not run.
func TestAHandOffFailureEndsOnWhereInitStopped(t *testing.T) {
	isolatedInitHost(t)
	t.Setenv("STUB_LOGIN_EXIT", "1")
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rec := &initRecord{}
	login := applyStep{name: actLogin, label: rowLogin, kind: stepSilent, run: func() error {
		return applyLogin(loginStage{
			auth:    func() sbx.Auth { return sbx.AuthOut },
			confirm: func() sbx.Auth { t.Error("a failed login was confirmed"); return sbx.AuthOut },
			login:   func() error { return handFramed(command(sbx.Binary, sbx.LoginArgs()...), "Docker not signed in") },
			tty:     true,
			rep:     rep,
		})
	}}
	gh := applyStep{name: actGitHub, label: rowGh, kind: stepSilent, run: func() error { t.Error("GitHub ran after the login failed"); return nil }}
	err := runApply(streamView{}, []applyStep{login, gh}, rec, nil)
	var failed stepErr
	if errors.As(err, &failed) {
		rec.unreached(failed.rest...)
	}
	ret := rep.close(rec, err)
	if code, ok := exitCode(ret); !ok || code != 1 {
		t.Errorf("close = %v, want Reported", ret)
	}
	got := out.String()
	if last := lastLine(got); last != "warn: init — stopped at Docker login" {
		t.Errorf("the last line = %q:\n%s", last, got)
	}
	if strings.Contains(got, "error:") {
		t.Errorf("init restated the program's failure as its own:\n%s", got)
	}
	if !strings.Contains(got, "\n── sbx login ── Docker not signed in ──") {
		t.Errorf("the login was not framed:\n%s", got)
	}
	for _, re := range []string{`(?m)^warn: Docker login — stopped · exit status 1$`, `(?m)^GitHub — not run$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the result lacks %s:\n%s", re, got)
		}
	}
}

// THE SBX STEPS RUN ONLY WITH WORK: sbx setup opens after the needs-you items and before the
// verdict, only on a terminal, after a walk that completed, when the page chose "after init" and
// the probe found work; otherwise its row says why, and "later" only when the probe found work.
func TestSbxSetupRunsAfterTheResultOnlyWhenOffered(t *testing.T) {
	calls := isolatedInitHost(t)
	quit := fmt.Errorf("%w at page 3/4", errInitLeft)
	failure := stepErr{label: labelCredentials, err: errors.New("form broke")}
	ctrlC := stepErr{label: rowLogin, err: handoffErr{err: errors.New("signal: interrupt"), interrupted: true}}
	for _, c := range []struct {
		name    string
		envOnly string
		asked   askedHow
		wizard  bool
		err     error
		runs    bool
		later   string
		row     string
	}{
		{"offered, on a terminal, after init", "1", askedOnPages, true, nil, true, "", ""},
		{"nothing to import", "0", askedOnPages, true, nil, false, "", "sbx setup — nothing to import"},
		{"offered, answered later", "2", askedOnPages, false, nil, false, "2 env keys sbx's store lacks · `sbx setup` on a terminal",
			"sbx setup — later · 2 env keys sbx's store lacks · `sbx setup` on a terminal"},
		{"offered, under --yes", "1", askedByFlag, false, nil, false, "1 env key sbx's store lacks · `sbx setup` on a terminal",
			"sbx setup — later · 1 env key sbx's store lacks · `sbx setup` on a terminal"},
		{"offered, after a quit", "1", askedOnPages, true, quit, false, "1 env key sbx's store lacks · `sbx setup` on a terminal",
			"sbx setup — later · 1 env key sbx's store lacks · `sbx setup` on a terminal"},
		{"nothing to import, after a failure", "0", askedOnPages, true, failure, false, "", ""},
		{"offered, after a Ctrl+C", "1", askedOnPages, false, ctrlC, false, "", ""},
	} {
		t.Setenv("STUB_ENV_ONLY", c.envOnly)
		if err := os.WriteFile(calls, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		out := captureUI(t)
		rep := testReport(t, initOptions{})
		rep.probe = func() (sbx.SetupOffer, error) { return sbx.SetupOfferAt(sbx.Binary, os.Getenv) }
		rep.ready, rep.asked, rep.wizard = true, c.asked, c.wizard
		items, _ := checkItems([]sbx.Check{sshClientConfig})
		rep.readiness, rep.judged = items, true
		rec := &initRecord{}
		rec.leaveUnchosen(initDecisions{Wizard: c.wizard, Login: true, Gh: true, Git: true, Prompt: true})
		_ = rep.close(rec, c.err)
		got := out.String()
		b, _ := os.ReadFile(calls)
		if ran := regexp.MustCompile(`(?m)^setup$`).Match(b); ran != c.runs {
			t.Errorf("%s: sbx setup ran = %v, want %v; sbx saw:\n%s", c.name, ran, c.runs, b)
		}
		if c.runs {
			frame := regexp.MustCompile(`(?m)^── sbx setup ── .+ ──+$`).FindStringIndex(got)
			needs, finished := strings.Index(got, "warn: SSH client config"), strings.Index(got, "ok: sbx setup — finished")
			if frame == nil || needs < 0 || frame[0] < needs || finished < frame[1] {
				t.Errorf("%s: want the needs-you items, then the frame, then the outcome:\n%s", c.name, got)
			}
			if !strings.HasSuffix(got, "ok: sbx setup — finished\n\nok: host — ready to run\n") {
				t.Errorf("%s: want the outcome row, a blank row, then the verdict last:\n%q", c.name, got)
			}
		} else if strings.Contains(got, "── sbx setup") {
			t.Errorf("%s: a setup that does not run printed its frame:\n%s", c.name, got)
		}
		if later, _ := rec.lines(); slices.Contains(later, rowWizard) != (c.later != "") || c.later != "" && !slices.Equal(rec.later[rowWizard], []string{c.later}) {
			t.Errorf("%s: later = %q (%q), want sbx setup later with %q", c.name, later, rec.later[rowWizard], c.later)
		}
		if c.row != "" && !regexp.MustCompile(`(?m)^`+regexp.QuoteMeta(c.row)+`$`).MatchString(got) || c.row == "" && strings.Contains(got, "sbx setup — ") && !c.runs {
			t.Errorf("%s: want the row %q:\n%s", c.name, c.row, got)
		}
		if probed := strings.Contains(string(b), "secret ls"); probed == interrupted(c.err) {
			t.Errorf("%s: the setup probe ran = %v; it runs unless a Ctrl+C ended init:\n%s", c.name, probed, b)
		}
	}
}

// THE SBX STEPS RUN ONLY WITH WORK: the result names Docker login once. A signed-out readiness
// item joins the Docker login row as its fix; a login that left Docker signed out already said so.
func TestTheResultNamesDockerLoginOnce(t *testing.T) {
	signedOut, _ := checkItems([]sbx.Check{{Name: sbx.AuthenticationCheck, Status: "fail", Message: "not signed in"}})
	for _, c := range []struct {
		name        string
		loginChosen bool
		warned      bool
	}{
		{"no login ran", false, false},
		{"a login left Docker signed out", true, true},
	} {
		out := captureUI(t)
		rep := testReport(t, initOptions{})
		rep.ready, rep.readiness, rep.judged, rep.loginWarned = true, signedOut, true, c.warned
		rep.heard([]sbx.Check{{Name: sbx.AuthenticationCheck, Status: "fail", Message: "not signed in"}}, nil)
		rec := &initRecord{}
		rec.leaveUnchosen(initDecisions{Login: c.loginChosen, Wizard: true, Gh: true, Git: true, Prompt: true})
		if c.warned {
			rec.row(rowLogin, choiceui.MarkNeeds, "not signed in")
			rep.say(rowLogin, sandbox.Item{Warn: true, Text: rowLogin + " — not signed in", Detail: []string{loginFix}})
		}
		if err := rep.close(rec, nil); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if n := strings.Count(got, "Docker login"); n != 1 {
			t.Errorf("%s: the result names Docker login %d times, want once:\n%s", c.name, n, got)
		}
		if !regexp.MustCompile(`(?m)^warn: Docker login — not signed in\n    ` + regexp.QuoteMeta(loginFix) + `$`).MatchString(got) {
			t.Errorf("%s: the Docker login row lost its fix line:\n%s", c.name, got)
		}
	}
}

// never is a page that must not run once init stopped.
func never(t *testing.T) initStep {
	return func(choiceui.Step) (stepResult, error) {
		t.Error("a page ran after init stopped")
		return stepNext, nil
	}
}

// HAND-OFF FAILURE: a failed `gh auth login` ends init where it stopped; the pages after it read
// not run, and nothing restates gh's failure.
func TestAFailedGhAuthLoginEndsInitOnWhereItStopped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing gh is a shell")
	}
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rec := &initRecord{}
	g, _ := stubAuth(nil, true, "", errors.New("no session"), func() error { return handOff(exec.Command("sh", "-c", "exit 1")) })
	setup := func(choiceui.Step) (stepResult, error) {
		err := runApply(streamView{}, []applyStep{
			{name: actGitHub, label: rowGh, kind: stepSilent, run: func() error {
				return applyGitHub(ghStage{auth: *g, tty: true, out: rep.block(rowGh), setenv: func(string, string) error { return nil }})
			}},
			{name: actPrompt, label: rowPrompt, kind: stepSilent, run: func() error {
				t.Error("the prompt kit ran after a failed sign-in")
				return nil
			}},
		}, rec, nil)
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
	for _, re := range []string{`(?m)^prompt kit — not run$`, `(?m)^credentials — not run$`, `(?m)^operator — not run$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the result lacks %s:\n%s", re, got)
		}
	}
	if strings.Contains(got, "error:") || strings.Contains(got, "did not complete") {
		t.Errorf("init repeated gh's failure:\n%s", got)
	}
}

// UNREACHED: a Ctrl+C that ends a sign-in on the credentials page reads stopped on its row, not run
// on the rows answered after it, and not run on the operator page.
func TestACredentialsPageStopNamesWhatItKeptFromRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT to a child is a unix test")
	}
	isolatedInitHost(t)
	out := captureUI(t)
	rep := testReport(t, initOptions{})
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
	ctrlC := func(_ credSlot, argv []string) error {
		if argv[1] == "run" {
			return handOff(exec.Command("sh", "-c", "kill -INT $$"))
		}
		return nil
	}
	held := func(string) (bool, bool) { return true, true }
	credentials := func(choiceui.Step) (stepResult, error) {
		did, items, err := applyCredentials(streamView{}, slots, rows, ctrlC, held, rec)
		rep.setCredentials(credsHeld{slots: slots, did: did, items: items})
		return stepNext, stopAt(labelCredentials, err)
	}
	err := runInitSteps(false, rec.pages([]initPage{{run: func(choiceui.Step) (stepResult, error) { return stepNext, nil }},
		{label: labelCredentials, run: credentials}, {label: labelOperator, run: never(t)}}))
	ret := rep.close(rec, err)
	if code, ok := exitCode(ret); !ok || code != 130 {
		t.Errorf("close = %v (%d), want exit 130", ret, code)
	}
	got := out.String()
	for _, re := range []string{`(?m)^warn: claudecode — stopped · Ctrl\+C ended it$`, `(?m)^codex — not run$`, `(?m)^anthropic — not run$`,
		`(?m)^not held — cursor sign-in · groq key$`, `(?m)^operator — not run$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the result lacks %s:\n%s", re, got)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "warn: init — stopped at credentials\n    Ctrl+C ended it") {
		t.Errorf("want the stop and its cause last:\n%s", got)
	}
}

// UNREACHED: a distro package this release does not ship fails the install step, and the chosen
// steps after it read not run.
func TestAMissingDistroPackageStopsAtTheInstallAndNamesWhatNeverRan(t *testing.T) {
	calls := isolatedInitHost(t)
	out := captureUI(t)
	host := sbx.Host{OS: "linux", Arch: "amd64", Distro: "rocky", Version: "9"}
	rep := newInitReport(initOptions{}, host, nil, sbx.Plan{})
	rec := &initRecord{}
	d := initDecisions{Install: installPackaged, Login: true}
	rec.leaveUnchosen(d)
	err := runApply(streamView{}, applyPlan(d, applyCtx{applied: appliedPlan(sbx.Plan{}, true), host: host, rep: rep}), rec, nil)
	var failed stepErr
	if !errors.As(err, &failed) || failed.label != rowInstall {
		t.Fatalf("runApply = %v, want the install step's failure", err)
	}
	rec.unreached(failed.rest...)
	_ = rep.close(rec, err)
	got := out.String()
	for _, re := range []string{`(?m)^Docker login — not run$`,
		`(?m)^error: sbx install — this release ships no distro package for linux/amd64 · rocky 9$`,
		`(?m)^error: init — stopped at sbx install$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the result lacks %s:\n%s", re, got)
		}
	}
	if b, _ := os.ReadFile(calls); strings.Contains(string(b), "login") {
		t.Errorf("sbx login ran after the install failed:\n%s", b)
	}
}

// A failure proveo itself found ends on where init stopped; its error stays on the closing line
// when no row names it.
func TestAFailureProveoFoundClosesOnItsStep(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	ret := rep.close(&initRecord{}, stepErr{label: rowInstall, err: errors.New("/usr/local is not writable by this user — re-run with `sudo -E`")})
	if _, ok := exitCode(ret); !ok {
		t.Errorf("close = %v, want Reported", ret)
	}
	if got := out.String(); !strings.HasSuffix(strings.TrimRight(got, "\n"), "error: init — stopped at sbx install\n    /usr/local is not writable by this user\n    re-run with `sudo -E`") {
		t.Errorf("want where init stopped, then the error dim:\n%s", got)
	}
	out = captureUI(t)
	rec := &initRecord{}
	rec.row(rowInstall, choiceui.MarkFailed, "/usr/local is not writable by this user — re-run with `sudo -E`")
	_ = rep.close(rec, stepErr{label: rowInstall, err: errors.New("/usr/local is not writable by this user — re-run with `sudo -E`")})
	if got := out.String(); strings.Count(got, "not writable") != 1 || !strings.HasSuffix(strings.TrimRight(got, "\n"), "error: init — stopped at sbx install") {
		t.Errorf("a failure its row names must not repeat on the closing line:\n%s", got)
	}
}

// NO TTY: a question init could not ask keeps one dim how-to under its row; --yes, an operator act,
// keeps none.
func TestOnlyAnInitWithNoTerminalKeepsAHowToUnderWhatItCouldNotAsk(t *testing.T) {
	for _, noTerminal := range []bool{true, false} {
		out := captureUI(t)
		asked := askedByFlag
		if noTerminal {
			asked = askedNoTerminal
		}
		rep := testReport(t, initOptions{yes: !noTerminal})
		rep.asked = asked
		mark, text := applyGitIdentity(gitStage{
			resolve:  func() gitidentity.Identity { return gitidentity.Identity{} },
			lookPath: func(string) (string, error) { return "/usr/bin/git", nil },
			setGlobal: func(string, string) error {
				t.Error("an unasked identity was written")
				return nil
			},
			out:        rep.block(rowGit),
			noTerminal: noTerminal,
		})
		rec := &initRecord{}
		rec.row(rowGit, mark, text)
		rep.setCredentials(credsHeld{slots: []credSlot{{Label: "groq", Kind: kindKey, Store: "groq"}},
			howTo: asked.howTo("`proveo init` from a terminal provisions them")})
		rep.setOperator(operatorSaid{name: "Executor", defaulted: true, howTo: asked.howTo("`proveo init` from a terminal changes it")})
		_ = rep.close(rec, nil)
		got := out.String()
		for _, howTo := range []string{noIdentityHowTo, "`proveo init` from a terminal provisions them", "`proveo init` from a terminal changes it"} {
			if strings.Contains(got, howTo) != noTerminal {
				t.Errorf("noTerminal=%v: how-to %q present=%v:\n%s", noTerminal, howTo, !noTerminal, got)
			}
		}
		if !strings.Contains(got, "warn: git identity — not set") || !strings.Contains(got, "\noperator — Executor (default)") {
			t.Errorf("noTerminal=%v: the identity's ! or the name's row is missing:\n%s", noTerminal, got)
		}
	}
}

// A tarball install's row names the version and where sbx lives, and says when no digest covered it.
func TestATarballInstallsRowNamesItsVersionAndPlace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub installer is a shell")
	}
	isolatedInitHost(t)
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
	for _, verified := range []bool{true, false} {
		out := captureUI(t)
		rep := newInitReport(initOptions{}, sbx.Host{OS: "darwin", Arch: "arm64"}, nil, plan)
		if err := placeRelease(plan, t.TempDir(), "abc", verified, applyCtx{rep: rep}); err != nil {
			t.Fatal(err)
		}
		rec := &initRecord{}
		rec.row(rowInstall, choiceui.MarkDone, "installed "+sbx.Release)
		_ = rep.close(rec, nil)
		want := "ok: sbx install — installed " + sbx.Release + " · " + bin
		if !verified {
			want += " · no published digest"
		}
		if got := out.String(); !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(want) + `$`).MatchString(got) {
			t.Errorf("verified=%v: the install row is not %q:\n%s", verified, want, got)
		}
	}
}

// The MSI's install line names its installer, and its row stays without a prefix's path.
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
	if err := placeRelease(sbx.Plan{Note: plan.Note}, "", "abc", false, applyCtx{rep: rep}); err != nil {
		t.Fatal(err)
	}
	if items := rep.about[rowInstall]; len(items) != 1 || items[0].Text != "sbx "+sbx.Release+" — installed" || !rep.unverified {
		t.Errorf("the MSI's install items = %+v, unverified %v", items, rep.unverified)
	}
	t.Setenv("PATH", t.TempDir())
	if err := verify(sbx.Binary, rep); err != nil || len(rep.readiness) != 1 || rep.readiness[0].Text != "sbx — not on this shell's PATH" {
		t.Errorf("verify of an sbx this process cannot find = %v %+v, want the item that a new shell finds it", err, rep.readiness)
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
	ret := rep.close(&initRecord{}, stepErr{label: rowLogin, err: handOff(exec.Command("sh", "-c", "kill -INT $$"))})
	if code, ok := exitCode(ret); !ok || code != 130 {
		t.Errorf("close = %v (%d), want exit 130 with nothing printed by main", ret, code)
	}
	if got := out.String(); !strings.HasSuffix(strings.TrimRight(got, "\n"), "warn: init — stopped at Docker login\n    Ctrl+C ended it") {
		t.Errorf("want the stop and its cause:\n%s", got)
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

// P7: the operator's name is a fact, never a ✓ line.
func TestTheOperatorsNameIsAFact(t *testing.T) {
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	rep.setOperator(operatorSaid{name: "Ada", asked: true})
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
