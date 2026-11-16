//go:build e2e

// SPEC: _spec/internal/sbx/host-android-adb.puml

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/hostdev"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/schedule"
	"github.com/proveo-ca/proveo/internal/testx"
)

var deviceLineRE = regexp.MustCompile(`emulator-\d+\s+device\b`)

// TestAndroidTickedInTheChoiceFormOpensTheHostEmulator ticks "android" with
// keystrokes in the real choice form, then proves the run
// booted an emulator on the host and handed the sandbox a working route to it.
func TestAndroidTickedInTheChoiceFormOpensTheHostEmulator(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	const target = "claudecode"
	requireTmux(t)
	harnessImage(t, target)
	sweepSandboxesAfter(t)
	if hostdev.EmulatorBinary(os.Getenv) == "" || hostdev.AdbBinary(os.Getenv) == "" {
		t.Skip("no Android SDK emulator/adb on this host")
	}
	port, err := hostdev.ADBPort(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if hostdev.ADBReady(port) {
		t.Skipf("a device is already on 127.0.0.1:%d — stop it so the run has to open one", port)
	}
	t.Cleanup(func() { stopEmulators(t, port) })

	proveoBin := buildProveo(t)
	home, work := t.TempDir(), t.TempDir()
	before, _ := sbxSandboxNames()
	sess := schedule.NewTmux(fmt.Sprintf("proveo-android-%d", time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)
	cmd := []string{"env", "PROVEO_WIZARD=on", "PROVEO_HOME=" + home, "PROVEO_MOUNT_GH_CONFIG=0",
		proveoBin, "run", target, "--input", work}
	if err := sess.Start(220, 60, cmd...); err != nil {
		t.Fatalf("start session: %v", err)
	}

	tickAndroid(t, sess)
	if err := sess.Enter(); err != nil {
		t.Fatalf("accept the form: %v", err)
	}

	k := awaitKit(t, sess, before, target, work)

	log, _ := sess.CaptureAll()
	runLog := readIn(home, filepath.Join("logs", "latest.log"))
	if !strings.Contains(runLog, "android: booting AVD") || !strings.Contains(runLog, "booted") {
		t.Fatalf("the run log does not say the run opened an emulator on the host:\n%s", tail(runLog, 40))
	}
	if !regexp.MustCompile(`android:\s+emulator-\d+ \(AVD `).MatchString(log) {
		t.Errorf("the interface block does not name the emulator the run booted:\n%s", log)
	}
	if strings.Contains(log, "android: booting AVD") {
		t.Errorf("the emulator's boot progress reached the screen; it belongs in the run log:\n%s", log)
	}
	if !strings.Contains(log, "the agent controls every device on that adb server") {
		t.Errorf("the run did not warn about adb's reach:\n%s", log)
	}
	if !hostdev.ADBReady(port) {
		t.Fatalf("no usable device on the host adb server 127.0.0.1:%d after the run opened one", port)
	}

	probe := `ip="$(getent ahostsv4 host.docker.internal | awk 'NR==1{print $1}')"
echo "PORT=${PROVEO_HOST_ADB_PORT:-unset}"
ADB_SERVER_SOCKET="tcp:${ip}:${PROVEO_HOST_ADB_PORT}" adb devices`
	out, status := k.exec(probe, 60*time.Second)
	if status != 0 {
		t.Fatalf("sandbox probe exited %d:\n%s", status, out)
	}
	if !strings.Contains(out, fmt.Sprintf("PORT=%d", port)) {
		t.Errorf("the sandbox did not receive %s=%d:\n%s", hostdev.EnvADBPort, port, out)
	}
	if !deviceLineRE.MatchString(out) {
		t.Errorf("the sandbox's adb does not see the emulator the run opened:\n%s", out)
	}
	t.Logf("sandbox view:\n%s", strings.TrimSpace(out))
}

// TestAndroidLeftUntickedOpensNoEmulator is the control: the same run, the
// form accepted as it comes up, and the host must stay without a device.
func TestAndroidLeftUntickedOpensNoEmulator(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	const target = "claudecode"
	requireTmux(t)
	harnessImage(t, target)
	sweepSandboxesAfter(t)
	if hostdev.AdbBinary(os.Getenv) == "" {
		t.Skip("no Android SDK adb on this host")
	}
	port, err := hostdev.ADBPort(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if hostdev.ADBReady(port) {
		t.Skipf("a device is already on 127.0.0.1:%d — the control needs none", port)
	}

	proveoBin := buildProveo(t)
	home, work := t.TempDir(), t.TempDir()
	before, _ := sbxSandboxNames()
	sess := schedule.NewTmux(fmt.Sprintf("proveo-android-off-%d", time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)
	cmd := []string{"env", "PROVEO_WIZARD=on", "PROVEO_HOME=" + home, "PROVEO_MOUNT_GH_CONFIG=0",
		proveoBin, "run", target, "--input", work}
	if err := sess.Start(220, 60, cmd...); err != nil {
		t.Fatalf("start session: %v", err)
	}
	if _, err := sess.WaitFor("[ ] "+hostdev.AddonAndroid, 90*time.Second); err != nil {
		screen, _ := sess.Capture()
		t.Fatalf("the form does not offer an unticked android box:\n%s", screen)
	}
	acceptChoicePrompt(t, sess, target)
	k := awaitKit(t, sess, before, target, work)

	log, _ := sess.CaptureAll()
	if strings.Contains(log, "android:") {
		t.Errorf("an unticked android box still reached the host side:\n%s", log)
	}
	if hostdev.ADBReady(port) {
		t.Errorf("a device appeared on 127.0.0.1:%d although android was never ticked", port)
	}
	out, _ := k.exec(`echo "PORT=${PROVEO_HOST_ADB_PORT:-unset}"`, 30*time.Second)
	if !strings.Contains(out, "PORT=unset") {
		t.Errorf("the sandbox received %s without the add-on:\n%s", hostdev.EnvADBPort, out)
	}
}

// stopEmulators kills every emulator on the server, offline ones included, and waits until none is listed.
func stopEmulators(t *testing.T, port int) {
	t.Helper()
	adb := hostdev.AdbBinary(os.Getenv)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		all, err := hostdev.AllDevices(port)
		if err != nil {
			return
		}
		left := 0
		for _, d := range all {
			if d.Emulator() {
				left++
				_ = exec.Command(adb, "-P", strconv.Itoa(port), "-s", d.Serial, "emu", "kill").Run()
			}
		}
		if left == 0 {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Errorf("an emulator on 127.0.0.1:%d outlived the test", port)
}

// tickAndroid walks the form the way an operator does: onto the devices row,
// right onto the android box, space to tick it.
func tickAndroid(t *testing.T, sess *schedule.TmuxSession) {
	t.Helper()
	if _, err := sess.WaitFor("enter accept", 90*time.Second); err != nil {
		t.Fatalf("choice prompt never appeared: %v", err)
	}
	focused := func(opt string) bool {
		screen, _ := sess.Capture()
		return strings.Contains(screen, "› "+opt+" —")
	}
	step := func(key, opt string, max int) {
		for i := 0; i < max && !focused(opt); i++ {
			if err := sess.SendKeys(key); err != nil {
				t.Fatalf("send %s: %v", key, err)
			}
			time.Sleep(150 * time.Millisecond)
		}
		if !focused(opt) {
			screen, _ := sess.Capture()
			t.Fatalf("never reached %q with %s:\n%s", opt, key, screen)
		}
	}
	onDevices := func() bool {
		screen, _ := sess.Capture()
		return strings.Contains(screen, "› devices ")
	}
	for i := 0; i < 12 && !onDevices(); i++ {
		if err := sess.SendKeys("Down"); err != nil {
			t.Fatalf("send Down: %v", err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	step("Right", hostdev.AddonAndroid, 6)
	if err := sess.SendKeys("Space"); err != nil {
		t.Fatalf("send Space: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if screen, _ := sess.Capture(); strings.Contains(screen, "[x] "+hostdev.AddonAndroid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	screen, _ := sess.Capture()
	t.Fatalf("the android box is not ticked after Space:\n%s", screen)
}

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
	{"opencode", opencodeToolCalls, "mobile.mobile_", nil},
	{"claudecode", `grep -rhoasE --include='*.jsonl' '"type":"tool_use","id":"[^"]*","name":"mcp__mobile__[a-z_]*"' "$HOME/.claude" /proveo-home/.claude 2>/dev/null | grep -o 'mcp__mobile__[a-z_]*' | sort -u`, "mcp__mobile__",
		[]tuiDialog{{"Make auto mode your default permission mode", []string{"Down", "Enter"}}}},
}

// opencodeToolCalls lists opencode 2.x's tool calls: part names and the completed MCP calls an `execute` part made.
// SPEC: _spec/internal/sbx/host-android-adb.puml (THE USER-STYLE E2E)
const opencodeToolCalls = `d=$(mktemp -d) && cp "$HOME"/.local/share/opencode/opencode.db* "$d"/ 2>/dev/null && python3 - "$d/opencode.db" <<'PY'
import json, sqlite3, sys
names = set()
for (data,) in sqlite3.connect(sys.argv[1]).execute("select data from session_message where type = 'assistant'"):
    for part in json.loads(data).get("content", []):
        if part.get("type") == "tool":
            names.add(part.get("name", ""))
            for call in part.get("state", {}).get("metadata", {}).get("toolCalls", []):
                if call.get("status") == "completed":
                    names.add(call.get("tool", ""))
print("\n".join(sorted(n for n in names if n)))
PY`

// TestAndroidAppInWorkspaceInstallsOnTheHostEmulator drives each harness the way a
// user does: tick android in the form, let the seed provision the build from the
// workspace's sources, type `!./install.sh` into the agent's TUI, then ask the
// agent — on a local model — to confirm the install through its mobile MCP tools.
func TestAndroidAppInWorkspaceInstallsOnTheHostEmulator(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	requireTmux(t)
	if hostdev.EmulatorBinary(os.Getenv) == "" || hostdev.AdbBinary(os.Getenv) == "" {
		t.Skip("no Android SDK emulator/adb on this host")
	}
	if tags, err := ollamaTags(); err != nil || !containsString(tags, glimmer) {
		t.Skipf("host Ollama lacks %s (%v)", glimmer, err)
	}
	if err := sbx.LocalModelFits(glimmer, 0, os.Getenv); err != nil {
		t.Skipf("local model %s does not fit this host right now: %v", glimmer, err)
	}
	port, err := hostdev.ADBPort(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !hostdev.ADBReady(port) {
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
		out, _ := exec.Command(hostdev.AdbBinary(os.Getenv), append([]string{"-P", strconv.Itoa(port)}, args...)...).CombinedOutput()
		return string(out)
	}
	t.Cleanup(func() { hostADB("uninstall", helloPackage) })

	work := testx.Extract(t, "android-hello")
	if err := os.Chmod(filepath.Join(work, "install.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := sbxSandboxNames()

	sess := schedule.NewTmux(fmt.Sprintf("proveo-android-%s-%d", h.target, time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)
	cmd := []string{"env", "PROVEO_WIZARD=on", "PROVEO_HOME=" + t.TempDir(), "PROVEO_MOUNT_GH_CONFIG=0",
		proveoBin, "run", h.target, "--local-model", glimmer, "--input", work}
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
	return slices.Contains(xs, x)
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
func awaitNewSandbox(t *testing.T, sess *schedule.TmuxSession, before map[string]bool, timeout time.Duration) string {
	t.Helper()
	var name string
	if !waitUntil(timeout, func() bool {
		if fresh := newSandboxes(t, before); len(fresh) > 0 {
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
	if !waitUntil(timeout, func() bool {
		started, done := seedCounts(guest(probe))
		if started == 0 || done < started {
			return false
		}
		return strings.TrimSpace(guest("test -e "+seedDone+" && echo done")) == "done"
	}) {
		t.Fatalf("the seed did not finish within %s:\n%s", timeout, guest(`tail -40 /var/log/sbx-kit-startup.log`))
	}
}

// awaitQuietScreen waits until the pane stops changing, the cue that the TUI takes input.
func awaitQuietScreen(sess *schedule.TmuxSession, timeout time.Duration) {
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
func answerDialogs(t *testing.T, sess *schedule.TmuxSession, dialogs []tuiDialog) {
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
func typeLine(t *testing.T, sess *schedule.TmuxSession, line string) {
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

// SPEC: _spec/internal/devports/dev-ports.puml

const helloApp = ":app " + helloPackage

// TestAndroidAppInstallsFromTheSeed ticks android and the app module once; the seed builds,
// installs and launches it with no typed command, and a --yes rerun on the same path does it again.
func TestAndroidAppInstallsFromTheSeed(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	requireTmux(t)
	if hostdev.EmulatorBinary(os.Getenv) == "" || hostdev.AdbBinary(os.Getenv) == "" {
		t.Skip("no Android SDK emulator/adb on this host")
	}
	port, err := hostdev.ADBPort(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !hostdev.ADBReady(port) {
		t.Cleanup(func() { stopEmulators(t, port) })
	}
	harnessImage(t, "opencode")
	sweepSandboxesAfter(t)
	before, canList := sbxSandboxNames()
	proveoBin := buildProveo(t)
	hostADB := func(args ...string) string {
		out, _ := exec.Command(hostdev.AdbBinary(os.Getenv), append([]string{"-P", strconv.Itoa(port)}, args...)...).CombinedOutput()
		return string(out)
	}
	installed := func() bool {
		return strings.Contains(hostADB("shell", "pm", "list", "packages", helloPackage), "package:"+helloPackage)
	}
	t.Cleanup(func() { hostADB("uninstall", helloPackage) })
	hostADB("uninstall", helloPackage)

	work := testx.Extract(t, "android-hello")
	if err := os.Chmod(filepath.Join(work, "install.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Cleanup(func() { removeLeakedSandboxes(t, before, canList) }) // before home's RemoveAll: a live sandbox still writes its toolchains
	budget := durationEnv(t, "PROVEO_ANDROID_BUILD_TIMEOUT", 25*time.Minute)

	first := seedRun(t, proveoBin, home, work, "first")
	tickAndroid(t, first)
	focusOption(t, first, "Down", helloApp, 8)
	_ = first.SendKeys("Space")
	_ = first.Enter()
	if !waitUntil(budget, installed) {
		screen, _ := first.CaptureAll()
		t.Fatalf("the seed did not install %s on the host emulator:\n%s\n%s", helloPackage, tail(screen, 60), installEvidence(t, before, hostADB))
	}
	var top string
	if !waitUntil(time.Minute, func() bool {
		top = hostADB("shell", "dumpsys", "activity", "activities")
		return strings.Contains(top, helloPackage)
	}) {
		t.Errorf("%s installed but not launched within a minute:\n%s", helloPackage, tail(top, 20))
	}
	if screen, ok := stopRun(first, 3*time.Minute); !ok {
		t.Fatalf("the first run never finished its teardown after SIGTERM\n%s", tail(screen, 40))
	}
	hostADB("uninstall", helloPackage)

	second := seedRun(t, proveoBin, home, work, "second", "--yes", "--addon", "android")
	if !waitUntil(budget, installed) {
		screen, _ := second.CaptureAll()
		t.Fatalf("a --yes rerun did not reinstall %s from the remembered answer:\n%s\n%s", helloPackage, tail(screen, 60), installEvidence(t, before, hostADB))
	}
}

// installEvidence is the seed's install log from each sandbox this test made, and the host's device list.
func installEvidence(t *testing.T, before map[string]bool, hostADB func(...string) string) string {
	t.Helper()
	var b strings.Builder
	for _, name := range newSandboxes(t, before) {
		out, err := exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "cat", sandbox.AndroidLog).CombinedOutput()
		fmt.Fprintf(&b, "── %s:%s (err=%v)\n%s\n", name, sandbox.AndroidLog, err, tail(string(out), 80))
	}
	fmt.Fprintf(&b, "── host adb devices -l\n%s", hostADB("devices", "-l"))
	return b.String()
}

func seedRun(t *testing.T, proveoBin, home, work, tag string, extra ...string) *schedule.TmuxSession {
	t.Helper()
	sess := schedule.NewTmux(fmt.Sprintf("proveo-android-seed-%s-%d", tag, time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)
	cmd := append([]string{"env", "PROVEO_WIZARD=on", "PROVEO_HOME=" + home, "PROVEO_MOUNT_GH_CONFIG=0",
		proveoBin, "run", "opencode", "--input", work}, extra...)
	if err := sess.Start(220, 60, cmd...); err != nil {
		t.Fatalf("start session: %v", err)
	}
	return sess
}

func focusOption(t *testing.T, sess *schedule.TmuxSession, key, opt string, max int) {
	t.Helper()
	for range max {
		if screen, _ := sess.Capture(); strings.Contains(screen, "› "+opt+" —") {
			return
		}
		_ = sess.SendKeys(key)
		time.Sleep(150 * time.Millisecond)
	}
	screen, _ := sess.Capture()
	t.Fatalf("never reached %q with %s:\n%s", opt, key, screen)
}
