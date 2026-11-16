// SPEC: _spec/internal/ui/step-lines.puml
package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
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
			"add /opt/proveo-test-prefix/bin to PATH", "verify sbx (sbx diagnose)", actLogin,
			"bind the network baseline: balanced", "set the sandbox git identity", "connect GitHub", "install the prompt kit"}},
		{"a revisit with everything later still verifies", initDecisions{Install: installSkip}, applyFixture(),
			[]string{"verify sbx (sbx diagnose)"}},
		{"print-only PATH is not a step", initDecisions{Install: installSkip, AddPath: false, Login: true}, applyFixture(),
			[]string{"verify sbx (sbx diagnose)", actLogin}},
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
		stepSilent: {actLogin, actGit, actGitHub, actPrompt},
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
	rec := &initRecord{}
	err := runApply(streamView{}, []applyStep{
		step("download sbx", "download sbx", rowInstall, stepLive, nil),
		step("", "", "", stepLive, nil),
		step("sign in to Docker", "sbx login — signing in to Docker", rowLogin, stepHand, boom),
		step("install the prompt kit", "", rowPrompt, stepSilent, nil),
		step("verify sbx", "", labelVerify, stepQuiet, nil),
	}, rec, nil)

	if !slices.Equal(rec.ran, []string{rowInstall}) {
		t.Errorf("ran %q; a step that failed must not be reported as run", rec.ran)
	}
	if got := rec.rows[rowLogin]; got.mark != choiceui.MarkFailed || got.outcome != "exit status 1" {
		t.Errorf("the failed row reads %+v, want × and its error", got)
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

// A quiet step leaves nothing in plain output, and a silent one nothing anywhere: the closed
// checklist says what each came to.
func TestQuietAndSilentStepsLeaveNoPlainLine(t *testing.T) {
	var term bytes.Buffer
	prev := ui.Default
	t.Cleanup(func() { ui.Default = prev })
	ui.Default = ui.New(&term)
	if err := runApply(streamView{}, []applyStep{
		{name: "set the sandbox git identity", label: rowGit, kind: stepSilent, run: func() error { return nil }},
		{name: "verify sbx (sbx diagnose)", label: labelVerify, kind: stepQuiet, run: func() error { return nil }},
	}, nil, nil); err != nil {
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
		captureUI(t)
		rep := newInitReport(initOptions{}, sbx.Host{OS: "darwin", Arch: "arm64"}, nil, plan)
		steps := applyPlan(d, applyCtx{plan: plan, applied: plan, rep: rep})
		if err := runApply(streamView{}, steps, &initRecord{}, nil); err != nil {
			t.Fatal(err)
		}
		var said []string
		for _, it := range rep.about[rowPath] {
			said = append(said, it.Text)
		}
		return settle(d, steps, plan), stepNames(steps), strings.Join(said, "\n")
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
	if err := runApply(streamView{}, applyPlan(initDecisions{Install: installSkip, Login: true},
		applyCtx{plan: rep.applied, applied: rep.applied, rep: rep}), nil, nil); err != nil {
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
		var said []string
		for _, it := range rep.about[rowLogin] {
			switch {
			case it.Ok:
				said = append(said, "ok: "+it.Text)
			case it.Warn:
				said = append(said, "warn: "+it.Text)
			default:
				continue
			}
			for _, d := range it.Detail {
				said = append(said, "    "+d)
			}
		}
		if got := strings.Join(said, "\n"); got != c.says {
			t.Errorf("%s: said %q of the Docker login row, want %q", c.name, got, c.says)
		}
		if unchecked := len(rep.about[rowLogin]) == 1 && rep.about[rowLogin][0].Text == "Docker login — not checked"; unchecked != (c.before == sbx.AuthUnknown) {
			t.Errorf("%s: Docker login items = %+v", c.name, rep.about[rowLogin])
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

// applyPlan is pages 1 and 2's steps for d, as one walk runs them: sbx's, then git's.
func applyPlan(d initDecisions, c applyCtx) []applyStep {
	return append(applySbx(d, c), applyGit(d, c, gitAnswer{})...)
}

// signedOut is sbx diagnose on a host whose Docker sign-in is gone.
const signedOut = `{"checks":[{"name":"virtualisation","status":"pass","message":"supported"},` +
	`{"name":"Authentication","status":"fail","message":"not signed in","hint":"Run: sbx login"}]}`

// onCard is a card view over a fake session with page n up.
func onCard(n int) (*cardView, *fakeSession) {
	s := newFakeSession()
	v := newCardView(s)
	v.page = &choiceui.Form{Title: "page", Step: initStepAt(n)}
	return v, s
}

// tarballPlan is a release served by srv whose one install step copies the stub sbx into the prefix.
func tarballPlan(t *testing.T, srv *httptest.Server) sbx.Plan {
	t.Helper()
	stub, err := exec.LookPath("sbx")
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(t.TempDir(), "prefix")
	bin := filepath.Join(prefix, "bin", "sbx")
	return sbx.Plan{Prefix: prefix, Bin: bin, URL: srv.URL + "/sbx.tar.gz", Asset: "sbx.tar.gz",
		Steps: []sbx.Step{{What: "place sbx", Argv: []string{"sh", "-c", `mkdir -p "$(dirname "$BIN")" && cp "$STUB" "$BIN" && echo placed`},
			Env: []string{"BIN=" + bin, "STUB=" + stub}}}}
}

// RUN IN PLACE: page 1's Enter runs its rows in row order on the card — sbx install, PATH, the
// checks, Docker login — each running, then its outcome; the install's output stays off the screen.
func TestPageOneEnterRunsItsRowsInRowOrder(t *testing.T) {
	isolatedInitHost(t)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("STUB_DIAGNOSE", signedOut)
	out := captureUI(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("tarball")) }))
	defer srv.Close()
	plan := tarballPlan(t, srv)
	rep := newInitReport(initOptions{}, sbx.Host{OS: "darwin", Arch: "arm64"}, nil, plan)
	v, s := onCard(1)
	d := initDecisions{Install: installTarball, Prefix: plan.Prefix, AddPath: true, Login: true}
	steps := applySbx(d, applyCtx{plan: plan, applied: plan, download: t.TempDir(), host: sbx.Host{OS: "darwin", Arch: "arm64"}, rep: rep, view: v})
	if err := runApply(v, steps, nil, nil); err != nil {
		t.Fatalf("page 1's run = %v\n%s", err, strings.Join(s.events, "\n"))
	}
	if got := s.with("plan"); !slices.Equal(got, []string{"plan sbx install, PATH, checks, Docker login"}) {
		t.Errorf("plan = %q", got)
	}
	var running []string
	for _, e := range s.with("status") {
		if strings.Contains(e, " ◐ ") {
			running = append(running, strings.Fields(e)[1])
		}
	}
	if want := []string{"sbx", "sbx", "PATH", "checks", "Docker"}; !slices.Equal(running, want) {
		t.Errorf("rows ran in the order %q, want %q:\n%s", running, want, strings.Join(s.events, "\n"))
	}
	for _, want := range []string{"status sbx install ✓ installed " + sbx.Release, "status Docker login ! not signed in"} {
		if !slices.Contains(s.events, want) {
			t.Errorf("no %q among:\n%s", want, strings.Join(s.events, "\n"))
		}
	}
	if !slices.Contains(s.events, "edit") {
		t.Error("the check rows never redrew from the diagnose that ran after the install")
	}
	if strings.Contains(out.String(), "placed") {
		t.Errorf("the install step's output reached the stream:\n%s", out.String())
	}
}

// RUN IN PLACE: page 2's Enter runs git identity, then GitHub with its sub-page, then the prompt
// kit; GitHub's sub-page stands over the page, and the page comes back to finish its run.
func TestPageTwoEnterRunsGitIdentityThenGitHubThenPromptKit(t *testing.T) {
	isolatedInitHost(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv(ghTokenEnvVar, "")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	captureUI(t)
	all := initDecisions{Git: true, Gh: true, Prompt: true}
	if got := planRows(applyGit(all, applyCtx{}, gitAnswer{})); !slices.Equal(got, []string{rowGit, rowGh, rowPrompt}) {
		t.Errorf("page 2 plans %q, want git identity, GitHub, then the prompt kit", got)
	}
	rep := newInitReport(initOptions{}, sbx.Host{OS: "darwin", Arch: "arm64"}, nil, sbx.Plan{})
	v, s := onCard(2)
	s.script = []choiceui.Outcome{choiceui.Back}
	d := initDecisions{Git: true, Gh: true}
	steps := applyGit(d, applyCtx{host: sbx.Host{OS: "darwin", Arch: "arm64"}, rep: rep, view: v,
		frame: subFrame{at: initStepAt(2)}}, gitAnswer{name: "Ada", email: "ada@example.invalid"})
	if err := runApply(v, steps, nil, nil); err != nil {
		t.Fatalf("page 2's run = %v\n%s", err, strings.Join(s.events, "\n"))
	}
	var order []string
	for _, e := range s.events {
		switch {
		case strings.HasPrefix(e, "status git identity ◐"):
			order = append(order, "git")
		case strings.HasPrefix(e, "status GitHub ◐"):
			order = append(order, "GitHub")
		case strings.HasPrefix(e, "ask "+titleAccess+" @2/4"):
			order = append(order, "sub-page")
		case strings.HasPrefix(e, "resume "):
			order = append(order, "resume")
		}
	}
	if want := []string{"git", "GitHub", "sub-page", "resume"}; !slices.Equal(order, want) {
		t.Errorf("page 2 ran %q, want %q:\n%s", order, want, strings.Join(s.events, "\n"))
	}
	if !slices.Contains(s.events, "status git identity ✓ Ada <ada@example.invalid> · git's global config") {
		t.Errorf("the identity's fields were not written:\n%s", strings.Join(s.events, "\n"))
	}
	if !slices.ContainsFunc(s.events, func(e string) bool {
		return strings.HasPrefix(e, "status GitHub ") && strings.HasSuffix(e, "sandbox Git: later")
	}) {
		t.Errorf("Esc on the sub-page did not leave sandbox Git for later:\n%s", strings.Join(s.events, "\n"))
	}
}

// Every hand-off on the card goes through the session: it lends the terminal under a frame that
// names the program by its base name and the page that returns when it exits.
func TestEveryHandOffIsFramedAndYieldsTheCard(t *testing.T) {
	isolatedInitHost(t)
	t.Setenv("STUB_DIAGNOSE", signedOut)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv(ghTokenEnvVar, "")
	t.Setenv("SSH_AUTH_SOCK", "")
	captureUI(t)
	bin := t.TempDir()
	for name, body := range map[string]string{"brew": "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	host := sbx.Host{OS: "darwin", Arch: "arm64"}
	rep := newInitReport(initOptions{}, host, nil, sbx.Plan{})

	v1, s1 := onCard(1)
	sbxSteps := applySbx(initDecisions{Install: installSkip, Login: true, Baseline: sbx.BaselineBalanced}, applyCtx{host: host, rep: rep, view: v1})
	if err := runApply(v1, sbxSteps, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := runPackage(sbx.Packaged{Argv: []string{"sudo", "apt-get", "install", "./sbx.deb"}}, t.TempDir(), "abc", applyCtx{rep: rep, view: v1}); err != nil {
		t.Fatal(err)
	}
	v2, s2 := onCard(2)
	s2.script = []choiceui.Outcome{choiceui.Back}
	gitSteps := applyGit(initDecisions{Gh: true}, applyCtx{host: host, rep: rep, view: v2, frame: subFrame{at: initStepAt(2)}}, gitAnswer{})
	_ = runApply(v2, gitSteps, nil, nil)

	hands := append(s1.with("hand "), s2.with("hand ")...)
	for _, want := range []string{
		"hand sbx login ── Docker not signed in · sbx's screen · page 1/4 returns when it exits",
		"hand sbx policy init balanced ── sbx's screen · page 1/4 returns when it exits",
		"hand sudo apt-get install ./sbx.deb ── apt-get's screen · page 1/4 returns when it exits",
		"hand brew install gh ── brew's screen · page 2/4 returns when it exits",
	} {
		if !slices.Contains(hands, want) {
			t.Errorf("no hand-off %q among:\n%s", want, strings.Join(hands, "\n"))
		}
	}
	for _, h := range hands {
		if strings.Contains(h, "/") && strings.Index(h, "/") < strings.Index(h, " ── ") && !strings.Contains(h, "./sbx.deb") {
			t.Errorf("a frame names its program by a path: %q", h)
		}
	}
}

// RUN IN PLACE: after a stop the running step lends the terminal to no program and opens no
// sub-page: Hand returns the stop, which ends the row stopped, and Sub answers Cancelled.
func TestAfterAStopNoProgramTakesTheTerminalAndNoSubPageOpens(t *testing.T) {
	v, s := onCard(2)
	step := applyStep{name: actGitHub, label: rowGh, doing: "gh auth status", run: func() error {
		s.cancel()
		if out, err := v.Sub(&choiceui.Form{Title: titleAccess, Step: initStepAt(2)}); out != choiceui.Cancelled || err != nil {
			t.Errorf("Sub after a stop = %v, %v; want Cancelled", out, err)
		}
		return v.Hand(command("gh", "auth", "login"), []string{"gh's screen"}, nil)
	}}
	err := runApply(v, []applyStep{step}, nil, nil)
	if !interrupted(err) || !slices.Contains(s.events, "status GitHub ! stopped · Ctrl+C ended it") {
		t.Errorf("a hand-off after a stop = %v, want the row stopped:\n%s", err, strings.Join(s.events, "\n"))
	}
	if reached := append(s.with("hand "), s.with("ask ")...); len(reached) > 0 {
		t.Errorf("a stopped run still reached the card: %q", reached)
	}
}

// RUN IN PLACE: Ctrl+C while a step runs lets that step finish, then stops the run: the rows after
// it read not run, and init ends as stopped there, by SIGINT.
func TestCtrlCDuringALiveStepStopsAfterIt(t *testing.T) {
	v, s := onCard(1)
	var ran []string
	step := func(row string, stop bool) applyStep {
		return applyStep{name: row, label: row, doing: "doing", run: func() error {
			ran = append(ran, row)
			if stop {
				s.cancel()
			}
			return nil
		}, outcome: func() (choiceui.Mark, string) { return choiceui.MarkDone, "done" }}
	}
	err := runApply(v, []applyStep{step(rowInstall, true), step(rowPath, false), step(rowLogin, false)}, nil, nil)
	if !slices.Equal(ran, []string{rowInstall}) {
		t.Errorf("ran %q after the stop, want only the row it came on", ran)
	}
	var failed stepErr
	if !errors.As(err, &failed) || failed.label != rowInstall || !interrupted(err) || !slices.Equal(failed.rest, []string{rowPath, rowLogin}) {
		t.Fatalf("err = %#v, want a stop at sbx install with the rest unreached", err)
	}
	for _, want := range []string{"status sbx install ✓ done", "status PATH – not run", "status Docker login – not run"} {
		if !slices.Contains(s.events, want) {
			t.Errorf("no %q among:\n%s", want, strings.Join(s.events, "\n"))
		}
	}
	v2, s2 := onCard(1)
	cut := applyStep{name: "download", label: rowInstall, run: func() error { s2.cancel(); return context.Canceled }}
	if err := runApply(v2, []applyStep{cut, step(rowPath, false)}, nil, nil); !interrupted(err) || !slices.Contains(s2.events, "status sbx install ! stopped · Ctrl+C ended it") {
		t.Errorf("a step Ctrl+C cut short = %v, want a stop:\n%s", err, strings.Join(s2.events, "\n"))
	}
	out := captureUI(t)
	rep := testReport(t, initOptions{})
	if code, ok := exitCode(rep.close(&initRecord{}, err)); !ok || code != 130 {
		t.Errorf("a stopped run exits %d, want 130", code)
	}
	if got := out.String(); !strings.Contains(got, "warn: init — stopped at sbx install\n    Ctrl+C ended it") {
		t.Errorf("the ending does not say where Ctrl+C stopped init:\n%s", got)
	}
}

// ONE SESSION PER WALK: while the card is up nothing reaches the terminal; ui's hold stays empty over
// a full walk, and the closed checklist prints once the card closes.
func TestNothingReachesTheTerminalWhileTheCardIsUp(t *testing.T) {
	out := captureUI(t)
	s := newFakeSession(choiceui.Accepted, choiceui.Accepted, choiceui.Skipped, choiceui.Accepted)
	s.edit = func(f *choiceui.Form, _ int) {
		for i := range f.Rows {
			switch f.Rows[i].Label {
			case rowName:
				f.Rows[i].Value = "Ada"
			case rowEmail:
				f.Rows[i].Value = "ada@example.invalid"
			}
		}
	}
	w := fakeWalk(t, s, initOptions{skipGh: true, skipPrompt: true})
	openCard = func() (cardSession, error) {
		s.release = ui.Hold()
		return s, nil
	}
	t.Setenv("STUB_DIAGNOSE", signedOut)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	for _, v := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(v, "")
	}
	if err := w.onCard(); err != nil {
		t.Fatalf("the walk = %v\n%s\n%s", err, out, strings.Join(s.events, "\n"))
	}
	if s.release == nil || s.closed != 1 {
		t.Fatalf("the walk opened no held card, or closed it %d times", s.closed)
	}
	if len(s.held) != 0 {
		t.Errorf("the walk wrote to the terminal while the card was up:\n%s", s.held)
	}
	if !slices.Contains(s.with("hand "), "hand sbx login ── Docker not signed in · sbx's screen · page 1/4 returns when it exits") {
		t.Errorf("the walk never reached its hand-off:\n%s", strings.Join(s.events, "\n"))
	}
	got := out.String()
	for _, re := range []string{`(?m)^── proveo init ── darwin/arm64 · sbx 0\.50\.0 ──+$`, `(?m)^warn: Docker login — not signed in$`,
		`(?m)^ok: git identity — Ada <ada@example\.invalid> · git's global config$`, `(?m)^ok: operator — Executor \(default\)$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("the closed checklist lacks %s:\n%s", re, got)
		}
	}
}
