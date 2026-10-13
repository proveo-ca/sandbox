//go:build e2e

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestHeadedCtrlCAsksBeforeRemoving drives a real proveo run on a tmux TTY and
// asserts Ctrl+C opens the close confirm instead of deleting the session.
func TestHeadedCtrlCAsksBeforeRemoving(t *testing.T) {
	requireTmux(t)
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/dev/kvm"); err != nil {
			t.Skip("sbx backend needs /dev/kvm; without it proveo never opens the headed confirm")
		}
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "sbx"), []byte(fakeSbx), 0o755); err != nil {
		t.Fatal(err)
	}
	proveoBin := buildProveo(t)
	home := t.TempDir()
	work := t.TempDir()
	const image = "proveo/close-confirm:e2e"

	sess := tmux.New(fmt.Sprintf("proveo-closecfm-%d", os.Getpid()), nil)
	t.Cleanup(sess.Kill)
	if err := sess.Start(120, 40, "env",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"PROVEO_WIZARD=off",
		"PROVEO_HOME="+home,
		"PROVEO_MOUNT_GH_CONFIG=0",
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		proveoBin, "run", "opencode",
		"--yes", "--clone=false", "--shell",
		"--egress-mode", "open", "--credentials", "forward",
		"--image", image, "--input", work,
	); err != nil {
		t.Fatalf("start tmux: %v", err)
	}

	screen, err := sess.WaitFor("SBX_HEADED_READY", 45*time.Second)
	if err != nil {
		t.Fatalf("the headed sbx client never attached:\n%s", screen)
	}
	if strings.Contains(screen, "falling back to docker+egress") {
		t.Fatalf("the run left the sbx backend, so Ctrl+C would not open the confirm:\n%s", screen)
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
		"y  remove the sandbox",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("confirm modal missing %q:\n%s", want, screen)
		}
	}
	if err := sess.SendKeys("n"); err != nil {
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

const fakeSbx = `#!/bin/sh
case "$1" in
  version)
    if [ "$2" = "--json" ]; then
      printf '%s\n' '{"client":{"version":"0.42.0"}}'
    else
      printf '%s\n' 'sbx version: v0.42.0 deadbeef'
    fi
    ;;
  template)
    if [ "$2" = "ls" ]; then
      printf '%s\n' 'REPOSITORY TAG IMAGE ID' 'proveo/close-confirm e2e deadbeef'
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
