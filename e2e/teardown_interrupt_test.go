//go:build e2e && !windows

// SPEC: _spec/internal/backend/teardown.puml

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// paneExit reports whether sess's pane process ended and, if so, its exit status.
func paneExit(sess *tmux.Session) (dead bool, status int) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", sess.Name, "#{pane_dead} #{pane_dead_status}").Output()
	f := strings.Fields(string(out))
	if err != nil || len(f) == 0 || f[0] != "1" {
		return false, 0
	}
	if len(f) > 1 {
		status, _ = strconv.Atoi(f[1])
	}
	return true, status
}

// slowRemovalSbx writes an sbx that waits before a removal and otherwise runs the real one; mark receives its pid.
func slowRemovalSbx(t *testing.T) (dir, mark string) {
	t.Helper()
	real, err := exec.LookPath(sbx.Binary)
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	mark = filepath.Join(dir, "rm.pid")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = rm ]; then echo $$ > '" + mark + "'; sleep 90; fi\n" +
		"exec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, sbx.Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, mark
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// TestCtrlCInTeardownSaysOnceThenLeavesWith130 presses Ctrl+C on a real terminal while a real
// teardown waits on its removal.
func TestCtrlCInTeardownSaysOnceThenLeavesWith130(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	const target = "claudecode"
	requireHarness(t, target)
	proveoBin := buildProveo(t)
	shim, mark := slowRemovalSbx(t)

	work := t.TempDir()
	writeFile(t, filepath.Join(work, "TRACKED.md"), []byte("# tracked\n"))
	gitInit(t, work)

	before, canList := sbxSandboxNames()
	sess := tmux.New(fmt.Sprintf("proveo-tdint-%d", os.Getpid()), nil)
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
	state := t.TempDir()
	cmd = append(cmd,
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_STATE_ROOT="+state,
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		"PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"),
		proveoBin, "run", target, "--clone", "--input", work,
	)
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start sandbox session: %v", err)
	}
	if out, err := exec.Command("tmux", "set-option", "-t", sess.Name, "remain-on-exit", "on").CombinedOutput(); err != nil {
		t.Fatalf("keep the pane after exit: %v\n%s", err, out)
	}
	k := awaitKit(t, sess, before, target, work)

	pane, _ := sess.PanePID()
	pid, err := strconv.Atoi(strings.TrimSpace(pane))
	if err != nil {
		t.Fatalf("pane pid %q: %v", pane, err)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("stop the run: %v", err)
	}
	teardown := durationEnv(t, "PROVEO_TEST_TEARDOWN", 3*time.Minute)
	screen := ""
	if !waitUntil(teardown, func() bool {
		screen, _ = sess.CaptureAll()
		_, err := os.Stat(mark)
		return err == nil && strings.Contains(screen, "· remove sandbox · ")
	}) {
		t.Fatalf("teardown never reached the removal\n%s", tail(screen, 40))
	}
	b, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	removal, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("removal pid %q: %v", b, err)
	}

	if err := sess.SendKeys("C-c"); err != nil {
		t.Fatal(err)
	}
	if !waitUntil(20*time.Second, func() bool {
		screen, _ = sess.CaptureAll()
		return strings.Contains(screen, "Ctrl+C again leaves now")
	}) {
		t.Fatalf("the first Ctrl+C said nothing\n%s", tail(screen, 40))
	}
	time.Sleep(time.Second)
	if dead, status := paneExit(sess); dead {
		t.Fatalf("proveo exited %d on the first Ctrl+C\n%s", status, tail(screen, 40))
	}
	if !alive(removal) {
		t.Fatalf("the first Ctrl+C killed the removal in flight\n%s", tail(screen, 40))
	}

	if err := sess.SendKeys("C-c"); err != nil {
		t.Fatal(err)
	}
	status := -1
	if !waitUntil(30*time.Second, func() bool {
		dead, s := paneExit(sess)
		status = s
		return dead
	}) {
		screen, _ = sess.CaptureAll()
		t.Fatalf("proveo did not leave on the second Ctrl+C\n%s", tail(screen, 40))
	}
	screen, _ = sess.CaptureAll()
	if status != 130 {
		t.Errorf("exit status = %d, want 130\n%s", status, tail(screen, 40))
	}
	for _, want := range []string{"kept (teardown stopped by SIGINT at remove sandbox)", "proveo rm " + k.name} {
		if !strings.Contains(screen, want) {
			t.Errorf("the leave lines miss %q\n%s", want, tail(screen, 40))
		}
	}
	if now, ok := sbxSandboxNames(); ok && !now[k.name] {
		t.Errorf("sandbox %s is gone; leaving teardown must keep it", k.name)
	}
	if !waitUntil(10*time.Second, func() bool { return !alive(removal) }) {
		t.Errorf("the removal (pid %d) outlived proveo", removal)
	}

	var kept *results.Entry
	for _, e := range results.List(results.Dir(state)) {
		if e.Kind == results.Kept && e.Key == k.name {
			kept = &e
		}
	}
	if kept == nil || kept.Pending || kept.Exit == nil || *kept.Exit != 130 || !strings.Contains(kept.Why, "stopped by SIGINT at remove sandbox") {
		t.Errorf("the store's entry for the kept sandbox = %+v, want a settled one with exit 130", kept)
	}
	prompt := exec.Command(proveoBin, "prompt")
	prompt.Dir = work
	prompt.Env = append(os.Environ(), "PROVEO_STATE_ROOT="+state)
	if out, err := prompt.CombinedOutput(); err != nil || !strings.HasPrefix(string(out), "1 kept") {
		t.Errorf("proveo prompt in the run's repository = %q, %v; want it to count the kept sandbox", out, err)
	}

	proveo := func(args ...string) (string, error) {
		cmd := exec.Command(proveoBin, args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "PROVEO_STATE_ROOT="+state, "PROVEO_HOME="+t.TempDir())
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	running := false
	if rows, ok := sbx.List(); ok {
		for _, r := range rows {
			running = running || (r.Name == k.name && r.Running())
		}
	}
	if running {
		if out, err := proveo("rm", k.name); err == nil || !strings.Contains(out, "is running") {
			t.Errorf("proveo rm of a running sandbox = %v, want a refusal\n%s", err, out)
		}
		if out, err := exec.Command(sbx.Binary, "stop", k.name).CombinedOutput(); err != nil {
			t.Fatalf("sbx stop %s: %v\n%s", k.name, err, out)
		}
	}
	out, err := proveo("rm", k.name)
	if err != nil || !strings.Contains(out, k.name+" is removed") {
		t.Fatalf("proveo rm = %v\n%s", err, out)
	}
	if now, ok := sbxSandboxNames(); ok && now[k.name] {
		t.Errorf("proveo rm left sandbox %s in place", k.name)
	}
	for _, e := range results.List(results.Dir(state)) {
		if e.Kind == results.Kept {
			t.Errorf("the store still holds a kept entry after proveo rm: %+v", e)
		}
	}
	if out, _ := proveo("prompt"); strings.Contains(out, "kept") {
		t.Errorf("proveo prompt after the removal = %q, want no kept sandbox", out)
	}
}
