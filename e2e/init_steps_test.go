//go:build e2e

// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml, _spec/cmd/proveo/init-credential-wireframe.puml
package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
  "diagnose --json") printf '%s\n' '{"checks":[{"name":"virtualisation","status":"pass","detail":"stub"}]}' ;;
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
		"STUB_SBX_LOG=" + r.log, "STUB_SBX_SECRETS=" + r.sec,
		"sh", "-c", `"$0" init --skip-prompt "$@"; echo "INIT_EXIT=$?"; exec sleep 600`, proveoBin}, args...)
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

// initPageLabel is the key line's label for form n: 1 setup, 2 credentials, 3 operator.
func initPageLabel(n int) string { return fmt.Sprintf("page %d/%d", n+1, initPages) }

// checks waits for the checks page, which opens every prompted init, and returns its screen.
func (r *initRun) checks(t *testing.T) string {
	t.Helper()
	return r.wait(t, fmt.Sprintf("page 1/%d", initPages))
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
	if !strings.Contains(all, "--yes — nothing provisioned") {
		t.Errorf("--yes did not say it skipped the credentials form:\n%s", all)
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
	screen := r.page(t, 2)
	var footer string
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, initPageLabel(2)) {
			footer = l
		}
	}
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

// The setup page names what Enter runs; the apply then shows each as a numbered step.
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
	for _, want := range []string{"· sign in to Docker (sbx login)", "· run sbx's first-run wizard (sbx setup)"} {
		if !regexp.MustCompile(`step \d+/\d+ ` + regexp.QuoteMeta(want)).MatchString(all) {
			t.Errorf("the apply never announced %q as a numbered step:\n%s", want, all)
		}
	}
	if strings.Contains(all, "· verify sbx (sbx diagnose)") {
		t.Errorf("a step that kept the terminal left its live line behind:\n%s", all)
	}
	if calls := r.calls(t); count(calls, "login") != 1 || count(calls, "setup") != 1 {
		t.Errorf("each listed action must run once; sbx saw:\n%s", strings.Join(calls, "\n"))
	}
}

// init opens on every check before it asks anything, and closes on what it did,
// what it left with the command that does it, and the next command to type.
func TestInitOpensOnTheChecksAndClosesOnTheResult(t *testing.T) {
	r := startInit(t, 140, "--skip-gh", "--skip-git")
	screen := r.checks(t)
	for _, want := range []string{"what was checked on", "to install sbx", "sbx diagnose", "enter continue", "esc cancel", "ctrl+c quit"} {
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
	results := all[strings.LastIndex(all, "results"):]
	for _, want := range []string{
		"done: sign in to Docker (sbx login)", "done: run sbx's first-run wizard (sbx setup)", "done: verify sbx (sbx diagnose)",
		"later: set the sandbox git identity — `proveo init`", "later: connect GitHub — `proveo init`",
		"later: provision credentials — `proveo init`", "next: `proveo run`",
	} {
		if !strings.Contains(results, want) {
			t.Errorf("the result lacks %q:\n%s", want, results)
		}
	}
	if strings.Contains(results, "later: sign in to Docker") {
		t.Errorf("an action that ran is listed as left for later:\n%s", results)
	}
	if calls := r.calls(t); count(calls, "login") != 1 || count(calls, "setup") != 1 {
		t.Errorf("stepping back to the checks page re-ran setup; sbx saw:\n%s", strings.Join(calls, "\n"))
	}
}
