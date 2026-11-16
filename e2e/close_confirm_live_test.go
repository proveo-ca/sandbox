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
	"syscall"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/ptyproxy"
	"github.com/proveo-ca/proveo/internal/schedule"
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
			sess := schedule.NewTmux(fmt.Sprintf("proveo-live-close-%d-%d", os.Getpid(), i), nil)
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
	cmd := exec.CommandContext(ctx, "python3", "-u", "-c", `import os, signal, sys, time, tty
tty.setraw(sys.stdin.fileno())
if os.environ.get("PROVEO_LIVE_CLOSE_STOP") == "slow":
    signal.signal(signal.SIGTERM, lambda *_: (time.sleep(3), os._exit(0)))
sys.stdout.write(os.environ.get("PROVEO_LIVE_CLOSE_MODES", ""))
print("CHILD_READY", flush=True)
while True:
    b = sys.stdin.buffer.read(1)
    if not b: break
    print("KEY:%02x" % b[0], flush=True)
`)
	p := ptyproxy.New(os.Stdin, os.Stdout)
	p.DisableFilter = os.Getenv("PROVEO_LIVE_CLOSE_FILTER") == "off"
	var removed atomic.Bool
	stop := func() { removed.Store(true); cancel() }
	if os.Getenv("PROVEO_LIVE_CLOSE_STOP") == "slow" {
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 20 * time.Second
	}
	p.HandleCtrlC(func(in io.Reader, _ io.Writer) ptyproxy.CtrlCAction {
		closed, _ := p.OverlayProgram(in, choiceui.NewSandboxClose(stop, 30*time.Second))
		if choiceui.SandboxCloseAnswer(closed) == choiceui.SandboxClosePass {
			return ptyproxy.CtrlCPass
		}
		return ptyproxy.CtrlCStay
	}, nil)
	_ = p.Run(cmd)
	result := "removed"
	if !removed.Load() || t.Failed() {
		result = "confirmation failed"
	}
	if err := os.WriteFile(done, []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
}

// screenFlags reads the pane's alternate-screen and cursor flags, and its
// bracketed-paste flag where this tmux reports one.
func screenFlags(t *testing.T, sess *schedule.TmuxSession) string {
	t.Helper()
	out, err := exec.Command("tmux", "display-message", "-p", "-t", sess.Name,
		"alt=#{alternate_on} cursor=#{cursor_flag} paste=#{bracket_paste_flag}").Output()
	if err != nil {
		t.Fatalf("read the pane's flags: %v", err)
	}
	return strings.TrimSuffix(strings.TrimSpace(string(out)), " paste=")
}

// TestLiveCtrlCDialogGivesTheChildItsScreenModesBack opens and closes the dialog over a child
// that set its own screen modes, and compares the terminal's flags before and after.
func TestLiveCtrlCDialogGivesTheChildItsScreenModesBack(t *testing.T) {
	requireTmux(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ name, modes, want string }{
		{"a child on the main screen", "", "alt=0 cursor=1 paste=0"},
		{"a child on the alternate screen", "\x1b[?1049h\x1b[?25l\x1b[?2004h", "alt=1 cursor=0 paste=1"},
		{"a child that only hid the cursor", "\x1b[?25l", "alt=0 cursor=0 paste=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := filepath.Join(t.TempDir(), "done")
			sess := schedule.NewTmux(fmt.Sprintf("proveo-live-modes-%d-%d", os.Getpid(), i), nil)
			t.Cleanup(sess.Kill)
			if err := sess.Start(120, 40, "env", "TERM=xterm-256color", "PROVEO_LIVE_CLOSE_HELPER="+done,
				"PROVEO_LIVE_CLOSE_FILTER=on", "PROVEO_LIVE_CLOSE_MODES="+tc.modes,
				binary, "-test.run=^TestLiveCtrlCConfirmationHelper$", "-test.timeout=45s"); err != nil {
				t.Fatal(err)
			}
			if screen, err := sess.WaitFor("CHILD_READY", 10*time.Second); err != nil {
				t.Fatalf("child readiness: %v\n%s", err, screen)
			}
			before := screenFlags(t, sess)
			if !strings.HasPrefix(tc.want, before) {
				t.Fatalf("the child's own flags = %q, want %q", before, tc.want)
			}
			if err := sess.SendKeys("C-c"); err != nil {
				t.Fatal(err)
			}
			if screen, err := sess.WaitFor("close this sandbox?", 5*time.Second); err != nil {
				t.Fatalf("confirmation missing: %v\n%s", err, screen)
			}
			if err := sess.SendKeys("n", "Enter"); err != nil {
				t.Fatal(err)
			}
			if err := sess.SendText("A"); err != nil {
				t.Fatal(err)
			}
			if screen, err := sess.WaitFor("KEY:41", 5*time.Second); err != nil {
				t.Fatalf("session did not resume: %v\n%s", err, screen)
			}
			if after := screenFlags(t, sess); after != before {
				t.Errorf("flags after the dialog = %q, before it = %q", after, before)
			}
		})
	}
}

// TestLiveCloseDialogStaysUpWhileTheAgentStops accepts Remove over a child that takes three
// seconds to stop, and reads the dialog's line while it waits.
func TestLiveCloseDialogStaysUpWhileTheAgentStops(t *testing.T) {
	requireTmux(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	done := filepath.Join(t.TempDir(), "done")
	sess := schedule.NewTmux(fmt.Sprintf("proveo-live-stopping-%d", os.Getpid()), nil)
	t.Cleanup(sess.Kill)
	if err := sess.Start(120, 40, "env", "TERM=xterm-256color", "PROVEO_LIVE_CLOSE_HELPER="+done,
		"PROVEO_LIVE_CLOSE_FILTER=on", "PROVEO_LIVE_CLOSE_STOP=slow",
		binary, "-test.run=^TestLiveCtrlCConfirmationHelper$", "-test.timeout=45s"); err != nil {
		t.Fatal(err)
	}
	if screen, err := sess.WaitFor("CHILD_READY", 10*time.Second); err != nil {
		t.Fatalf("child readiness: %v\n%s", err, screen)
	}
	if err := sess.SendKeys("C-c"); err != nil {
		t.Fatal(err)
	}
	if screen, err := sess.WaitFor("close this sandbox?", 5*time.Second); err != nil {
		t.Fatalf("confirmation missing: %v\n%s", err, screen)
	}
	if err := sess.SendKeys("Enter"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SendText("A"); err != nil {
		t.Fatal(err)
	}
	if screen, err := sess.WaitFor("KEY:41", 5*time.Second); err != nil {
		t.Fatalf("Enter alone did not hand the terminal back: %v\n%s", err, screen)
	}
	if _, err := os.Stat(done); err == nil {
		t.Fatal("Enter alone removed the sandbox")
	}

	if err := sess.SendKeys("C-c"); err != nil {
		t.Fatal(err)
	}
	if screen, err := sess.WaitFor("close this sandbox?", 5*time.Second); err != nil {
		t.Fatalf("confirmation did not reopen: %v\n%s", err, screen)
	}
	if err := sess.SendKeys("r", "Enter"); err != nil {
		t.Fatal(err)
	}
	screen, err := sess.WaitFor("stopping the agent (up to 30s)", 5*time.Second)
	if err != nil {
		t.Fatalf("the dialog did not say a stop is in flight: %v\n%s", err, screen)
	}
	if strings.Contains(screen, "enter press") {
		t.Errorf("the waiting dialog still offers its buttons\n%s", screen)
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(done); err == nil {
			if string(b) != "removed" {
				t.Fatalf("helper: %s", b)
			}
			return
		}
	}
	screen, _ = sess.Capture()
	t.Fatalf("the removal did not finish: %s", screen)
}

// TestCtrlCDialogOverARealAgentGivesItsScreenBackAndRemovesOnYes opens the close dialog over
// a real agent in a real sandbox: n restores the agent's terminal modes, y removes the sandbox.
func TestCtrlCDialogOverARealAgentGivesItsScreenBackAndRemovesOnYes(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	const target = "claudecode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)

	work := t.TempDir()
	writeFile(t, filepath.Join(work, "TRACKED.md"), []byte("# tracked\n"))
	gitInit(t, work)

	before, canList := sbxSandboxNames()
	sess := schedule.NewTmux(fmt.Sprintf("proveo-closesbx-%d", os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
	})
	cmd := []string{"env"}
	if secrets := harnessSecrets(t, target); len(secrets) > 0 {
		cmd = append(cmd, childEnvArgsFor(t, secrets[0])...)
	} else {
		cmd = append(cmd, childEnvArgs(t)...)
	}
	cmd = append(cmd,
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		proveoBin, "run", target, "--clone", "--input", work,
	)
	if err := sess.Start(160, 45, cmd...); err != nil {
		t.Fatalf("start sandbox session: %v", err)
	}
	k := awaitKit(t, sess, before, target, work)
	awaitQuietScreen(sess, 30*time.Second)

	flags := screenFlags(t, sess)
	t.Logf("the agent's terminal flags: %s", flags)
	open := func() {
		t.Helper()
		if err := sess.SendKeys("C-c"); err != nil {
			t.Fatal(err)
		}
		if screen, err := sess.WaitFor("close this sandbox?", 15*time.Second); err != nil {
			t.Fatalf("Ctrl+C over the agent opened no dialog: %v\n%s", err, tail(screen, 40))
		}
	}
	open()
	if err := sess.SendKeys("n", "Enter"); err != nil {
		t.Fatal(err)
	}
	if !waitUntil(15*time.Second, func() bool {
		screen, _ := sess.Capture()
		return !strings.Contains(screen, "close this sandbox?")
	}) {
		screen, _ := sess.Capture()
		t.Fatalf("the dialog stayed up after n\n%s", tail(screen, 40))
	}
	awaitQuietScreen(sess, 15*time.Second)
	if after := screenFlags(t, sess); after != flags {
		t.Errorf("the agent's terminal flags after the dialog = %q, before it = %q", after, flags)
	}
	if now, ok := sbxSandboxNames(); ok && !now[k.name] {
		t.Fatalf("answering n removed sandbox %s", k.name)
	}

	open()
	if err := sess.SendKeys("y", "Enter"); err != nil {
		t.Fatal(err)
	}
	screen, exited := waitSessionExit(sess, durationEnv(t, "PROVEO_TEST_TEARDOWN", 3*time.Minute))
	if !exited {
		t.Fatalf("proveo did not end after y\n%s", tail(screen, 40))
	}
	if now, ok := sbxSandboxNames(); ok && now[k.name] {
		t.Errorf("answering y left sandbox %s in place\n%s", k.name, tail(screen, 40))
	}
}
