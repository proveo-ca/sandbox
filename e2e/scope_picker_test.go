//go:build e2e

// SPEC: _spec/internal/run/run-spec.puml, _spec/internal/choiceui/scope-tree.puml

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

// monorepoRun starts `proveo run claudecode` in a two-project monorepo on a tmux terminal.
func monorepoRun(t *testing.T, name string) *tmux.Session {
	t.Helper()
	const target = "claudecode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)

	work := t.TempDir()
	writeFile(t, filepath.Join(work, "package.json"), []byte(`{"name":"mono","workspaces":["apps/*"]}`))
	for _, app := range []string{"api", "web"} {
		mkdirAll(t, filepath.Join(work, "apps", app))
		writeFile(t, filepath.Join(work, "apps", app, "package.json"), []byte(`{"name":"`+app+`"}`))
	}
	gitInit(t, work)

	before, canList := sbxSandboxNames()
	sess := tmux.New(fmt.Sprintf("proveo-%s-%d", name, os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
		if made := newSandboxes(t, before); canList && len(made) > 0 {
			t.Errorf("a cancelled run created %v", made)
		}
	})
	cmd := append([]string{"env"}, childEnvArgsNoCredential(t)...)
	cmd = append(cmd,
		"PROVEO_WIZARD=on",
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_STATE_ROOT="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		"sh", "-c", proveoBin+" run "+target+" --input "+work+"; echo PROVEO_EXIT=$?; sleep 300",
	)
	if err := sess.Start(160, 45, cmd...); err != nil {
		t.Fatalf("start the session: %v", err)
	}
	return sess
}

// awaitScopePage waits for the scope page and returns the screen.
func awaitScopePage(t *testing.T, sess *tmux.Session) string {
	t.Helper()
	screen, err := sess.WaitFor("enter choose", durationEnv(t, "PROVEO_TEST_TIMEOUT", 2*time.Minute))
	if err != nil {
		t.Fatalf("the scope page never opened: %v\n%s", err, tail(screen, 45))
	}
	return screen
}

// TestEscInTheScopePageCancelsTheRun closes the monorepo scope page on a real terminal.
func TestEscInTheScopePageCancelsTheRun(t *testing.T) {
	sess := monorepoRun(t, "scope-esc")
	screen := awaitScopePage(t, sess)
	for _, want := range []string{"where the agent works", "repo root  default", "apps", "web",
		"scope · 1/2", "enter starts the agent at the repo root"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the scope page shows no %q\n%s", want, tail(screen, 45))
		}
	}
	if strings.Contains(screen, "enter accept") {
		t.Errorf("the scope page says \"enter accept\"; the helpers that wait for the form would press Enter here\n%s", tail(screen, 45))
	}
	if err := sess.SendKeys("Escape"); err != nil {
		t.Fatal(err)
	}
	screen, err := sess.WaitFor("PROVEO_EXIT=", 30*time.Second)
	if err != nil {
		t.Fatalf("proveo kept running after Esc closed the scope page: %v\n%s", err, tail(screen, 45))
	}
	if !strings.Contains(screen, "cancelled at the scope prompt") || !strings.Contains(screen, "PROVEO_EXIT=1") {
		t.Errorf("Esc did not cancel the run with status 1\n%s", tail(screen, 45))
	}
}

// TestTheFormStepsBackToTheScopePageWhereItWasLeft walks page 1, page 2, and back.
func TestTheFormStepsBackToTheScopePageWhereItWasLeft(t *testing.T) {
	sess := monorepoRun(t, "scope-back")
	awaitScopePage(t, sess)
	for _, k := range []string{"Down", "Down", "Down"} {
		if err := sess.SendKeys(k); err != nil {
			t.Fatal(err)
		}
	}
	screen, err := sess.WaitFor("enter starts the agent in apps/web", 15*time.Second)
	if err != nil {
		t.Fatalf("three rows down is not apps/web: %v\n%s", err, tail(screen, 45))
	}
	if err := sess.SendKeys("Enter"); err != nil {
		t.Fatal(err)
	}
	screen, err = sess.WaitFor("enter accept", durationEnv(t, "PROVEO_TEST_TIMEOUT", 2*time.Minute))
	if err != nil {
		t.Fatalf("the form never followed the scope page: %v\n%s", err, tail(screen, 45))
	}
	for _, want := range []string{"page 2/2", "esc back", "ctrl+c quit", "confirm or change this run", "enter accept · 3 steps"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the form lacks %q\n%s", want, tail(screen, 45))
		}
	}
	if err := sess.SendKeys("Escape"); err != nil {
		t.Fatal(err)
	}
	screen, err = sess.WaitFor("enter starts the agent in apps/web", 30*time.Second)
	if err != nil {
		t.Fatalf("a step back did not return to the scope page on apps/web: %v\n%s", err, tail(screen, 45))
	}
	if strings.Contains(screen, "enter accept") {
		t.Errorf("the form stayed on screen after the step back\n%s", tail(screen, 45))
	}
	for _, k := range []string{"Home", "Enter"} {
		if err := sess.SendKeys(k); err != nil {
			t.Fatal(err)
		}
	}
	if screen, err = sess.WaitFor("enter accept", time.Minute); err != nil {
		t.Fatalf("the form did not reopen for the repo root: %v\n%s", err, tail(screen, 45))
	}
	if err := sess.SendKeys("C-c"); err != nil {
		t.Fatal(err)
	}
	if screen, err = sess.WaitFor("Leave proveo run?", 15*time.Second); err != nil {
		t.Fatalf("Ctrl+C on the form did not ask first: %v\n%s", err, tail(screen, 45))
	}
	if !strings.Contains(screen, "No sandbox has been created or resumed.") || strings.Contains(screen, "leave the wizard?") {
		t.Errorf("the run's quit question must say what quitting leaves behind:\n%s", tail(screen, 45))
	}
	if err := sess.SendKeys("C-c"); err != nil {
		t.Fatal(err)
	}
	screen, err = sess.WaitFor("PROVEO_EXIT=", 30*time.Second)
	if err != nil {
		t.Fatalf("a second Ctrl+C did not end the run: %v\n%s", err, tail(screen, 45))
	}
	if !strings.Contains(screen, "quit at the choice prompt") || strings.Contains(screen, "scope: ") {
		t.Errorf("the run must end at the form, and announce no scope\n%s", tail(screen, 45))
	}
}
