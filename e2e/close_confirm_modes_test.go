//go:build e2e && !windows

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

	"github.com/proveo-ca/proveo/internal/tmux"
)

// screenFlags reads the pane's alternate-screen and cursor flags, and its
// bracketed-paste flag where this tmux reports one.
func screenFlags(t *testing.T, sess *tmux.Session) string {
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
			sess := tmux.New(fmt.Sprintf("proveo-live-modes-%d-%d", os.Getpid(), i), nil)
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
