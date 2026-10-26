//go:build e2e

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestHeadedCtrlCAsksBeforeRemoving drives a real proveo run on a tmux TTY and
// asserts Ctrl+C opens the close confirm instead of deleting the session.
func TestHeadedCtrlCAsksBeforeRemoving(t *testing.T) {
	requireTmux(t)
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
	work := t.TempDir()
	const image = "proveo/close-confirm:e2e"
	if out, err := exec.Command("docker", "tag", harnessImage(t, "claudecode"), image).CombinedOutput(); err != nil {
		t.Fatalf("docker tag %s: %v\n%s", image, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", image).Run() })

	sess := tmux.New(fmt.Sprintf("proveo-closecfm-%d", os.Getpid()), nil)
	t.Cleanup(func() { stopRun(sess, time.Minute) })
	if err := sess.Start(120, 40, "env",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"PROVEO_WIZARD=off",
		"PROVEO_HOME="+home,
		"PROVEO_MOUNT_GH_CONFIG=0",
		"PATH="+os.Getenv("PATH"),
		proveoBin, "run", "claudecode",
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
	if err := sess.SendKeys("C-c"); err != nil {
		t.Fatal(err)
	}
	screen, err = sess.WaitFor("sbx rm --force will run.", 15*time.Second)
	if err != nil {
		t.Fatalf("Ctrl+C did not ask before removing:\n%s", screen)
	}
	for _, want := range []string{
		"That deletes the session.",
		"Uncommitted changes can be permanently lost.",
		"[y/N]",
		"[esc]",
		"[ctrl+c]",
		"remove the sandbox",
		"enter submits",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("confirm modal missing %q:\n%s", want, screen)
		}
	}
	if err := sess.SendKeys("n", "Enter"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var after string
	for time.Now().Before(deadline) {
		after, _ = sess.Capture()
		if strings.Contains(after, "SBX_HEADED_READY") && !strings.Contains(after, "close this sandbox?") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("n did not return to the session:\n%s", after)
}

// fakeSbx is an sbx that reports version, lists the test template, and holds a headed run.
func fakeSbx(version string) string {
	return strings.ReplaceAll(fakeSbxScript, "VERSION", version)
}

const fakeSbxScript = `#!/bin/sh
case "$1" in
  version)
    if [ "$2" = "--json" ]; then
      printf '%s\n' '{"client":{"version":"VERSION"}}'
    else
      printf '%s\n' 'sbx version: vVERSION deadbeef'
    fi
    ;;
  run)
    printf '%s\n' 'SBX_HEADED_READY'
    trap '' INT TERM
    exec sleep 600
    ;;
esac
exit 0
`
