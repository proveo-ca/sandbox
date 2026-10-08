//go:build e2e

// SPEC: _spec/internal/devports/dev-ports.puml

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/tmux"
)

const helloApp = ":app " + helloPackage

// TestAndroidAppInstallsFromTheSeed ticks android and the app module once; the seed builds,
// installs and launches it with no typed command, and a --yes rerun on the same path does it again.
func TestAndroidAppInstallsFromTheSeed(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	requireTmux(t)
	if hostadb.EmulatorBinary(os.Getenv) == "" || hostadb.AdbBinary(os.Getenv) == "" {
		t.Skip("no Android SDK emulator/adb on this host")
	}
	port, err := hostadb.Port(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !hostadb.Ready(port) {
		t.Cleanup(func() { stopEmulators(t, port) })
	}
	harnessImage(t, "opencode")
	sweepSandboxesAfter(t)
	proveoBin := buildProveo(t)
	hostADB := func(args ...string) string {
		out, _ := exec.Command(hostadb.AdbBinary(os.Getenv), append([]string{"-P", strconv.Itoa(port)}, args...)...).CombinedOutput()
		return string(out)
	}
	installed := func() bool {
		return strings.Contains(hostADB("shell", "pm", "list", "packages", helloPackage), "package:"+helloPackage)
	}
	t.Cleanup(func() { hostADB("uninstall", helloPackage) })
	hostADB("uninstall", helloPackage)

	work := t.TempDir()
	if out, err := exec.Command("cp", "-R", filepath.Join(repoRoot(t), "e2e", "testdata", "android-hello")+"/.", work).CombinedOutput(); err != nil {
		t.Fatalf("copy fixture: %v\n%s", err, out)
	}
	home := t.TempDir()
	budget := durationEnv(t, "PROVEO_ANDROID_BUILD_TIMEOUT", 25*time.Minute)

	first := seedRun(t, proveoBin, home, work, "first")
	tickAndroid(t, first)
	focusOption(t, first, "Up", helloApp, 8)
	_ = first.SendKeys("Space")
	_ = first.Enter()
	if !waitUntil(budget, installed) {
		screen, _ := first.CaptureAll()
		t.Fatalf("the seed did not install %s on the host emulator:\n%s", helloPackage, tail(screen, 60))
	}
	if top := hostADB("shell", "dumpsys", "activity", "activities"); !strings.Contains(top, helloPackage) {
		t.Errorf("%s installed but not launched:\n%s", helloPackage, tail(top, 20))
	}
	first.Kill()
	hostADB("uninstall", helloPackage)

	second := seedRun(t, proveoBin, home, work, "second", "--yes", "--addon", "android")
	if !waitUntil(budget, installed) {
		screen, _ := second.CaptureAll()
		t.Fatalf("a --yes rerun did not reinstall %s from the remembered answer:\n%s", helloPackage, tail(screen, 60))
	}
}

func seedRun(t *testing.T, proveoBin, home, work, tag string, extra ...string) *tmux.Session {
	t.Helper()
	sess := tmux.New(fmt.Sprintf("proveo-android-seed-%s-%d", tag, time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)
	cmd := append([]string{"env", "PROVEO_WIZARD=on", "PROVEO_HOME=" + home, "PROVEO_MOUNT_GH_CONFIG=0",
		proveoBin, "run", "opencode", "--egress-mode", "open", "--input", work, "--shell"}, extra...)
	if err := sess.Start(220, 60, cmd...); err != nil {
		t.Fatalf("start session: %v", err)
	}
	return sess
}

func focusOption(t *testing.T, sess *tmux.Session, key, opt string, max int) {
	t.Helper()
	for i := 0; i < max; i++ {
		if screen, _ := sess.Capture(); strings.Contains(screen, "› "+opt+" —") {
			return
		}
		_ = sess.SendKeys(key)
		time.Sleep(150 * time.Millisecond)
	}
	screen, _ := sess.Capture()
	t.Fatalf("never reached %q with %s:\n%s", opt, key, screen)
}
