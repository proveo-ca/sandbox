//go:build e2e && !windows

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/ptyproxy"
	"github.com/proveo-ca/proveo/internal/tmux"
)

func TestLiveCtrlCConfirmationOnTmux(t *testing.T) {
	requireTmux(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ name, key, filter string }{
		{"raw", "", "on"},
		{"CSI-u_without_filter", "\x1b[99;5u", "off"},
		{"modifyOtherKeys", "\x1b[27;5;99~", "on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := filepath.Join(t.TempDir(), "done")
			sess := tmux.New(fmt.Sprintf("proveo-live-close-%d-%d", os.Getpid(), i), nil)
			t.Cleanup(sess.Kill)
			if err := sess.Start(120, 40, "env", "TERM=xterm-256color", "PROVEO_LIVE_CLOSE_HELPER="+done, "PROVEO_LIVE_CLOSE_FILTER="+tc.filter, binary, "-test.run=^TestLiveCtrlCConfirmationHelper$", "-test.timeout=45s"); err != nil {
				t.Fatal(err)
			}
			if screen, err := sess.WaitFor("CHILD_READY", 10*time.Second); err != nil {
				t.Fatalf("child readiness: %v\n%s", err, screen)
			}
			interrupt := func() {
				t.Helper()
				var err error
				if tc.key == "" {
					err = sess.SendKeys("C-c")
				} else {
					err = sess.SendText(tc.key)
				}
				if err != nil {
					t.Fatal(err)
				}
				if screen, err := sess.WaitFor("close this sandbox?", 5*time.Second); err != nil {
					t.Fatalf("confirmation missing: %v\n%s", err, screen)
				}
			}
			interrupt()
			if err := sess.SendKeys("n", "Enter"); err != nil {
				t.Fatal(err)
			}
			if err := sess.SendText("A"); err != nil {
				t.Fatal(err)
			}
			if screen, err := sess.WaitFor("KEY:41", 5*time.Second); err != nil {
				t.Fatalf("session did not resume: %v\n%s", err, screen)
			} else if strings.Contains(screen, "KEY:03") || strings.Contains(screen, "KEY:1b") {
				t.Fatalf("interrupt reached child: %s", screen)
			}
			interrupt()
			if err := sess.SendKeys("y", "Enter"); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if b, err := os.ReadFile(done); err == nil {
					if string(b) != "removed" {
						t.Fatalf("helper: %s", b)
					}
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			screen, _ := sess.Capture()
			t.Fatalf("confirmed removal did not finish: %s", screen)
		})
	}
}

func TestLiveCtrlCConfirmationHelper(t *testing.T) {
	done := os.Getenv("PROVEO_LIVE_CLOSE_HELPER")
	if done == "" {
		t.Skip("tmux helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-u", "-c", `import sys, tty
tty.setraw(sys.stdin.fileno())
print("CHILD_READY", flush=True)
while True:
    b = sys.stdin.buffer.read(1)
    if not b: break
    print("KEY:%02x" % b[0], flush=True)
`)
	p := ptyproxy.New(os.Stdin, os.Stdout)
	p.DisableFilter = os.Getenv("PROVEO_LIVE_CLOSE_FILTER") == "off"
	var removed atomic.Bool
	p.HandleCtrlC(func(in io.Reader, _ io.Writer) ptyproxy.CtrlCAction {
		screen, err := p.OverlayScreen(in)
		if err != nil {
			t.Error(err)
			return ptyproxy.CtrlCStay
		}
		if err := screen.Init(); err != nil {
			t.Error(err)
			return ptyproxy.CtrlCStay
		}
		defer screen.Fini()
		switch choiceui.ConfirmSandboxClose(screen) {
		case choiceui.SandboxCloseRemove:
			return ptyproxy.CtrlCRemove
		case choiceui.SandboxClosePass:
			return ptyproxy.CtrlCPass
		default:
			return ptyproxy.CtrlCStay
		}
	}, func() { removed.Store(true); cancel() })
	_ = p.Run(cmd)
	result := "removed"
	if !removed.Load() || t.Failed() {
		result = "confirmation failed"
	}
	if err := os.WriteFile(done, []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
}
