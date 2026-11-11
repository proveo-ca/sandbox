//go:build e2e

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestHeadedCtrlCAsksBeforeRemoving drives a real proveo run on a tmux TTY and
// asserts Ctrl+C opens the close confirm instead of deleting the session.
func TestHeadedCtrlCAsksBeforeRemoving(t *testing.T) {
	manifests, err := manifest.Load(filepath.Join(repoRoot(t), "defs"))
	if err != nil {
		t.Fatal(err)
	}
	targets, err := manifest.Targets(manifests)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		t.Fatal("no runnable defs discovered")
	}
	names := make([]string, 0, len(targets))
	for target := range targets {
		names = append(names, target)
	}
	slices.Sort(names)
	for _, target := range names {
		t.Run(target, func(t *testing.T) {
			for _, tc := range []struct{ name, filter, key string }{
				{"raw", "on", "\x03"},
				{"CSI-u_without_filter", "off", "\x1b[99;5u"},
				{"modifyOtherKeys", "on", "\x1b[27;5;99~"},
			} {
				t.Run(tc.name, func(t *testing.T) { headedCtrlCAsksBeforeRemoving(t, target, tc.filter, tc.key) })
			}
		})
	}
}

func headedCtrlCAsksBeforeRemoving(t *testing.T, target, filter, key string) {
	requireTmux(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "sbx"), []byte(fakeSbx(sbx.MinVersion)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if ok, why := sbx.Available(); !ok {
		t.Skipf("this platform cannot host sbx (%s), so proveo never opens the headed confirm", why)
	}
	proveoBin := buildProveo(t)
	home := t.TempDir()
	calls := filepath.Join(t.TempDir(), "sbx-calls")
	t.Setenv("PROVEO_CLOSE_TEST_LOG", calls)
	work := t.TempDir()
	image := "proveo/close-confirm-" + target + ":e2e"
	if out, err := exec.Command("docker", "tag", harnessImage(t, target), image).CombinedOutput(); err != nil {
		t.Fatalf("docker tag %s: %v\n%s", image, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", image).Run() })

	sess := tmux.New(fmt.Sprintf("proveo-closecfm-%d", os.Getpid()), nil)
	t.Cleanup(func() { stopRun(sess, time.Minute) })
	if err := sess.Start(120, 40, "env",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"PROVEO_WIZARD=off",
		"PROVEO_STDIN_FILTER="+filter,
		"PROVEO_CLOSE_TEST_LOG="+calls,
		"PROVEO_HOME="+home,
		"PROVEO_MOUNT_GH_CONFIG=0",
		"PATH="+os.Getenv("PATH"),
		proveoBin, "run", target,
		"--yes", "--clone=false",
		"--credentials", "forward",
		"--image", image, "--input", work,
	); err != nil {
		t.Fatalf("start tmux: %v", err)
	}

	screen, err := sess.WaitFor("SBX_HEADED_READY", 45*time.Second)
	if err != nil {
		t.Fatalf("the headed sbx client never attached:\n%s", screen)
	}
	if err := sess.SendText(key); err != nil {
		t.Fatal(err)
	}
	screen, err = sess.WaitFor("sbx rm --force will run.", 15*time.Second)
	if err != nil {
		t.Fatalf("Ctrl+C did not ask before removing:\n%s", screen)
	}
	for _, want := range []string{
		"That deletes the session.",
		"Uncommitted changes can be permanently lost.",
		"Stay",
		"Remove",
		"Interrupt agent",
		"enter accept",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("confirm modal missing %q:\n%s", want, screen)
		}
	}
	if err := sess.SendKeys("n", "Enter"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SendText("A"); err != nil {
		t.Fatal(err)
	}
	if screen, err := sess.WaitFor("KEY:41", 10*time.Second); err != nil {
		t.Fatalf("n did not restore input: %v\n%s", err, screen)
	}
	if b, _ := os.ReadFile(calls); strings.Contains(string(b), "rm --force") {
		t.Fatal("sandbox removed before confirmation")
	}
	if err := sess.SendText(key); err != nil {
		t.Fatal(err)
	}
	if screen, err := sess.WaitFor("close this sandbox?", 5*time.Second); err != nil {
		t.Fatalf("confirmation did not reopen: %v\n%s", err, screen)
	}
	if err := sess.SendKeys("y", "Enter"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(calls); strings.Contains(string(b), "rm --force") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	screen, _ = sess.Capture()
	t.Fatalf("confirmed removal did not execute sbx rm --force:\n%s", screen)
}

// fakeSbx is an sbx that reports version, lists the test template, and holds a headed run.
func fakeSbx(version string) string {
	return strings.ReplaceAll(fakeSbxScript, "VERSION", version)
}

const fakeSbxScript = `#!/bin/sh
[ -z "${PROVEO_CLOSE_TEST_LOG:-}" ] || printf '%s\n' "$*" >> "$PROVEO_CLOSE_TEST_LOG"
case "$1" in
  version)
    if [ "$2" = "--json" ]; then
      printf '%s\n' '{"client":{"version":"VERSION"}}'
    else
      printf '%s\n' 'sbx version: vVERSION deadbeef'
    fi
    ;;
  run)
    exec python3 -u -c '
import sys, tty
tty.setraw(sys.stdin.fileno())
print("SBX_HEADED_READY", flush=True)
while True:
    b = sys.stdin.buffer.read(1)
    if not b: break
    print("KEY:%02x" % b[0], flush=True)
'
    ;;
esac
exit 0
`
