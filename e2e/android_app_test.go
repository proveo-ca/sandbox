//go:build e2e

// SPEC: _spec/internal/sbx/host-android-adb.puml

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

const (
	helloPackage  = "ca.proveo.hello"
	absentPackage = "ca.proveo.absent"
	glimmer       = "muse-glimmer:30b-mlx"
)

// androidHarness is one harness row: where its session records tool invocations
// (not the tool catalog), and what a mobile MCP call looks like there.
type androidHarness struct {
	target   string
	evidence string // guest shell: prints the harness's recorded mobile tool names
	toolMark string
	dialogs  []tuiDialog
}

// tuiDialog is a first-run question a harness asks, and the keys a user answers it with.
type tuiDialog struct {
	marker string
	keys   []string
}

var androidHarnesses = []androidHarness{
	{"opencode", `grep -rhoasE '"tool": ?"mobile_mobile_[a-z_]*"' "$HOME/.local/share/opencode" 2>/dev/null | grep -o 'mobile_mobile_[a-z_]*' | sort -u`, "mobile_mobile_", nil},
	{"claudecode", `grep -rhoasE --include='*.jsonl' '"type":"tool_use","id":"[^"]*","name":"mcp__mobile__[a-z_]*"' "$HOME/.claude" /proveo-home/.claude 2>/dev/null | grep -o 'mcp__mobile__[a-z_]*' | sort -u`, "mcp__mobile__",
		[]tuiDialog{{"Make auto mode your default permission mode", []string{"Down", "Enter"}}}},
}

// TestAndroidAppInWorkspaceInstallsOnTheHostEmulator drives each harness the way a
// user does: tick android in the form, let the seed provision the build from the
// workspace's sources, type `!./install.sh` into the agent's TUI, then ask the
// agent — on a local model — to confirm the install through its mobile MCP tools.
func TestAndroidAppInWorkspaceInstallsOnTheHostEmulator(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	requireTmux(t)
	if hostadb.EmulatorBinary(os.Getenv) == "" || hostadb.AdbBinary(os.Getenv) == "" {
		t.Skip("no Android SDK emulator/adb on this host")
	}
	if tags, err := ollamaTags(); err != nil || !containsString(tags, glimmer) {
		t.Skipf("host Ollama lacks %s (%v)", glimmer, err)
	}
	port, err := hostadb.Port(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !hostadb.Ready(port) {
		t.Cleanup(func() { stopEmulators(t, port) })
	}
	proveoBin := buildProveo(t)
	for _, h := range androidHarnesses {
		t.Run(h.target, func(t *testing.T) {
			harnessImage(t, h.target)
			sweepSandboxesAfter(t)
			androidAppRow(t, proveoBin, port, h)
		})
	}
}

func androidAppRow(t *testing.T, proveoBin string, port int, h androidHarness) {
	t.Helper()
	hostADB := func(args ...string) string {
		out, _ := exec.Command(hostadb.AdbBinary(os.Getenv), append([]string{"-P", strconv.Itoa(port)}, args...)...).CombinedOutput()
		return string(out)
	}
	t.Cleanup(func() { hostADB("uninstall", helloPackage) })

	work := t.TempDir()
	if out, err := exec.Command("cp", "-R", filepath.Join(repoRoot(t), "e2e", "testdata", "android-hello")+"/.", work).CombinedOutput(); err != nil {
		t.Fatalf("copy fixture: %v\n%s", err, out)
	}
	before, _ := sbxSandboxNames()

	sess := tmux.New(fmt.Sprintf("proveo-android-%s-%d", h.target, time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)
	cmd := []string{"env", "PROVEO_WIZARD=on", "PROVEO_HOME=" + t.TempDir(), "PROVEO_MOUNT_GH_CONFIG=0",
		proveoBin, "run", h.target, "--local-model", glimmer, "--egress-mode", "open", "--input", work}
	runLog := filepath.Join(t.TempDir(), "run.log")
	if err := sess.Start(220, 60, "sh", "-c", shellQuote(cmd)+" 2>&1 | tee "+shellQuote([]string{runLog})); err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("proveo run log (tail):\n%s", tail(readFile(runLog), 40))
		}
	})
	tickAndroid(t, sess)
	if err := sess.Enter(); err != nil {
		t.Fatalf("accept the form: %v", err)
	}

	name := awaitNewSandbox(t, sess, before, 5*time.Minute)
	guest := func(script string) string {
		out, _ := exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "bash", "-lc", script).CombinedOutput()
		return string(out)
	}
	awaitGuestSeed(t, guest, h.target, durationEnv(t, "PROVEO_TEST_SEED_TIMEOUT", 20*time.Minute))

	reach := guest(fmt.Sprintf(`ADB_SERVER_SOCKET=tcp:host.docker.internal:%d adb devices`, port))
	if !deviceLineRE.MatchString(reach) {
		t.Fatalf("the sandbox does not reach the host emulator:\n%s", reach)
	}
	if seeded := guest(`grep -h "Detected an Android build" /var/log/sbx-kit-startup.log`); !strings.Contains(seeded, "Detected an Android build") {
		t.Fatalf("the seed did not provision the Android build from the workspace sources:\n%s", guest(`tail -40 /var/log/sbx-kit-startup.log`))
	}
	hostADB("uninstall", helloPackage)

	awaitQuietScreen(sess, 2*time.Minute)
	answerDialogs(t, sess, h.dialogs)
	typeLine(t, sess, "!./install.sh")
	installed := waitUntil(durationEnv(t, "PROVEO_ANDROID_BUILD_TIMEOUT", 25*time.Minute), func() bool {
		return strings.Contains(hostADB("shell", "pm", "list", "packages", helloPackage), "package:"+helloPackage)
	})
	if !installed {
		screen, _ := sess.CaptureAll()
		t.Fatalf("`!./install.sh` did not install %s on the host emulator:\n%s", helloPackage, tail(screen, 60))
	}
	if started := hostADB("shell", "am", "start", "-W", "-n", helloPackage+"/.MainActivity"); !strings.Contains(started, "Status: ok") {
		t.Errorf("the installed app does not launch:\n%s", started)
	}

	awaitQuietScreen(sess, 2*time.Minute)
	answerDialogs(t, sess, h.dialogs)
	typeLine(t, sess, fmt.Sprintf("Use your mobile MCP tools to list the apps on the connected Android device. "+
		"Then answer with exactly two lines: MOBILE-CHECK %s=yes or =no, and MOBILE-CHECK %s=yes or =no.",
		helloPackage, absentPackage))
	yes := regexp.MustCompile(`MOBILE-CHECK\s*` + regexp.QuoteMeta(helloPackage) + `\s*=\s*yes`)
	no := regexp.MustCompile(`MOBILE-CHECK\s*` + regexp.QuoteMeta(absentPackage) + `\s*=\s*no`)
	var screen string
	answered := waitUntil(durationEnv(t, "PROVEO_TEST_TIMEOUT", 30*time.Minute), func() bool {
		screen, _ = sess.CaptureAll()
		return yes.MatchString(screen) && no.MatchString(screen)
	})
	if !answered {
		t.Fatalf("the agent did not confirm %s (and deny %s):\n%s", helloPackage, absentPackage, tail(screen, 60))
	}
	calls := guest(h.evidence)
	if !strings.Contains(calls, h.toolMark) {
		t.Fatalf("the agent answered without a recorded mobile MCP call (%s):\n%s", h.toolMark, calls)
	}
	t.Logf("%s: mobile tool calls recorded: %s", h.target, strings.Join(strings.Fields(calls), " "))
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return cond()
}

// awaitNewSandbox returns the sandbox this run created.
func awaitNewSandbox(t *testing.T, sess *tmux.Session, before map[string]bool, timeout time.Duration) string {
	t.Helper()
	var name string
	if !waitUntil(timeout, func() bool {
		if fresh := newSandboxes(before); len(fresh) > 0 {
			name = fresh[0]
			return true
		}
		return false
	}) {
		screen, _ := sess.CaptureAll()
		t.Fatalf("no sandbox appeared:\n%s", tail(screen, 40))
	}
	return name
}

// awaitGuestSeed holds until every Kit startup command has finished, proveo's posture kit included.
func awaitGuestSeed(t *testing.T, guest func(string) string, target string, timeout time.Duration) {
	t.Helper()
	probe := `printf 'SEED %s %s\n' "$(grep -c '^> ' /var/log/sbx-kit-startup.log 2>/dev/null || echo 0)" "$(grep -cE '^(ok|fail|error)' /var/log/sbx-kit-startup.log 2>/dev/null || echo 0)"`
	posture := fmt.Sprintf(`grep -cE '^ok .*-startup-%s-posture/' /var/log/sbx-kit-startup.log 2>/dev/null || echo 0`, target)
	if !waitUntil(timeout, func() bool {
		started, done := seedCounts(guest(probe))
		if started == 0 || done < started {
			return false
		}
		n, _ := strconv.Atoi(strings.TrimSpace(guest(posture)))
		return n > 0
	}) {
		t.Fatalf("the seed did not finish within %s:\n%s", timeout, guest(`tail -40 /var/log/sbx-kit-startup.log`))
	}
}

// awaitQuietScreen waits until the pane stops changing, the cue that the TUI takes input.
func awaitQuietScreen(sess *tmux.Session, timeout time.Duration) {
	var last string
	stable := 0
	waitUntil(timeout, func() bool {
		now, _ := sess.Capture()
		if now == last {
			stable++
		} else {
			stable, last = 0, now
		}
		return stable >= 2
	})
}

// answerDialogs answers every listed first-run question that is on screen.
func answerDialogs(t *testing.T, sess *tmux.Session, dialogs []tuiDialog) {
	t.Helper()
	for _, d := range dialogs {
		screen, _ := sess.Capture()
		if !strings.Contains(screen, d.marker) {
			continue
		}
		t.Logf("answering first-run dialog %q with %v", d.marker, d.keys)
		for _, k := range d.keys {
			if err := sess.SendKeys(k); err != nil {
				t.Fatalf("send %s: %v", k, err)
			}
			time.Sleep(300 * time.Millisecond)
		}
		if !waitUntil(10*time.Second, func() bool {
			now, _ := sess.Capture()
			return !strings.Contains(now, d.marker)
		}) {
			now, _ := sess.Capture()
			t.Fatalf("dialog %q still open after %v:\n%s", d.marker, d.keys, now)
		}
		awaitQuietScreen(sess, 30*time.Second)
	}
}

// typeLine types line, confirms the TUI shows it in the prompt (a leading `!` may become a mode), then submits it.
func typeLine(t *testing.T, sess *tmux.Session, line string) {
	t.Helper()
	if err := sess.SendText(line); err != nil {
		t.Fatalf("type %q: %v", line, err)
	}
	head := strings.TrimPrefix(line, "!")
	if len(head) > 24 {
		head = head[:24]
	}
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if !waitUntil(10*time.Second, func() bool {
		screen, _ := sess.Capture()
		return strings.Contains(squash(screen), squash(head))
	}) {
		screen, _ := sess.Capture()
		t.Fatalf("typed %q but the prompt does not show it:\n%s", head, screen)
	}
	if err := sess.Enter(); err != nil {
		t.Fatalf("enter: %v", err)
	}
}
