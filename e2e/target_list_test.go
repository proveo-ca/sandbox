//go:build e2e && !windows

// SPEC: _spec/internal/choiceui/scope-tree.puml

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestABareRunListsItsTargetsNarrowsOnTypingAndCancelsOnEsc drives the list a
// bare `proveo run` opens, on a real terminal. It starts no sandbox.
func TestABareRunListsItsTargetsNarrowsOnTypingAndCancelsOnEsc(t *testing.T) {
	proveoBin := buildProveo(t)
	sess := tmux.New(fmt.Sprintf("proveo-target-list-%d", os.Getpid()), nil)
	t.Cleanup(sess.Kill)
	cmd := append([]string{"env"}, childEnvArgsNoCredential(t)...)
	cmd = append(cmd,
		"PROVEO_WIZARD=on",
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_STATE_ROOT="+t.TempDir(),
		"sh", "-c", proveoBin+" run; echo PROVEO_EXIT=$?; sleep 300",
	)
	if err := sess.Start(140, 40, cmd...); err != nil {
		t.Fatalf("start the session: %v", err)
	}
	screen, err := sess.WaitFor("enter choose", durationEnv(t, "PROVEO_TEST_TIMEOUT", time.Minute))
	if err != nil {
		t.Fatalf("the target list never opened: %v\n%s", err, tail(screen, 40))
	}
	for _, want := range []string{"run> ", "claudecode", "opencode", "type to narrow", "esc cancel"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the list lacks %q\n%s", want, tail(screen, 40))
		}
	}
	if err := sess.SendText("opencode"); err != nil {
		t.Fatal(err)
	}
	screen, err = sess.WaitFor("run> opencode", 15*time.Second)
	if err != nil {
		t.Fatalf("typing did not reach the list: %v\n%s", err, tail(screen, 40))
	}
	if strings.Contains(screen, "claudecode") {
		t.Errorf("typing did not narrow the list\n%s", tail(screen, 40))
	}
	if err := sess.SendKeys("Escape"); err != nil {
		t.Fatal(err)
	}
	screen, err = sess.WaitFor("PROVEO_EXIT=", 30*time.Second)
	if err != nil {
		t.Fatalf("Esc did not close the list: %v\n%s", err, tail(screen, 40))
	}
	if !strings.Contains(screen, "no target selected") || !strings.Contains(screen, "PROVEO_EXIT=1") {
		t.Errorf("a closed list must end the run with status 1\n%s", tail(screen, 40))
	}
}
