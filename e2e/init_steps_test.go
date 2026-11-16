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
	"github.com/proveo-ca/proveo/internal/schedule"
)

// initStubSbx answers what `proveo init` asks of sbx, logs every call, and keeps a secret listing.
// Docker reads signed in unless STUB_SBX_SIGNED_OUT is set and no login ran; STUB_SBX_ENV_ONLY is
// the env keys sbx's store lacks; STUB_SBX_SLOW_SET is the seconds a key's `secret set` takes.
const initStubSbx = `#!/bin/sh
printf '%s\n' "$*" >> "$STUB_SBX_LOG"
case "$1 $2" in
  "version --json") printf '%s\n' '{"client":{"version":"0.50.0"},"server":{"state":"running"}}' ;;
  "version "*|"version") printf '%s\n' 'sbx version: v0.50.0 deadbeef' ;;
  "diagnose --json")
    if [ -n "$STUB_SBX_DIAGNOSE" ]; then printf '%s\n' "$STUB_SBX_DIAGNOSE"; else
    auth='{"name":"Authentication","status":"pass","message":"authenticated","detail":"","hint":""}'
    if [ -n "$STUB_SBX_SIGNED_OUT" ] && [ ! -e "$STUB_SBX_LOG.signed-in" ]; then
      auth='{"name":"Authentication","status":"fail","message":"not signed in","detail":"","hint":"Run: sbx login"}'
    fi
    printf '%s\n' '{"checks":[{"name":"virtualisation","status":"pass","message":"supported","detail":"stub"},{"name":"Binary version","status":"warn","message":"update available: v9.9.9","detail":"https://example.invalid/sbx/v9.9.9","hint":"Download the latest release from the link above"},{"name":"SSH client config","status":"fail","message":"an unmanaged sandbox Host stanza can shadow the managed configuration","detail":"/home/op/.ssh/config:5: Host","hint":"Remove or rename the reported Host *.sbx stanza, then run sbx setup ssh"},'"$auth"']}'; fi ;;
  "login ") touch "$STUB_SBX_LOG.signed-in" ;;
  "secret ls")
    if [ "$3" = --json ]; then
      printf '{"secrets":[],"custom_secrets":[],"shadowed_services":[],"env_only_count":%s}\n' "${STUB_SBX_ENV_ONLY:-0}"
    else cat "$STUB_SBX_SECRETS" 2>/dev/null; fi ;;
  "settings get")
    case "$3" in
      --json) printf '{"value":%s,"source":"default"}\n' "${STUB_SBX_FORWARDING:-true}" ;;
      ssh.agentForwardingEnabled) echo true ;;
      ssh.agentSocketPath) echo /tmp/proveo-test-agent.sock ;;
    esac ;;
  "secret set")
    case "$*" in
      *--oauth*) printf '(global) service %s (oauth configured)\n' "$3" >> "$STUB_SBX_SECRETS" ;;
      *) [ -z "$STUB_SBX_SLOW_SET" ] || sleep "$STUB_SBX_SLOW_SET"
         eval "svc=\${$#}"; printf '(global) service %s (stored)\n' "$svc" >> "$STUB_SBX_SECRETS" ;;
    esac ;;
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
	sess                *schedule.TmuxSession
	home, tmp, log, sec string
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
	if !schedule.TmuxAvailable() {
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
	r.sess = schedule.NewTmux(fmt.Sprintf("proveo-init-%s-%d", strings.ToLower(regexp.MustCompile(`\W`).ReplaceAllString(t.Name(), "")), os.Getpid()), nil)
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

// init's four pages on one card, and the keys and hint that move between them.
const (
	initPages    = 4 // sbx, git and shell, credentials, operator
	initBackKey  = "Escape"
	initBackHint = "esc back"
)

// initSkipKeys focus the Skip button under a page's rows and press it.
var initSkipKeys = []string{"End", "Down", "Enter"}

// initPageLabel is page n's chip: "page n/4".
func initPageLabel(n int) string { return fmt.Sprintf("page %d/%d", n, initPages) }

// keyLine is the screen's key line: the first line that names enter between the key line's separators.
func keyLine(screen string) string {
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, "enter ") && strings.Contains(l, " · ") {
			return l
		}
	}
	return ""
}

// page waits for page n of the card and returns its screen: its chip at the end of the title row,
// never an ahead row's "page n/4 · …".
func (r *initRun) page(t *testing.T, n int) string {
	t.Helper()
	chip := regexp.MustCompile(regexp.QuoteMeta(initPageLabel(n)) + ` +│`)
	screen := ""
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if screen, _ = r.sess.Capture(); chip.MatchString(screen) {
			return screen
		}
	}
	t.Fatalf("page %d never came up:\n%s", n, screen)
	return screen
}

// next presses Enter on page n and waits for page n+1.
func (r *initRun) next(t *testing.T, n int) string {
	t.Helper()
	r.page(t, n)
	r.keys(t, "Enter")
	return r.page(t, n+1)
}

// back steps to the previous page.
func (r *initRun) back(t *testing.T) {
	t.Helper()
	r.keys(t, initBackKey)
}

// skip leaves the credentials page through its Skip button, provisioning nothing.
func (r *initRun) skip(t *testing.T) {
	t.Helper()
	r.keys(t, initSkipKeys...)
}

// finish ends init from the operator page: Enter with the name field as it stands.
func (r *initRun) finish(t *testing.T) {
	t.Helper()
	r.keys(t, "Enter")
}

// closedHead is the closed checklist's heading line.
var closedHead = regexp.MustCompile(`(?m)^── proveo init ── .+ ──+$`)

// result is the closed checklist: everything from its heading on.
func result(t *testing.T, all string) string {
	t.Helper()
	at := closedHead.FindAllStringIndex(all, -1)
	if len(at) != 1 {
		t.Fatalf("the closed checklist's heading drew %d times, want once:\n%s", len(at), all)
	}
	return all[at[0][0]:]
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

// Four pages on one card; a step back keeps typed keys, and locks the rows that ran so nothing
// runs twice.
func TestInitStepsBackWithoutRerunningSetup(t *testing.T) {
	r := startInitWith(t, 140, nil, []string{"STUB_SBX_SIGNED_OUT=1"}, "--skip-gh", "--skip-git")
	screen := r.page(t, 1)
	if !regexp.MustCompile(`Docker login\s+\(•\) now`).MatchString(screen) {
		t.Fatalf("page 1 does not open with Docker login on now:\n%s", screen)
	}
	if strings.Contains(keyLine(screen), initBackHint) || !strings.Contains(keyLine(screen), "esc quit") {
		t.Errorf("page 1 has nowhere to step back to; Esc there asks to quit:\n%s", screen)
	}
	screen = r.next(t, 1)
	if !regexp.MustCompile(`! sbx\s+kept 0\.50\.0 · Docker signed in · 2 checks need you`).MatchString(screen) {
		t.Errorf("page 2 does not fold page 1 into its done row:\n%s", screen)
	}
	screen = r.next(t, 2)
	for _, want := range []string{initBackHint, "enter next", "Store agent credentials"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the credentials page lacks %q:\n%s", want, screen)
		}
	}
	if !regexp.MustCompile(`› claudecode\s+\(•\) not signed in`).MatchString(screen) {
		t.Errorf("the cursor does not open on the top row:\n%s", screen)
	}

	r.keys(t, "Down", "Down", "Down")
	r.waitMatch(t, `› opencode\s+✓`)
	r.keys(t, "s", "k", "-", "t", "y", "p", "e", "d")
	r.back(t)
	r.page(t, 2)
	r.back(t)
	screen = r.page(t, 1)
	for _, re := range []string{`✓ Docker login\s+signed in`, `› sbx install\s+.*\(•\) keep as is`} {
		if !regexp.MustCompile(re).MatchString(screen) {
			t.Errorf("the revisit does not lock what ran and keep what is left (%s):\n%s", re, screen)
		}
	}
	if !strings.Contains(keyLine(screen), "enter next") {
		t.Errorf("a revisit with nothing left to run must say so:\n%s", screen)
	}
	r.next(t, 1)
	screen = r.next(t, 2)
	if !regexp.MustCompile(`opencode\s+✓ \[•{8}\s`).MatchString(screen) {
		t.Errorf("the typed key did not survive the step back:\n%s", screen)
	}
	calls := r.calls(t)
	if count(calls, "login") != 1 || count(calls, "setup") != 0 {
		t.Errorf("sbx login ran %d times and sbx setup %d; login once, setup only after the result:\n%s",
			count(calls, "login"), count(calls, "setup"), strings.Join(calls, "\n"))
	}

	r.keys(t, "Down", "Down", "Down")
	r.waitMatch(t, `› opencode\s+✓`)
	r.keys(t, "C-u", "Enter")
	r.page(t, 4)
	r.back(t)
	r.next(t, 3)
	if err := r.sess.SendText("Ada"); err != nil {
		t.Fatal(err)
	}
	r.keys(t, "Enter")
	r.waitExit(t)
	b, err := os.ReadFile(filepath.Join(r.home, ".proveo", "operator.yml"))
	if err != nil || !strings.Contains(string(b), "Ada") {
		t.Errorf("the operator page did not save its answer: %v %q", err, b)
	}
	for _, c := range r.calls(t) {
		if strings.HasPrefix(c, "secret set") {
			t.Errorf("a cleared key was still provisioned: %q", c)
		}
	}
	if calls := r.calls(t); count(calls, "login") != 1 || count(calls, "setup") != 0 {
		t.Errorf("the walk re-ran sbx login, or opened sbx setup with nothing to import:\n%s", strings.Join(calls, "\n"))
	}
}

// A sign-in row runs sbx's own flow after enter, and sign out leaves the confirmation to sbx.
func TestInitSignInRowsRunSbxsOwnFlows(t *testing.T) {
	r := startInit(t, 140, "--skip-login", "--skip-setup", "--skip-gh", "--skip-git")
	r.next(t, 1)
	r.next(t, 2)
	r.keys(t, "Right", "Down", "Right", "Right")
	r.waitMatch(t, `claudecode\s+\( \) not signed in\s+\(•\) sign in`, `codex\s+\( \) signed in\s+\( \) sign in again\s+\(•\) sign out`)
	if line := keyLine(r.page(t, 3)); !strings.Contains(line, "enter run 2 steps") {
		t.Errorf("the key line does not count the two rows Enter runs: %q", line)
	}
	r.keys(t, "Enter")
	r.wait(t, "[y/N]")
	r.keys(t, "y", "Enter")
	r.page(t, 4)

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
	r.page(t, 3)
	r.waitMatch(t, `claudecode\s+\(•\) signed in`, `codex\s+\(•\) not signed in`)
	r.skip(t)
	r.page(t, 4)
	r.finish(t)
	all := r.waitExit(t)
	for _, re := range []string{`(?m)^── sbx run claudecode ── claudecode's sign-in · page 3/4 returns when it exits ──+$`,
		`(?m)^── sbx secret rm openai ── sbx asks y/N · page 3/4 returns when it exits ──+$`} {
		if !regexp.MustCompile(re).MatchString(all) {
			t.Errorf("a credential hand-off was not framed (%s):\n%s", re, all)
		}
	}
}

// --yes takes every default: no numbered screen and no credentials form.
func TestInitYesNeverOpensTheCredentialsForm(t *testing.T) {
	r := startInit(t, 140, "--yes", "--skip-login", "--skip-setup", "--skip-gh", "--skip-git")
	all := r.waitExit(t)
	if strings.Contains(all, "page ") {
		t.Errorf("--yes numbered a screen:\n%s", all)
	}
	closed := result(t, all)
	for _, re := range []string{`(?m)^  ✓ codex\s+login held by sbx as openai$`, `(?m)^  ✓ opencode\s+token stored as opencode$`, `(?m)^  – not held\s+claudecode`} {
		if !regexp.MustCompile(re).MatchString(closed) {
			t.Errorf("--yes did not say what sbx holds (%s):\n%s", re, closed)
		}
	}
	if strings.Contains(closed, "later · credentials") || strings.Contains(closed, "credentials  ") {
		t.Errorf("--yes left the credentials for later instead of saying what sbx holds:\n%s", closed)
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

// On a narrow terminal the key line drops its lowest keys first; the way out stays.
func TestInitNarrowFooterKeepsTheWayOut(t *testing.T) {
	r := startInit(t, 64, "--skip-login", "--skip-setup", "--skip-gh", "--skip-git")
	r.next(t, 1)
	footer := keyLine(r.next(t, 2))
	for _, want := range []string{initBackHint, "enter next"} {
		if !strings.Contains(footer, want) {
			t.Errorf("64 columns lost %q: %q", want, footer)
		}
	}
	r.skip(t)
	r.page(t, 4)
	r.finish(t)
	r.waitExit(t)
}

// Esc on page 1 and Ctrl+C on any page ask before they quit; Skip is a button the cursor reaches,
// not a key.
func TestInitCtrlCAsksBeforeItQuitsAndSkipIsAButton(t *testing.T) {
	r := startInit(t, 140, "--skip-gh", "--skip-git")
	r.page(t, 1)
	r.keys(t, "Escape")
	screen := r.wait(t, "Quit init?")
	if !strings.Contains(screen, "Answers on page 1/4 are not saved.") || !strings.Contains(screen, "←→ choose · enter press · esc stay · ctrl+c quit") {
		t.Errorf("Esc on page 1 does not ask about page 1:\n%s", screen)
	}
	r.keys(t, "Enter")
	r.next(t, 1)
	screen = r.next(t, 2)
	for _, want := range []string{"[ Skip ]", "ctrl+c quit", initBackHint, "enter next"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the credentials page lacks %q:\n%s", want, screen)
		}
	}
	r.keys(t, "C-c")
	screen = r.wait(t, "Quit init?")
	if strings.Contains(screen, "INIT_EXIT=") || !strings.Contains(screen, "Answers on page 3/4 are not saved.") {
		t.Fatalf("one Ctrl+C ended init, or the question does not name page 3/4:\n%s", screen)
	}
	r.keys(t, "Enter")
	r.page(t, 3)
	r.keys(t, "End", "Down")
	r.wait(t, "enter skip")
	r.keys(t, "Enter")
	r.page(t, 4)
	r.keys(t, "C-c")
	r.wait(t, "Quit init?")
	r.keys(t, "C-c")
	screen = r.wait(t, "INIT_EXIT=")
	if !strings.Contains(screen, "quit at page 4/4") || !strings.Contains(screen, "INIT_EXIT=1") {
		t.Errorf("a confirmed quit must say where init stopped and exit 1:\n%s", screen)
	}
}

// RUN IN PLACE: page 1 counts what Enter runs, with no list under the card; the login yields the
// card behind its frame, and its outcome row prints under what sbx printed.
func TestInitSaysWhatEnterRunsThenShowsEachStep(t *testing.T) {
	r := startInitWith(t, 140, nil, []string{"STUB_SBX_SIGNED_OUT=1"}, "--skip-gh", "--skip-git")
	screen := r.page(t, 1)
	if line := keyLine(screen); !strings.Contains(line, "enter run 1 step") {
		t.Errorf("page 1 does not count the login Enter runs: %q", line)
	}
	for _, gone := range []string{"on enter", "(sbx setup)", "(sbx login)"} {
		if strings.Contains(screen, gone) {
			t.Errorf("page 1 still lists %q under the card:\n%s", gone, screen)
		}
	}
	r.next(t, 1)
	r.next(t, 2)
	r.skip(t)
	r.page(t, 4)
	r.finish(t)
	all := r.waitExit(t)
	if regexp.MustCompile(`step \d+/\d+`).MatchString(all) {
		t.Errorf("a line that stays on screen carries its place:\n%s", all)
	}
	if !regexp.MustCompile(`(?m)^── sbx login ── Docker not signed in · sbx's screen · page 1/4 returns when it exits ──+\n(.*\n)*\n  ✓ Docker login  signed in$`).MatchString(all) {
		t.Errorf("the login was not framed, or its outcome row is missing:\n%s", all)
	}
	for _, gone := range []string{"· verify sbx (sbx diagnose)", "sbx login — signing in to Docker", "sbx setup — running the first-run wizard"} {
		if strings.Contains(all, gone) {
			t.Errorf("the stream still announces %q:\n%s", gone, all)
		}
	}
	if calls := r.calls(t); count(calls, "login") != 1 || count(calls, "setup") != 0 {
		t.Errorf("want sbx login once and no sbx setup with nothing to import; sbx saw:\n%s", strings.Join(calls, "\n"))
	}
}

// THE SBX STEPS RUN ONLY WITH WORK: a signed-in host never hands the terminal to `sbx login`; the
// row says so.
func TestInitSkipsSbxLoginOnASignedInHost(t *testing.T) {
	r := startInit(t, 140, "--skip-gh", "--skip-git")
	screen := r.page(t, 1)
	if !regexp.MustCompile(`✓ Docker login\s+signed in`).MatchString(screen) || !strings.Contains(keyLine(screen), "enter next") {
		t.Errorf("a signed-in host offers a login:\n%s", screen)
	}
	r.next(t, 1)
	r.next(t, 2)
	r.skip(t)
	r.page(t, 4)
	r.finish(t)
	all := r.waitExit(t)
	if count(r.calls(t), "login") != 0 || strings.Contains(all, "── sbx login") {
		t.Errorf("sbx login ran on a signed-in host; sbx saw:\n%s\n%s", strings.Join(r.calls(t), "\n"), all)
	}
	if closed := result(t, all); !regexp.MustCompile(`(?m)^  ✓ Docker login  signed in$`).MatchString(closed) || strings.Contains(closed, "Docker login  later") {
		t.Errorf("a moot login is not signed in, or is listed as later:\n%s", closed)
	}
}

// THE SBX STEPS RUN ONLY WITH WORK: a signed-out host hands `sbx login` the terminal under its
// frame, after a blank row, and a second diagnose decides the outcome.
func TestInitFramesSbxLoginOnASignedOutHost(t *testing.T) {
	r := startInitWith(t, 140, nil, []string{"STUB_SBX_SIGNED_OUT=1"}, "--skip-gh", "--skip-git")
	r.next(t, 1)
	r.next(t, 2)
	r.skip(t)
	r.page(t, 4)
	r.finish(t)
	all := r.waitExit(t)
	if !regexp.MustCompile(`(?m)^\n── sbx login ── Docker not signed in · sbx's screen · page 1/4 returns when it exits ──+$`).MatchString(all) {
		t.Errorf("sbx login ran without its frame after a blank row:\n%s", all)
	}
	if !regexp.MustCompile(`(?m)^  ✓ Docker login  signed in$`).MatchString(all) {
		t.Errorf("the login's outcome row is missing:\n%s", all)
	}
	if calls := r.calls(t); count(calls, "login") != 1 || count(calls, "diagnose --json") != 2 {
		t.Errorf("want sbx login once and two diagnoses, before and after it:\n%s", strings.Join(calls, "\n"))
	}
}

// THE SBX STEPS RUN ONLY WITH WORK: with nothing for sbx setup to import, it never opens, page 4
// and the result say why.
func TestInitSkipsSbxSetupWithNothingToImport(t *testing.T) {
	r := startInit(t, 140, "--skip-gh", "--skip-git")
	r.next(t, 1)
	r.next(t, 2)
	r.skip(t)
	if screen := r.page(t, 4); !regexp.MustCompile(`– sbx setup\s+nothing to import`).MatchString(screen) {
		t.Errorf("page 4 does not say sbx setup has nothing to import:\n%s", screen)
	}
	r.finish(t)
	all := r.waitExit(t)
	calls := r.calls(t)
	if count(calls, "setup") != 0 || !slices.Contains(calls, "secret ls --json") || strings.Contains(all, "── sbx setup") {
		t.Errorf("want the probe and no sbx setup; sbx saw:\n%s\n%s", strings.Join(calls, "\n"), all)
	}
	if !regexp.MustCompile(`(?m)^  – sbx setup     nothing to import$`).MatchString(result(t, all)) {
		t.Errorf("the result does not say sbx setup had nothing to import:\n%s", all)
	}
	if last := lastProveoLine(all); last != "✓ host — ready to run" {
		t.Errorf("init must end on its verdict, not %q:\n%s", last, all)
	}
}

// THE SBX STEPS RUN ONLY WITH WORK: with an env key sbx's store lacks, page 4 asks after init or
// later, and sbx setup opens after the result's items, under its frame; the verdict stays last.
func TestInitOpensSbxSetupAfterTheResult(t *testing.T) {
	r := startInitWith(t, 140, nil, []string{"STUB_SBX_ENV_ONLY=1"}, "--skip-gh", "--skip-git")
	r.next(t, 1)
	r.next(t, 2)
	if calls := r.calls(t); count(calls, "setup") != 0 {
		t.Fatalf("sbx setup ran before the result:\n%s", strings.Join(calls, "\n"))
	}
	r.skip(t)
	if screen := r.page(t, 4); !regexp.MustCompile(`sbx setup\s+\(•\) after init\s+\( \) later`).MatchString(screen) {
		t.Errorf("page 4 does not offer sbx setup after init:\n%s", screen)
	}
	r.finish(t)
	all := r.waitExit(t)
	frame := regexp.MustCompile(`(?m)^── sbx setup ── 1 env key sbx's store lacks · sbx's screen · init ends when it exits ──+$`).FindStringIndex(all)
	results, needs := closedHead.FindStringIndex(all)[0], strings.LastIndex(all, "SSH client config — ")
	finished := regexp.MustCompile(`(?m)^  ✓ sbx setup     finished$`).FindStringIndex(all)
	if frame == nil || finished == nil || results < 0 || needs < results || frame[0] < needs || finished[0] < frame[1] {
		t.Errorf("want the result's items, then the frame, then the outcome:\n%s", all)
	}
	if last := lastProveoLine(all); last != "✓ host — ready to run" {
		t.Errorf("the verdict must stay last, not %q:\n%s", last, all)
	}
	if calls := r.calls(t); count(calls, "setup") != 1 {
		t.Errorf("sbx setup ran %d times, want once:\n%s", count(calls, "setup"), strings.Join(calls, "\n"))
	}
}

// THE SBX STEPS RUN ONLY WITH WORK: with an SSH agent on the host and sbx's forwarding off, sbx
// setup opens after the result, and its frame names the forwarding.
func TestInitOpensSbxSetupWhenForwardingIsOff(t *testing.T) {
	r := startInitWith(t, 140, nil, []string{"SSH_AUTH_SOCK=/tmp/proveo-test-agent.sock", "STUB_SBX_FORWARDING=false"}, "--skip-gh", "--skip-git")
	r.next(t, 1)
	r.next(t, 2)
	r.skip(t)
	r.page(t, 4)
	r.finish(t)
	all := r.waitExit(t)
	if !regexp.MustCompile(`(?m)^── sbx setup ── SSH agent forwarding off · sbx's screen · init ends when it exits ──+$`).MatchString(all) {
		t.Errorf("sbx setup ran without a frame that names the forwarding:\n%s", all)
	}
	if calls := r.calls(t); count(calls, "setup") != 1 || !slices.Contains(calls, "settings get --json ssh.agentForwardingEnabled") {
		t.Errorf("want the forwarding probe and sbx setup once; sbx saw:\n%s", strings.Join(calls, "\n"))
	}
}

// What the operator types while sbx's own setup runs, after the result, reaches it, and nothing of it reaches proveo.
func TestInitHandsTheTerminalWhollyToSbxSetup(t *testing.T) {
	r := startInitWith(t, 140, nil, []string{"STUB_SBX_SETUP_READS=1", "STUB_SBX_ENV_ONLY=1"}, "--skip-gh", "--skip-git")
	r.next(t, 1)
	r.next(t, 2)
	r.skip(t)
	r.page(t, 4)
	r.finish(t)
	r.wait(t, "setup asks:")
	r.keys(t, "typed-for-setup", "Enter")
	screen, err := r.sess.WaitFor("INIT_EXIT=", 15*time.Second)
	if calls := r.calls(t); err != nil || !slices.Contains(calls, "setup read: typed-for-setup") {
		t.Fatalf("sbx setup did not get the line typed for it (%v); sbx saw:\n%s\nscreen:\n%s", err, strings.Join(calls, "\n"), screen)
	}
	r.waitExit(t)
}

// init opens on page 1 with every check before it asks anything, and closes on what waits, the
// checks sbx failed, and its verdict on the host.
func TestInitOpensOnTheChecksAndClosesOnTheResult(t *testing.T) {
	r := startInit(t, 140, "--skip-gh", "--skip-git")
	screen := r.page(t, 1)
	for _, want := range []string{"Prepare sbx on this ", "── checks ─", "── sbx ─", "esc quit", "ctrl+c quit",
		"Binary version — update available: v9.9.9", "Download the latest release: https://example.invalid/sbx/v9.9.9",
		"! SSH client config — an unmanaged sandbox Host stanza", "a run does not need this check; it affects IDE attach"} {
		if !strings.Contains(screen, want) {
			t.Errorf("page 1 lacks %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, initBackHint) || strings.Contains(screen, "from the link above") {
		t.Errorf("page 1 offers a step back, or a fix points at another line:\n%s", screen)
	}
	r.next(t, 1)
	r.next(t, 2)
	r.skip(t)
	r.page(t, 4)
	r.finish(t)
	all := r.waitExit(t)
	results := result(t, all)
	for _, re := range []string{`(?m)^  – sbx install   kept 0\.50\.0`, `(?m)^  ✓ Docker login  signed in$`,
		`(?m)^  – git identity  later · --skip-git$`, `(?m)^  – GitHub        later · --skip-gh$`, `(?m)^  ✓ codex         login held by sbx as openai$`,
		`(?m)^  ✓ operator      Executor \(default\)$`,
		`(?m)^  ! Binary version — update available: v9\.9\.9\n      Download the latest release: https://example\.invalid/sbx/v9\.9\.9$`,
		`(?m)^  ! SSH client config — an unmanaged sandbox Host stanza`, `(?m)^\s+a run does not need this check; it affects IDE attach`} {
		if !regexp.MustCompile(re).MatchString(results) {
			t.Errorf("the result lacks %s:\n%s", re, results)
		}
	}
	if regexp.MustCompile(`(?m)^  – (Docker login|sbx setup)\s+later`).MatchString(results) {
		t.Errorf("a step with nothing to do is listed as left for later:\n%s", results)
	}
	if last := lastProveoLine(all); last != "✓ host — ready to run" {
		t.Errorf("init must end on its verdict, not %q:\n%s", last, all)
	}
	for _, gone := range []string{"done:", "next:", "later:", "not ready to run", "proveo init`", "⚠"} {
		if strings.Contains(all, gone) {
			t.Errorf("the stream still says %q:\n%s", gone, all)
		}
	}
	if calls := r.calls(t); count(calls, "login") != 0 || count(calls, "setup") != 0 || count(calls, "diagnose --json") != 1 {
		t.Errorf("a signed-in host with nothing to import ran sbx login or sbx setup, or diagnosed twice; sbx saw:\n%s", strings.Join(calls, "\n"))
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
	r.next(t, 1)
	r.next(t, 2)
	r.skip(t)
	r.page(t, 4)
	r.finish(t)
	all := r.waitExit(t)
	if last := lastProveoLine(all); last != "✓ host — ready to run" {
		t.Errorf("a ready host must end on its verdict, not %q:\n%s", last, all)
	}
	result(t, all)
	if regexp.MustCompile(`(?m)^\s*─{6} \S+ ─+$`).MatchString(all) {
		t.Errorf("init drew a section heading; the closed checklist has one heading:\n%s", all)
	}
	if regexp.MustCompile(`(?m)^  [!×] `).MatchString(result(t, all)) {
		t.Errorf("a ready host's result names something that needs the operator:\n%s", all)
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
	for _, re := range []string{`(?m)^  × Daemon — not running$`, `(?m)^\s+Start Docker Sandboxes, then run sbx diagnose again$`,
		`(?m)^  – credentials   not run$`, `(?m)^  – operator      not run$`} {
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
	if strings.Contains(screen, "PATH") || !strings.Contains(keyLine(screen), "enter next") {
		t.Errorf("page 1 names a PATH step for an sbx the prefix does not hold:\n%s", screen)
	}
	r.next(t, 1)
	r.next(t, 2)
	r.skip(t)
	r.page(t, 4)
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
	if !regexp.MustCompile(`(?m)^  ! git identity  not set$`).MatchString(all) {
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
// and the plain closed checklist prints once, "<tag><label> — <outcome>" per row, under one heading,
// ending on the verdict.
func TestInitYesInPlainOutputCarriesNoStepPlaces(t *testing.T) {
	got, calls := initYesPlain(t)
	if strings.ContainsAny(got, "\x1b\r") || regexp.MustCompile(`step \d+/\d+`).MatchString(got) {
		t.Errorf("plain output carries a step place or terminal control:\n%q", got)
	}
	for _, re := range []string{`(?m)^── proveo init ── \S+/\S+ · sbx 0\.50\.0 ──+$`, `(?m)^sbx install — kept 0\.50\.0 · .*sbx$`,
		`(?m)^ok: Docker login — signed in$`, `(?m)^warn: git identity — not set$`, `(?m)^ok: codex — login held by sbx as openai$`,
		`(?m)^operator — Executor \(default\)$`, `(?m)^sbx setup — nothing to import$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("plain output lacks %s:\n%s", re, got)
		}
	}
	if last := lastLine(got); last != "ok: host — ready to run" {
		t.Errorf("the plain closed checklist ends %q, want the verdict:\n%s", last, got)
	}
	if count(calls, "setup") != 0 || count(calls, "login") != 0 || strings.Contains(got, "── sbx") {
		t.Errorf("--yes must hand the terminal to neither sbx login nor sbx setup:\n%s\n--- sbx calls ---\n%s", got, strings.Join(calls, "\n"))
	}
	if strings.Contains(got, "verify sbx") {
		t.Errorf("verify left a line in plain output:\n%s", got)
	}
	if n := len(closedHead.FindAllString(got, -1)); n != 1 || regexp.MustCompile(`(?m)^\s*-{6} \S+ -{6}$`).MatchString(got) {
		t.Errorf("want the closed checklist's heading once and no section heading:\n%s", got)
	}
}

// lastLine is the last non-blank line of out.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines[len(lines)-1]
}

// THE SBX STEPS RUN ONLY WITH WORK: `init --yes` on a signed-out host runs no login, names Docker
// login once, as the readiness warning with its fix, and exits 0.
func TestInitYesOnASignedOutHostWarnsAndExitsZero(t *testing.T) {
	got, calls := initYesPlain(t, "STUB_SBX_SIGNED_OUT=1")
	if !regexp.MustCompile(`(?m)^warn: Docker login — not signed in$`).MatchString(got) || !strings.Contains(got, "sbx login --username <name> --password-stdin") {
		t.Errorf("a signed-out host lacks the Docker login warning and its fix:\n%s", got)
	}
	if n := strings.Count(got, "Docker login"); n != 1 {
		t.Errorf("the result names Docker login %d times, want once:\n%s", n, got)
	}
	if count(calls, "login") != 0 || strings.Contains(got, "── sbx login") {
		t.Errorf("--yes handed the terminal to sbx login:\n%s\n--- sbx calls ---\n%s", got, strings.Join(calls, "\n"))
	}
	if !regexp.MustCompile(`(?m)^ok: host — ready to run$`).MatchString(got) {
		t.Errorf("a signed-out host must not block the verdict:\n%s", got)
	}
}

// initYesPlain runs `proveo init --yes` with stderr a pipe against initStubSbx, with env for the
// stub; init must exit 0. It returns stderr and the stub's calls.
func initYesPlain(t *testing.T, env ...string) (stderr string, calls []string) {
	t.Helper()
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
	cmd.Env = append([]string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(), "TERM=xterm-256color",
		"PATH=" + bin + ":/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"STUB_SBX_LOG=" + filepath.Join(state, "sbx.log"), "STUB_SBX_SECRETS=" + filepath.Join(state, "secrets")}, env...)
	var out strings.Builder
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("proveo init --yes: %v\n%s", err, out.String())
	}
	log, _ := os.ReadFile(filepath.Join(state, "sbx.log"))
	return out.String(), strings.Split(strings.TrimSpace(string(log)), "\n")
}

// A Ctrl+C while sbx setup has the terminal, after the result, reaches it; init then says it
// stopped, prints its verdict, and dies of SIGINT, so a shell like install.sh's stops too.
func TestInitCtrlCInSbxsWizardEndsOnWhereItStopped(t *testing.T) {
	r := startInitWrapped(t, 140, nil, []string{"STUB_SBX_SETUP_READS=1", "STUB_SBX_ENV_ONLY=1"}, installerWrapper, "--skip-gh", "--skip-git")
	r.next(t, 1)
	r.next(t, 2)
	r.skip(t)
	r.page(t, 4)
	r.finish(t)
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
	if !regexp.MustCompile(`(?m)^  ! sbx setup     stopped · Ctrl\+C ended it\n\n  ✓ host — ready to run\nINIT_EXIT=130`).MatchString(all) {
		t.Errorf("init must say sbx setup stopped and why, then end on its verdict:\n%s", all)
	}
	if strings.Contains(result(t, all), "not run") {
		t.Errorf("init must name nothing as never run:\n%s", all)
	}
	if n := count(r.calls(t), "diagnose --json"); n != 1 {
		t.Errorf("sbx diagnose ran %d times; once for page 1, whose checks judge the host:\n%s", n, strings.Join(r.calls(t), "\n"))
	}
}

// HAND-OFF FAILURE: a `gh auth login` that fails keeps its own error; init prints no × for it,
// ends on where it stopped, names what never ran, and exits 1.
func TestInitAFailedGhAuthLoginEndsOnWhereItStopped(t *testing.T) {
	r := startInitTools(t, 140, map[string]string{
		"gh": "#!/bin/sh\ncase \"$1 $2\" in\n \"auth login\") echo 'gh: login aborted' >&2; exit 1 ;;\n *) exit 1 ;;\nesac\n",
	}, "--skip-login", "--skip-setup", "--skip-git")
	r.next(t, 1)
	r.keys(t, "Enter")
	r.wait(t, "INIT_EXIT=")
	all, err := r.sess.CaptureAll()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^  ! init — stopped at GitHub\n\s+host ready to run\nINIT_EXIT=1`).MatchString(all) {
		t.Errorf("a failed gh auth login must end init on where it stopped, exit 1:\n%s", all)
	}
	for _, re := range []string{`(?m)^gh: login aborted$`, `(?m)^── gh auth login ── gh's screen · page 2/4 returns when it exits ──+$`,
		`(?m)^  ! GitHub        stopped · exit status 1$`, `(?m)^  – credentials   not run$`, `(?m)^  – operator      not run$`} {
		if !regexp.MustCompile(re).MatchString(all) {
			t.Errorf("the screen lacks %s:\n%s", re, all)
		}
	}
	if strings.Contains(all, "× ") || strings.Contains(all, "did not complete") || strings.Contains(all, initPageLabel(3)) {
		t.Errorf("init restated gh's failure or went on to the credentials page:\n%s", all)
	}
}

// waitStopped waits for init to end and returns everything it printed; a stop ends it by SIGINT.
func (r *initRun) waitStopped(t *testing.T, at string) string {
	t.Helper()
	r.wait(t, "INIT_EXIT=")
	all, err := r.sess.CaptureAll()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^  ! init — stopped at `+regexp.QuoteMeta(at)+`\n\s+Ctrl\+C ended it$`).MatchString(all) || !strings.Contains(all, "INIT_EXIT=130") {
		t.Errorf("a Ctrl+C on the running card must end init stopped at %s, by SIGINT:\n%s", at, all)
	}
	return all
}

// RUN IN PLACE: Ctrl+C on page 3's running card lets the row it came on finish, stores no later
// key, and ends init stopped at credentials before page 4.
func TestInitCtrlCOnPageThreeStopsAfterItsRow(t *testing.T) {
	r := startInitWith(t, 140, nil, []string{"STUB_SBX_SLOW_SET=2"}, "--skip-login", "--skip-setup", "--skip-gh", "--skip-git")
	r.next(t, 1)
	r.next(t, 2)
	r.keys(t, "Down", "Down", "Down")
	r.waitMatch(t, `› opencode\s+✓`)
	r.keys(t, "t", "o", "k", "Down", "k", "e", "y")
	r.waitMatch(t, `enter run 2 steps`)
	r.keys(t, "Enter")
	r.wait(t, "storing in sbx")
	r.keys(t, "C-c")
	r.wait(t, "stopping after opencode")
	all := r.waitStopped(t, "credentials")
	sets := 0
	for _, c := range r.calls(t) {
		if strings.HasPrefix(c, "secret set") {
			sets++
		}
	}
	if sets != 1 {
		t.Errorf("sbx stored %d secrets after the stop; want the row the Ctrl+C came on alone:\n%s", sets, strings.Join(r.calls(t), "\n"))
	}
	if strings.Contains(all, "Name the operator") {
		t.Errorf("page 4 opened after the stop:\n%s", all)
	}
}

// RUN IN PLACE: after Ctrl+C on page 2's running card, GitHub's step hands the terminal to no
// program and opens no sub-page.
func TestInitCtrlCDuringGitHubHandsNothingOut(t *testing.T) {
	r := startInitTools(t, 140, map[string]string{
		"gh": "#!/bin/sh\nprintf 'gh %s\\n' \"$*\" >> \"$STUB_SBX_LOG\"\ncase \"$1 $2\" in\n \"auth token\") sleep 3; exit 1 ;;\n *) exit 0 ;;\nesac\n",
	}, "--skip-login", "--skip-setup", "--skip-git")
	r.next(t, 1)
	r.keys(t, "Enter")
	r.waitMatch(t, `running \d of \d — GitHub`)
	r.keys(t, "C-c")
	r.wait(t, "stopping after GitHub")
	all := r.waitStopped(t, "GitHub")
	if strings.Contains(all, "── gh auth login ──") || strings.Contains(all, "Choose sandbox Git access") {
		t.Errorf("GitHub's step handed the terminal out or opened its sub-page after the stop:\n%s", all)
	}
	for _, c := range r.calls(t) {
		if strings.HasPrefix(c, "gh auth login") {
			t.Errorf("gh auth login ran after the stop:\n%s", strings.Join(r.calls(t), "\n"))
		}
	}
}
