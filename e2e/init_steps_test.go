//go:build e2e

// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml, _spec/cmd/proveo/init-credential-wireframe.puml
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// initStubSbx answers what `proveo init` asks of sbx, logs every call, and keeps a secret listing.
const initStubSbx = `#!/bin/sh
printf '%s\n' "$*" >> "$STUB_SBX_LOG"
case "$1 $2" in
  "version --json") printf '%s\n' '{"client":{"version":"0.50.0"},"server":{"state":"running"}}' ;;
  "version "*|"version") printf '%s\n' 'sbx version: v0.50.0 deadbeef' ;;
  "diagnose --json")
    if [ -n "$STUB_SBX_DIAGNOSE" ]; then printf '%s\n' "$STUB_SBX_DIAGNOSE"; else
    printf '%s\n' '{"checks":[{"name":"virtualisation","status":"pass","message":"supported","detail":"stub"},{"name":"Binary version","status":"warn","message":"update available: v9.9.9","detail":"https://example.invalid/sbx/v9.9.9","hint":"Download the latest release from the link above"},{"name":"SSH client config","status":"fail","message":"an unmanaged sandbox Host stanza can shadow the managed configuration","detail":"/home/op/.ssh/config:5: Host","hint":"Remove or rename the reported Host *.sbx stanza, then run sbx setup ssh"}]}'; fi ;;
  "secret ls") cat "$STUB_SBX_SECRETS" 2>/dev/null ;;
  "settings get")
    case "$3" in
      ssh.agentForwardingEnabled) echo true ;;
      ssh.agentSocketPath) echo /tmp/proveo-test-agent.sock ;;
    esac ;;
  "secret set")
    case "$*" in *--oauth*) printf '(global) service %s (oauth configured)\n' "$3" >> "$STUB_SBX_SECRETS" ;; esac ;;
  "secret rm")
    printf 'Remove %s from every scope? [y/N] ' "$3"
    read -r answer
    [ "$answer" = y ] || { echo kept; exit 1; }
    grep -v " service $3 " "$STUB_SBX_SECRETS" > "$STUB_SBX_SECRETS.tmp"; mv "$STUB_SBX_SECRETS.tmp" "$STUB_SBX_SECRETS"
    echo removed ;;
  "setup ")
    if [ -n "$STUB_SBX_SETUP_READS" ]; then
      printf 'setup asks: '
      IFS= read -r line
      printf 'setup read: %s\n' "$line" >> "$STUB_SBX_LOG"
    fi ;;
  "policy check") printf '%s\n' '{"allowed":false}' ;;
  "run --name") printf '(global) service anthropic (oauth configured)\n' >> "$STUB_SBX_SECRETS" ;;
esac
exit 0
`

type initRun struct {
	sess                *tmux.Session
	home, tmp, log, sec string
	pastChecks          bool // the checks page was continued past
}

// startInit runs the real `proveo init` on a tmux TTY against initStubSbx, in a scrubbed environment.
func startInit(t *testing.T, width int, args ...string) *initRun {
	return startInitTools(t, width, nil, args...)
}

func startInitTools(t *testing.T, width int, bins map[string]string, args ...string) *initRun {
	t.Helper()
	return startInitWith(t, width, bins, nil, args...)
}

// initWrapper runs init, then prints its status and keeps the pane open for the capture.
const initWrapper = `"$0" init --skip-prompt "$@"; echo "INIT_EXIT=$?"; exec sleep 600`

// installerWrapper runs init as install.sh does, `proveo init || init_incomplete`, in an inner
// shell; the outer shell traps INT and prints the inner shell's status.
const installerWrapper = `trap : INT; sh -c '"$0" init --skip-prompt "$@" || echo INIT_INCOMPLETE' "$0" "$@"; echo "INIT_EXIT=$?"; exec sleep 600`

// startInitWith starts init with extra tools on PATH and extra environment for the stub sbx.
func startInitWith(t *testing.T, width int, bins map[string]string, env []string, args ...string) *initRun {
	t.Helper()
	return startInitWrapped(t, width, bins, env, initWrapper, args...)
}

// startInitWrapped starts init under wrapper, an `sh -c` script whose $0 is proveo and whose
// arguments are init's flags.
func startInitWrapped(t *testing.T, width int, bins map[string]string, env []string, wrapper string, args ...string) *initRun {
	t.Helper()
	if !tmux.Available() {
		t.Skip("tmux unavailable")
	}
	host := sbx.DetectHost()
	if _, err := sbx.PlanFor(host, "/tmp/unused", "/tmp/unused"); err != nil {
		t.Skipf("no Docker Sandboxes build for %s/%s: %v", host.OS, host.Arch, err)
	}
	proveoBin := buildProveo(t)
	bin := t.TempDir()
	for name, body := range bins {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "sbx"), []byte(initStubSbx), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &initRun{home: t.TempDir(), tmp: t.TempDir()}
	state := t.TempDir()
	r.log, r.sec = filepath.Join(state, "sbx.log"), filepath.Join(state, "secrets")
	if err := os.WriteFile(r.sec, []byte("(global) service openai (oauth configured)\n(global) service opencode (stored)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.sess = tmux.New(fmt.Sprintf("proveo-init-%s-%d", strings.ToLower(regexp.MustCompile(`\W`).ReplaceAllString(t.Name(), "")), os.Getpid()), nil)
	t.Cleanup(r.sess.Kill)
	argv := append([]string{"env", "-i",
		"HOME=" + r.home, "TMPDIR=" + r.tmp, "TERM=xterm-256color",
		"PATH=" + bin + ":/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"STUB_SBX_LOG=" + r.log, "STUB_SBX_SECRETS=" + r.sec}, env...)
	argv = append(append(argv,
		"sh", "-c", wrapper, proveoBin), args...)
	if err := r.sess.Start(width, 50, argv...); err != nil {
		t.Fatalf("start tmux: %v", err)
	}
	return r
}

func (r *initRun) wait(t *testing.T, want string) string {
	t.Helper()
	screen, err := r.sess.WaitFor(want, 30*time.Second)
	if err != nil {
		t.Fatalf("never saw %q: %v\n%s", want, err, screen)
	}
	return screen
}

// waitAny polls the screen until it holds one of wants.
func (r *initRun) waitAny(t *testing.T, wants ...string) string {
	t.Helper()
	screen := ""
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		screen, _ = r.sess.Capture()
		for _, want := range wants {
			if strings.Contains(screen, want) {
				return screen
			}
		}
	}
	t.Fatalf("never saw any of %q:\n%s", wants, screen)
	return screen
}

// waitMatch polls the screen until every pattern matches.
func (r *initRun) waitMatch(t *testing.T, patterns ...string) string {
	t.Helper()
	screen := ""
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		screen, _ = r.sess.Capture()
		matched := true
		for _, p := range patterns {
			matched = matched && regexp.MustCompile(p).MatchString(screen)
		}
		if matched {
			return screen
		}
	}
	t.Fatalf("the screen never matched %q:\n%s", patterns, screen)
	return screen
}

func (r *initRun) keys(t *testing.T, keys ...string) {
	t.Helper()
	if err := r.sess.SendKeys(keys...); err != nil {
		t.Fatal(err)
	}
}

// init's numbered pages, and the keys and hint that move between them.
const (
	initPages    = 4 // the checks, then setup, credentials and operator
	initBackKey  = "Escape"
	initBackHint = "esc back"
)

// initSkipKeys focus the Skip button under a page's rows and press it.
var initSkipKeys = []string{"End", "Down", "Enter"}

// initPageLabel is form n's page count, on its head's chip or its key line: 1 setup, 2 credentials, 3 operator.
func initPageLabel(n int) string { return fmt.Sprintf("page %d/%d", n+1, initPages) }

// checks waits for the checks page, which opens every prompted init, and returns its screen; its
// page count is the head's chip, or the key line's when the chip does not fit.
func (r *initRun) checks(t *testing.T) string {
	t.Helper()
	return r.waitAny(t, fmt.Sprintf("checks · 1/%d", initPages), fmt.Sprintf("page 1/%d", initPages))
}

// keyLine is the screen's key line: the first line that names enter between the key line's separators.
func keyLine(screen string) string {
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, "enter ") && strings.Contains(l, " · ") {
			return l
		}
	}
	return ""
}

// page waits for form n and returns its screen; the first call continues past the checks page.
func (r *initRun) page(t *testing.T, n int) string {
	t.Helper()
	if !r.pastChecks {
		r.checks(t)
		r.keys(t, "Enter")
		r.pastChecks = true
	}
	return r.wait(t, initPageLabel(n))
}

// back steps to the previous page.
func (r *initRun) back(t *testing.T) {
	t.Helper()
	r.keys(t, initBackKey)
}

// skip leaves the credentials page without provisioning.
func (r *initRun) skip(t *testing.T) {
	t.Helper()
	r.keys(t, initSkipKeys...)
}

// finish ends init from the operator page.
func (r *initRun) finish(t *testing.T) {
	t.Helper()
	r.keys(t, initSkipKeys...)
}

func (r *initRun) calls(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(r.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func count(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if l == want {
			n++
		}
	}
	return n
}

// waitExit waits for init to return and answers everything it printed.
func (r *initRun) waitExit(t *testing.T) string {
	t.Helper()
	r.wait(t, "INIT_EXIT=")
	all, err := r.sess.CaptureAll()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all, "INIT_EXIT=0") {
		t.Errorf("init failed:\n%s", all)
	}
	return all
}

// Three numbered screens; a step back keeps typed keys and never re-runs what setup already ran.
func TestInitStepsBackWithoutRerunningSetup(t *testing.T) {
	r := startInit(t, 140, "--skip-gh", "--skip-git")
	screen := r.page(t, 1)
	if !regexp.MustCompile(`sign in\s+\(•\) now`).MatchString(screen) {
		t.Fatalf("setup does not open with sign in on now:\n%s", screen)
	}
	if !strings.Contains(screen, initBackHint) {
		t.Errorf("setup does not offer the step back to the checks page:\n%s", screen)
	}
	r.keys(t, "Enter")
	screen = r.page(t, 2)
	for _, want := range []string{initBackHint, "enter accept", "pgup/pgdn scroll", "a sign-in per subscription"} {
		if !strings.Contains(screen, want) {
			t.Errorf("credentials screen lacks %q:\n%s", want, screen)
		}
	}
	if !regexp.MustCompile(`› opencode\s+✓`).MatchString(screen) {
		t.Errorf("the cursor does not open on the first key field:\n%s", screen)
	}

	r.keys(t, "s", "k", "-", "t", "y", "p", "e", "d")
	r.back(t)
	screen = r.page(t, 1)
	for _, re := range []string{`sign in\s+\( \) now\s+\(•\) later`, `sbx setup\s+\( \) now\s+\(•\) later`, `\(•\) keep what`} {
		if !regexp.MustCompile(re).MatchString(screen) {
			t.Errorf("the revisit does not open on what is left (%s):\n%s", re, screen)
		}
	}
	r.keys(t, "Enter")
	screen = r.page(t, 2)
	if !regexp.MustCompile(`› opencode\s+✓ \[•{8}\s`).MatchString(screen) {
		t.Errorf("the typed key did not survive the step back:\n%s", screen)
	}
	calls := r.calls(t)
	if count(calls, "login") != 1 || count(calls, "setup") != 1 {
		t.Errorf("sbx login ran %d times and sbx setup %d; each must run once:\n%s",
			count(calls, "login"), count(calls, "setup"), strings.Join(calls, "\n"))
	}

	r.keys(t, "C-u", "Enter")
	r.page(t, 3)
	r.back(t)
	r.page(t, 2)
	r.keys(t, "Enter")
	r.page(t, 3)
	if err := r.sess.SendText("Ada"); err != nil {
		t.Fatal(err)
	}
	r.keys(t, "Enter")
	r.waitExit(t)
	b, err := os.ReadFile(filepath.Join(r.home, ".proveo", "operator.yml"))
	if err != nil || !strings.Contains(string(b), "Ada") {
		t.Errorf("the operator screen did not save its answer: %v %q", err, b)
	}
	for _, c := range r.calls(t) {
		if strings.HasPrefix(c, "secret set") {
			t.Errorf("a cleared key was still provisioned: %q", c)
		}
	}
}

// A sign-in row runs sbx's own flow after enter, and sign out leaves the confirmation to sbx.
func TestInitSignInRowsRunSbxsOwnFlows(t *testing.T) {
	r := startInit(t, 140, "--skip-login", "--skip-setup", "--skip-gh", "--skip-git")
	r.page(t, 1)
	r.keys(t, "Enter")
	r.page(t, 2)
	r.keys(t, "Up", "Up", "Right", "Right", "Up", "Right")
	r.waitMatch(t, `claudecode\s+\( \) not signe…\s+\(•\) sign in`, `codex\s+\( \) signed in\s+\( \) sign in a…\s+\(•\) sign out`)
	r.keys(t, "Enter")
	r.wait(t, "[y/N]")
	r.keys(t, "y", "Enter")
	r.page(t, 3)

	calls := r.calls(t)
	run, rm, out := -1, -1, -1
	for i, c := range calls {
		switch {
		case strings.HasPrefix(c, "run --name "+sbx.SignInSandbox+" claude ") && strings.HasSuffix(c, " -- auth login"):
			run = i
		case c == "rm --force "+sbx.SignInSandbox:
			rm = i
		case c == "secret rm openai":
			out = i
		case strings.HasPrefix(c, "secret rm") || strings.HasPrefix(c, "secret set"):
			t.Errorf("unexpected credential write %q", c)
		}
	}
	if run < 0 || rm < run || out < 0 {
		t.Errorf("want sign-in run, its cleanup, and `secret rm openai` without -f; sbx saw:\n%s", strings.Join(calls, "\n"))
	}
	sec, _ := os.ReadFile(r.sec)
	if strings.Contains(string(sec), "service openai") || !strings.Contains(string(sec), "service anthropic (oauth configured)") {
		t.Errorf("the store after sign-in/sign-out:\n%s", sec)
	}
	if left, _ := filepath.Glob(filepath.Join(r.tmp, sbx.SignInSandbox+"-*")); len(left) > 0 {
		t.Errorf("the sign-in workspace was left behind: %v", left)
	}
	r.back(t)
	r.page(t, 2)
	r.waitMatch(t, `claudecode\s+\(•\) signed in`, `codex\s+\(•\) not signe`)
	r.skip(t)
	r.page(t, 3)
	r.finish(t)
	r.waitExit(t)
}

// --yes takes every default: no numbered screen and no credentials form.
func TestInitYesNeverOpensTheCredentialsForm(t *testing.T) {
	r := startInit(t, 140, "--yes", "--skip-login", "--skip-setup", "--skip-gh", "--skip-git")
	all := r.waitExit(t)
	if strings.Contains(all, "page ") {
		t.Errorf("--yes numbered a screen:\n%s", all)
	}
	if results := all[strings.LastIndex(all, " results "):]; !regexp.MustCompile(`(?m)^\s+credentials$`).MatchString(results) {
		t.Errorf("--yes did not leave the credentials for later:\n%s", all)
	}
	calls := r.calls(t)
	if count(calls, "login") != 0 {
		t.Errorf("--skip-login still signed in: %v", calls)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "secret set") || strings.HasPrefix(c, "secret rm") || strings.HasPrefix(c, "run ") {
			t.Errorf("--yes changed a credential: %q", c)
		}
	}
}

// On a narrow terminal the key line drops the scroll keys first; the way out stays.
func TestInitNarrowFooterKeepsTheWayOut(t *testing.T) {
	r := startInit(t, 64, "--skip-login", "--skip-setup", "--skip-gh", "--skip-git")
	r.page(t, 1)
	r.keys(t, "Enter")
	footer := keyLine(r.page(t, 2))
	for _, want := range []string{initBackHint, "enter accept"} {
		if !strings.Contains(footer, want) {
			t.Errorf("64 columns lost %q: %q", want, footer)
		}
	}
	if strings.Contains(footer, "pgup/pgdn") {
		t.Errorf("64 columns kept the scroll keys over the way out: %q", footer)
	}
	r.skip(t)
	r.page(t, 3)
	r.finish(t)
	r.waitExit(t)
}

// Ctrl+C on a page asks before it leaves; Skip is a button the cursor reaches, not a key.
func TestInitCtrlCAsksBeforeItQuitsAndSkipIsAButton(t *testing.T) {
	r := startInit(t, 140, "--skip-gh", "--skip-git")
	r.page(t, 1)
	r.keys(t, "Enter")
	screen := r.page(t, 2)
	for _, want := range []string{"[ Skip ]", "ctrl+c quit", initBackHint, "enter accept"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the credentials page lacks %q:\n%s", want, screen)
		}
	}
	r.keys(t, "C-c")
	screen = r.wait(t, "leave the wizard?")
	if strings.Contains(screen, "INIT_EXIT=") {
		t.Fatalf("one Ctrl+C ended init:\n%s", screen)
	}
	r.keys(t, "Enter")
	r.page(t, 2)
	r.keys(t, "End", "Down")
	r.wait(t, "enter skip")
	r.keys(t, "Enter")
	r.page(t, 3)
	r.keys(t, "C-c")
	r.wait(t, "leave the wizard?")
	r.keys(t, "C-c")
	screen = r.wait(t, "INIT_EXIT=")
	if !strings.Contains(screen, "quit at page 4/4") || !strings.Contains(screen, "INIT_EXIT=1") {
		t.Errorf("a confirmed quit must say where init stopped and exit 1:\n%s", screen)
	}
}

// The setup page names what Enter runs; the apply then starts each program under its own line, with no place in it.
func TestInitSaysWhatEnterRunsThenShowsEachStep(t *testing.T) {
	r := startInit(t, 140, "--skip-gh", "--skip-git")
	screen := r.page(t, 1)
	for _, want := range []string{"on enter", "sign in to Docker (sbx login)", "run sbx's first-run wizard (sbx setup)",
		"verify sbx (sbx diagnose)"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the setup page does not name %q among what Enter runs:\n%s", want, screen)
		}
	}
	if !regexp.MustCompile(`enter accept · \d+ steps`).MatchString(screen) {
		t.Errorf("the count of steps must follow \"enter accept\":\n%s", screen)
	}
	r.keys(t, "Enter")
	r.page(t, 2)
	r.skip(t)
	r.page(t, 3)
	r.finish(t)
	all := r.waitExit(t)
	for _, want := range []string{"sbx login — signing in to Docker", "sbx setup — running the first-run wizard"} {
		if !regexp.MustCompile(`(?m)^. ` + regexp.QuoteMeta(want) + `$`).MatchString(all) {
			t.Errorf("the apply never announced %q:\n%s", want, all)
		}
	}
	if regexp.MustCompile(`step \d+/\d+`).MatchString(all) {
		t.Errorf("a line that stays on screen carries its place; only live frames keep it:\n%s", all)
	}
	if strings.Contains(all, "· verify sbx (sbx diagnose)") {
		t.Errorf("a step that kept the terminal left its live line behind:\n%s", all)
	}
	if calls := r.calls(t); count(calls, "login") != 1 || count(calls, "setup") != 1 {
		t.Errorf("each listed action must run once; sbx saw:\n%s", strings.Join(calls, "\n"))
	}
}

// What the operator types while sbx's own wizard runs reaches the wizard, and nothing of it reaches proveo.
func TestInitHandsTheTerminalWhollyToSbxSetup(t *testing.T) {
	r := startInitWith(t, 140, nil, []string{"STUB_SBX_SETUP_READS=1"}, "--skip-gh", "--skip-git")
	r.page(t, 1)
	r.keys(t, "Enter")
	r.wait(t, "setup asks:")
	r.keys(t, "typed-for-setup", "Enter")
	screen, err := r.sess.WaitFor(initPageLabel(2), 15*time.Second)
	if calls := r.calls(t); err != nil || !slices.Contains(calls, "setup read: typed-for-setup") {
		t.Fatalf("sbx setup did not get the line typed for it (%v); sbx saw:\n%s\nscreen:\n%s", err, strings.Join(calls, "\n"), screen)
	}
	r.skip(t)
	r.page(t, 3)
	r.finish(t)
	r.waitExit(t)
}

// init opens on every check before it asks anything, and closes on what waits,
// the checks sbx failed, and its verdict on the host.
func TestInitOpensOnTheChecksAndClosesOnTheResult(t *testing.T) {
	r := startInit(t, 140, "--skip-gh", "--skip-git")
	screen := r.checks(t)
	for _, want := range []string{"init — preparing the ", "to install sbx", "sbx diagnose", "enter continue", "esc cancel", "ctrl+c quit",
		"Binary version — update available: v9.9.9", "https://example.invalid/sbx/v9.9.9", "Download the latest release from the link above",
		"! SSH client config — an unmanaged sandbox Host stanza", "a run does not need this check; it affects IDE attach"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the checks page lacks %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "enter accept") || strings.Contains(screen, initBackHint) {
		t.Errorf("the checks page is the first page and no form: it must not say \"enter accept\" or offer a step back:\n%s", screen)
	}
	r.page(t, 1)
	r.back(t)
	r.checks(t)
	r.keys(t, "Enter")
	r.page(t, 1)
	r.keys(t, "Enter")
	r.page(t, 2)
	r.skip(t)
	r.page(t, 3)
	r.finish(t)
	all := r.waitExit(t)
	results := all[strings.LastIndex(all, " results "):]
	for _, re := range []string{`(?m)^. later: GitHub$`, `(?m)^\s+git identity$`, `(?m)^\s+credentials$`,
		`(?m)^. SSH client config — an unmanaged sandbox Host stanza`, `(?m)^\s+a run does not need this check; it affects IDE attach`} {
		if !regexp.MustCompile(re).MatchString(results) {
			t.Errorf("the result lacks %s:\n%s", re, results)
		}
	}
	if regexp.MustCompile(`(?m)^\s+(sign in|sbx setup)$`).MatchString(results) {
		t.Errorf("an action that ran is listed as left for later:\n%s", results)
	}
	if last := lastProveoLine(all); last != "✓ host — ready to run" {
		t.Errorf("init must end on its verdict, not %q:\n%s", last, all)
	}
	for _, gone := range []string{"done:", "next:", "not ready to run", "proveo init`"} {
		if strings.Contains(all, gone) {
			t.Errorf("the stream still says %q:\n%s", gone, all)
		}
	}
	if n := strings.Count(all, " results "); n != 1 {
		t.Errorf("results drawn %d times, want once:\n%s", n, all)
	}
	if calls := r.calls(t); count(calls, "login") != 1 || count(calls, "setup") != 1 {
		t.Errorf("stepping back to the checks page re-ran setup; sbx saw:\n%s", strings.Join(calls, "\n"))
	}
}

// lastProveoLine is the last line init printed before the harness's INIT_EXIT.
func lastProveoLine(all string) string {
	before, _, _ := strings.Cut(all, "INIT_EXIT=")
	lines := strings.Split(strings.TrimRight(before, "\n "), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// A host whose every check passes closes on the verdict alone, each subject's heading drawn once.
func TestInitOnAReadyHostClosesOnReady(t *testing.T) {
	r := startInitWith(t, 140, nil, []string{`STUB_SBX_DIAGNOSE={"checks":[{"name":"virtualisation","status":"pass","message":"supported"}]}`},
		"--skip-gh", "--skip-git")
	r.page(t, 1)
	r.keys(t, "Enter")
	r.page(t, 2)
	r.skip(t)
	r.page(t, 3)
	r.finish(t)
	all := r.waitExit(t)
	if last := lastProveoLine(all); last != "✓ host — ready to run" {
		t.Errorf("a ready host must end on its verdict, not %q:\n%s", last, all)
	}
	for _, h := range []string{" sbx ", " credentials ", " results "} {
		if n := len(regexp.MustCompile(`(?m)^\s*─+`+regexp.QuoteMeta(h)+`─+$`).FindAllString(all, -1)); n != 1 {
			t.Errorf("heading%q drawn %d times, want once:\n%s", h, n, all)
		}
	}
	for _, gone := range []string{" run ", " execution ", " starting ", " secrets "} {
		if regexp.MustCompile(`(?m)^\s*─+` + regexp.QuoteMeta(gone) + `─+$`).MatchString(all) {
			t.Errorf("init drew the%qheading:\n%s", gone, all)
		}
	}
}

// A failing check a run needs closes on ONE × host line, names what never ran, and exits 1.
func TestInitARunBlockerClosesOnTheHostVerdict(t *testing.T) {
	r := startInitWith(t, 140, nil, []string{`STUB_SBX_DIAGNOSE={"checks":[{"name":"Daemon","status":"fail","message":"not running","detail":"no socket","hint":"Start Docker Sandboxes, then run sbx diagnose again"}]}`},
		"--yes", "--skip-login", "--skip-setup", "--skip-gh", "--skip-git")
	r.wait(t, "INIT_EXIT=")
	all, err := r.sess.CaptureAll()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all, "INIT_EXIT=1") {
		t.Errorf("a run blocker must exit 1:\n%s", all)
	}
	if last := lastProveoLine(all); last != "× host — not ready to run" {
		t.Errorf("a run blocker must end on the host's verdict, not %q:\n%s", last, all)
	}
	if n := strings.Count(all, "× host"); n != 1 {
		t.Errorf("%d × host lines, want one:\n%s", n, all)
	}
	for _, re := range []string{`(?m)^× Daemon — not running$`, `(?m)^\s+Start Docker Sandboxes, then run sbx diagnose again$`,
		`(?m)^. not run: credentials$`, `(?m)^\s+operator name$`} {
		if !regexp.MustCompile(re).MatchString(all) {
			t.Errorf("the result lacks %s:\n%s", re, all)
		}
	}
}

// An sbx kept outside the prefix earns no PATH step, so nothing claims a PATH change and no rc
// file appears.
func TestInitAddsNoPathStepForAnSbxOutsideThePrefix(t *testing.T) {
	r := startInit(t, 140, "--skip-gh", "--skip-git")
	screen := r.page(t, 1)
	if strings.Contains(screen, "to PATH") {
		t.Errorf("the setup page names a PATH step for an sbx the prefix does not hold:\n%s", screen)
	}
	r.keys(t, "Enter")
	r.page(t, 2)
	r.skip(t)
	r.page(t, 3)
	r.finish(t)
	all := r.waitExit(t)
	if strings.Contains(all, "to PATH") || strings.Contains(all, "not on PATH") {
		t.Errorf("init claims a PATH change it never made:\n%s", all)
	}
	for _, rc := range []string{".zshrc", ".bashrc", ".bash_profile", ".profile", filepath.Join(".config", "fish", "config.fish")} {
		if _, err := os.Stat(filepath.Join(r.home, rc)); err == nil {
			t.Errorf("init wrote %s under the temp HOME", rc)
		}
	}
}

// --yes asks nothing, git identity included; with no identity it says so.
func TestInitYesNeverAsksForAGitIdentity(t *testing.T) {
	r := startInit(t, 140, "--yes", "--skip-login", "--skip-setup", "--skip-gh")
	all := r.waitExit(t)
	if strings.Contains(all, "› git user.name") || strings.Contains(all, "git user.email") {
		t.Errorf("--yes asked for a git identity:\n%s", all)
	}
	if !regexp.MustCompile(`(?m)^⚠ git identity — not set$`).MatchString(all) {
		t.Errorf("--yes with no identity must say it is not set:\n%s", all)
	}
	if strings.Contains(all, "`git config --global") {
		t.Errorf("--yes is the operator's act, so the how-to belongs to flag help:\n%s", all)
	}
	if _, err := os.Stat(filepath.Join(r.home, ".gitconfig")); err == nil {
		t.Error("--yes wrote a git identity")
	}
}

// The CDN installer's CI path is `init --yes` with stderr a pipe: plain lines carry no step place,
// each subject's heading draws once, in order, above lines of its own, and verify leaves no line
// under git, the heading before it.
func TestInitYesInPlainOutputCarriesNoStepPlaces(t *testing.T) {
	host := sbx.DetectHost()
	if _, err := sbx.PlanFor(host, "/tmp/unused", "/tmp/unused"); err != nil {
		t.Skipf("no Docker Sandboxes build for %s/%s: %v", host.OS, host.Arch, err)
	}
	proveoBin := buildProveo(t)
	bin, state := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"sbx":     initStubSbx,
		"gh":      "#!/bin/sh\ncase \"$1 $2\" in\n \"config get\") echo https ;;\n *) exit 1 ;;\nesac\n",
		"ssh-add": "#!/bin/sh\nexit 1\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(state, "secrets"), []byte("(global) service openai (oauth configured)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(proveoBin, "init", "--yes", "--skip-prompt")
	cmd.Stdin = strings.NewReader("")
	cmd.Env = []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(), "TERM=xterm-256color",
		"PATH=" + bin + ":/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"STUB_SBX_LOG=" + filepath.Join(state, "sbx.log"), "STUB_SBX_SECRETS=" + filepath.Join(state, "secrets")}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("proveo init --yes: %v\n%s", err, stderr.String())
	}
	got := stderr.String()
	if strings.ContainsAny(got, "\x1b\r") || regexp.MustCompile(`step \d+/\d+`).MatchString(got) {
		t.Errorf("plain output carries a step place or terminal control:\n%q", got)
	}
	for _, re := range []string{`(?m)^sbx login — signing in to Docker$`, `(?m)^sbx setup — running the first-run wizard$`,
		`(?m)^ok: host — ready to run$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("plain output lacks %s:\n%s", re, got)
		}
	}
	if strings.Contains(got, "verify sbx") {
		t.Errorf("verify left a line in plain output:\n%s", got)
	}
	var heads []string
	blocks := regexp.MustCompile(`(?m)^\s*-{6} (\S+) -{6}\n`).Split(got, -1)[1:]
	for i, m := range regexp.MustCompile(`(?m)^\s*-{6} (\S+) -{6}$`).FindAllStringSubmatch(got, -1) {
		heads = append(heads, m[1])
		if strings.TrimSpace(blocks[i]) == "" {
			t.Errorf("heading %q drawn over no line of its own:\n%s", m[1], got)
		}
	}
	if want := []string{"sbx", "git", "credentials", "operator", "results"}; !slices.Equal(heads, want) {
		t.Fatalf("headings = %q, want %q, each once:\n%s", heads, want, got)
	}
	if git := blocks[slices.Index(heads, "git")]; !regexp.MustCompile(`(?m)^warn: git identity — not set$`).MatchString(git) {
		t.Errorf("the git block lacks the identity --yes left unset:\n%s", got)
	}
}

// A Ctrl+C while sbx's wizard has the terminal reaches the wizard; init then prints what it
// gathered, ends on where it stopped, and dies of SIGINT, so a shell like install.sh's stops too.
func TestInitCtrlCInSbxsWizardEndsOnWhereItStopped(t *testing.T) {
	r := startInitWrapped(t, 140, nil, []string{"STUB_SBX_SETUP_READS=1"}, installerWrapper, "--skip-gh", "--skip-git")
	r.page(t, 1)
	r.keys(t, "Enter")
	r.wait(t, "setup asks:")
	r.keys(t, "C-c")
	r.wait(t, "INIT_EXIT=")
	all, err := r.sess.CaptureAll()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all, "INIT_EXIT=130") || strings.Contains(all, "INIT_INCOMPLETE") {
		t.Errorf("a Ctrl+C in a hand-off must end init by SIGINT, so the installer's shell stops before init_incomplete:\n%s", all)
	}
	if !regexp.MustCompile(`(?m)^⚠ init — stopped at sbx setup\n\s+Ctrl\+C ended it\nINIT_EXIT=130`).MatchString(all) {
		t.Errorf("init must end on where it stopped and why:\n%s", all)
	}
	if !regexp.MustCompile(`(?m)^. not run: sbx diagnose$`).MatchString(all) || !regexp.MustCompile(`(?m)^. host:\s+`).MatchString(all) {
		t.Errorf("init must still print the sbx block and what never ran:\n%s", all)
	}
	if n := count(r.calls(t), "diagnose --json"); n != 1 {
		t.Errorf("sbx diagnose ran %d times; once for the checks page, and no step after the Ctrl+C:\n%s", n, strings.Join(r.calls(t), "\n"))
	}
}

// HAND-OFF FAILURE: a `gh auth login` that fails keeps its own error; init prints no × for it,
// ends on where it stopped, names what never ran, and exits 1.
func TestInitAFailedGhAuthLoginEndsOnWhereItStopped(t *testing.T) {
	r := startInitTools(t, 140, map[string]string{
		"gh": "#!/bin/sh\ncase \"$1 $2\" in\n \"auth login\") echo 'gh: login aborted' >&2; exit 1 ;;\n *) exit 1 ;;\nesac\n",
	}, "--skip-login", "--skip-setup", "--skip-git")
	r.page(t, 1)
	r.keys(t, "Enter")
	r.wait(t, "INIT_EXIT=")
	all, err := r.sess.CaptureAll()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^⚠ init — stopped at GitHub\nINIT_EXIT=1`).MatchString(all) {
		t.Errorf("a failed gh auth login must end init on where it stopped, exit 1:\n%s", all)
	}
	for _, re := range []string{`(?m)^gh: login aborted$`, `(?m)^. not run: sbx diagnose$`, `(?m)^\s+credentials$`, `(?m)^\s+operator name$`} {
		if !regexp.MustCompile(re).MatchString(all) {
			t.Errorf("the screen lacks %s:\n%s", re, all)
		}
	}
	if strings.Contains(all, "× ") || strings.Contains(all, "did not complete") || strings.Contains(all, initPageLabel(2)) {
		t.Errorf("init restated gh's failure or went on to the credentials page:\n%s", all)
	}
}
