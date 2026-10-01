//go:build e2e

// SPEC: _spec/internal/sbx/host-android-adb.puml

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/tmux"
)

var deviceLineRE = regexp.MustCompile(`emulator-\d+\s+device\b`)

// TestAndroidTickedInTheChoiceFormOpensTheHostEmulator ticks "android (host
// emulator)" with keystrokes in the real choice form, then proves the run
// booted an emulator on the host and handed the sandbox a working route to it.
func TestAndroidTickedInTheChoiceFormOpensTheHostEmulator(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	const target = "claudecode"
	requireTmux(t)
	harnessImage(t, target)
	sweepSandboxesAfter(t)
	if hostadb.EmulatorBinary(os.Getenv) == "" || hostadb.AdbBinary(os.Getenv) == "" {
		t.Skip("no Android SDK emulator/adb on this host")
	}
	port, err := hostadb.Port(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if hostadb.Ready(port) {
		t.Skipf("a device is already on 127.0.0.1:%d — stop it so the run has to open one", port)
	}
	t.Cleanup(func() { stopEmulators(t, port) })

	proveoBin := buildProveo(t)
	home, work := t.TempDir(), t.TempDir()
	sess := tmux.New(fmt.Sprintf("proveo-android-%d", time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)
	cmd := []string{"env", "PROVEO_WIZARD=on", "PROVEO_HOME=" + home, "PROVEO_MOUNT_GH_CONFIG=0",
		proveoBin, "run", target, "--input", work, "--shell"}
	if err := sess.Start(220, 60, cmd...); err != nil {
		t.Fatalf("start session: %v", err)
	}

	tickAndroid(t, sess)
	if err := sess.Enter(); err != nil {
		t.Fatalf("accept the form: %v", err)
	}

	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 5*time.Minute)
	w := newWatcher(t, sess)
	waitForContainerShell(t, w, timeout)

	log, _ := sess.CaptureAll()
	if !strings.Contains(log, "android: booting AVD") || !strings.Contains(log, "booted") {
		t.Fatalf("the run did not open an emulator on the host:\n%s", log)
	}
	if !strings.Contains(log, "the agent controls every device on that adb server") {
		t.Errorf("the run did not warn about adb's reach:\n%s", log)
	}
	if !hostadb.Ready(port) {
		t.Fatalf("no usable device on the host adb server 127.0.0.1:%d after the run opened one", port)
	}

	probe := `ip="$(getent ahostsv4 host.docker.internal | awk 'NR==1{print $1}')"
echo "PORT=${PROVEO_HOST_ADB_PORT:-unset}"
ADB_SERVER_SOCKET="tcp:${ip}:${PROVEO_HOST_ADB_PORT}" adb devices`
	out, status := shellExec(t, sess, probe, 60*time.Second)
	if status != 0 {
		t.Fatalf("sandbox probe exited %d:\n%s", status, out)
	}
	if !strings.Contains(out, fmt.Sprintf("PORT=%d", port)) {
		t.Errorf("the sandbox did not receive %s=%d:\n%s", hostadb.EnvPort, port, out)
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
	if hostadb.AdbBinary(os.Getenv) == "" {
		t.Skip("no Android SDK adb on this host")
	}
	port, err := hostadb.Port(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if hostadb.Ready(port) {
		t.Skipf("a device is already on 127.0.0.1:%d — the control needs none", port)
	}

	proveoBin := buildProveo(t)
	home, work := t.TempDir(), t.TempDir()
	sess := tmux.New(fmt.Sprintf("proveo-android-off-%d", time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)
	cmd := []string{"env", "PROVEO_WIZARD=on", "PROVEO_HOME=" + home, "PROVEO_MOUNT_GH_CONFIG=0",
		proveoBin, "run", target, "--input", work, "--shell"}
	if err := sess.Start(220, 60, cmd...); err != nil {
		t.Fatalf("start session: %v", err)
	}
	if _, err := sess.WaitFor("[ ] "+hostadb.Addon, 90*time.Second); err != nil {
		screen, _ := sess.Capture()
		t.Fatalf("the form does not offer an unticked android box:\n%s", screen)
	}
	acceptChoicePrompt(t, sess, target)
	waitForContainerShell(t, newWatcher(t, sess), durationEnv(t, "PROVEO_TEST_TIMEOUT", 5*time.Minute))

	log, _ := sess.CaptureAll()
	if strings.Contains(log, "android:") {
		t.Errorf("an unticked android box still reached the host side:\n%s", log)
	}
	if hostadb.Ready(port) {
		t.Errorf("a device appeared on 127.0.0.1:%d although android was never ticked", port)
	}
	out, _ := shellExec(t, sess, `echo "PORT=${PROVEO_HOST_ADB_PORT:-unset}"`, 30*time.Second)
	if !strings.Contains(out, "PORT=unset") {
		t.Errorf("the sandbox received %s without the add-on:\n%s", hostadb.EnvPort, out)
	}
}

// stopEmulators kills every emulator on the server and waits until none is listed.
func stopEmulators(t *testing.T, port int) {
	t.Helper()
	adb := hostadb.AdbBinary(os.Getenv)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		serials, err := hostadb.Devices(port)
		if err != nil || len(serials) == 0 {
			return
		}
		for _, s := range serials {
			_ = exec.Command(adb, "-P", strconv.Itoa(port), "-s", s, "emu", "kill").Run()
		}
		time.Sleep(2 * time.Second)
	}
	t.Errorf("an emulator on 127.0.0.1:%d outlived the test", port)
}

// tickAndroid walks the form the way an operator does: down to the interface
// row, right onto the android box, space to tick it.
func tickAndroid(t *testing.T, sess *tmux.Session) {
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
	step("Down", "tui (this session)", 12)
	step("Right", hostadb.Addon, 6)
	if err := sess.SendKeys("Space"); err != nil {
		t.Fatalf("send Space: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if screen, _ := sess.Capture(); strings.Contains(screen, "[x] "+hostadb.Addon) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	screen, _ := sess.Capture()
	t.Fatalf("the android box is not ticked after Space:\n%s", screen)
}
