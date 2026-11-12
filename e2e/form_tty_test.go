//go:build e2e && !windows

// SPEC: _spec/internal/choiceui/tui-stack.puml

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestTheRunFormDrawsOnTheTerminalWhenStdoutIsRedirected runs `proveo run … >/dev/null`
// on a real terminal: the form must still appear, take keys, and cancel on Esc.
func TestTheRunFormDrawsOnTheTerminalWhenStdoutIsRedirected(t *testing.T) {
	const target = "claudecode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)

	work := t.TempDir()
	writeFile(t, filepath.Join(work, "TRACKED.md"), []byte("# tracked\n"))
	gitInit(t, work)

	before, canList := sbxSandboxNames()
	sess := tmux.New(fmt.Sprintf("proveo-form-tty-%d", os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
	})
	cmd := append([]string{"env"}, childEnvArgsNoCredential(t)...)
	cmd = append(cmd,
		"PROVEO_WIZARD=on",
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_STATE_ROOT="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		"sh", "-c", proveoBin+" run "+target+" --input "+work+" > /dev/null; echo PROVEO_EXIT=$?; sleep 300",
	)
	if err := sess.Start(160, 45, cmd...); err != nil {
		t.Fatalf("start the session: %v", err)
	}
	screen, err := sess.WaitFor("enter accept", durationEnv(t, "PROVEO_TEST_TIMEOUT", 2*time.Minute))
	if err != nil {
		t.Fatalf("the form never drew with stdout redirected: %v\n%s", err, tail(screen, 45))
	}
	for _, want := range []string{"confirm or change this run", "esc cancel"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the form lacks %q\n%s", want, tail(screen, 45))
		}
	}
	if err := sess.SendKeys("Down"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SendKeys("Escape"); err != nil {
		t.Fatal(err)
	}
	screen, err = sess.WaitFor("PROVEO_EXIT=", 30*time.Second)
	if err != nil {
		t.Fatalf("Esc did not end the form: %v\n%s", err, tail(screen, 45))
	}
	if strings.Contains(screen, "enter accept") {
		t.Errorf("the form stayed on the terminal after it ended\n%s", tail(screen, 45))
	}
	if made := newSandboxes(t, before); canList && len(made) > 0 {
		t.Errorf("a cancelled run created %v", made)
	}
}
