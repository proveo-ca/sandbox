//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// kitRun is one `proveo run <target>` through its kit under tmux, and the
// sandbox it runs in.
type kitRun struct {
	t    *testing.T
	sess *tmux.Session
	name string
	dir  string
}

// launchKit starts `proveo run <target>` through its kit under tmux, with the
// picker skipped and credentials forwarded, and returns once the sandbox's seed
// has finished. env is set on the proveo process; extra follows the run flags.
func launchKit(t *testing.T, proveoBin, target, dir string, env []string, extra ...string) *kitRun {
	t.Helper()
	requireSbx(t)
	sweepSandboxesAfter(t)
	before, _ := sbxSandboxNames()
	sess := tmux.New(fmt.Sprintf("proveo-kit-%s-%d", target, time.Now().UnixNano()), nil)
	cmd := append([]string{"env", "PROVEO_WIZARD=off", "PROVEO_MOUNT_GH_CONFIG=0"}, env...)
	cmd = append(cmd, proveoBin, "run", target, "--credentials", "forward", "--input", dir)
	if err := sess.Start(220, 50, append(cmd, extra...)...); err != nil {
		t.Fatalf("start proveo run %s: %v", target, err)
	}
	return awaitKit(t, sess, before, target, dir)
}

// awaitKit names the sandbox the `proveo run <target> --input dir` in sess
// created since before, waits for its seed, and ends the run at cleanup.
func awaitKit(t *testing.T, sess *tmux.Session, before map[string]bool, target, dir string) *kitRun {
	t.Helper()
	pid, _ := sess.PanePID()
	t.Cleanup(func() { killAndAwait(t, sess, pid, durationEnv(t, "PROVEO_TEST_TEARDOWN", 3*time.Minute)) })
	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 8*time.Minute)
	k := &kitRun{t: t, sess: sess, dir: dir}
	if !waitUntil(timeout, func() bool { k.tend(); k.name = kitSandbox(before, target, dir); return k.name != "" }) {
		t.Fatalf("no %s sandbox appeared within %s\n%s", target, timeout, k.pane(40))
	}
	seed := durationEnv(t, "PROVEO_TEST_SEED_TIMEOUT", 12*time.Minute)
	if !waitUntil(seed, func() bool {
		k.tend()
		_, rc := k.raw("test -e "+seedDone, time.Minute)
		return rc == 0
	}) {
		log, _ := k.raw("tail -40 /var/log/sbx-kit-startup.log", time.Minute)
		t.Fatalf("the seed in %s did not finish within %s:\n%s\n%s", k.name, seed, log, k.pane(40))
	}
	return k
}

// end stops the run the way `docker stop` or a closed terminal does: SIGTERM
// to proveo, whose own teardown then runs. It returns the last screen and
// whether proveo exited within the teardown budget.
func (k *kitRun) end() (string, bool) {
	k.t.Helper()
	return stopRun(k.sess, durationEnv(k.t, "PROVEO_TEST_TEARDOWN", 3*time.Minute))
}

// stopRun sends SIGTERM to the proveo in sess and returns its last screen and
// whether it exited within timeout.
func stopRun(sess *tmux.Session, timeout time.Duration) (string, bool) {
	pid, _ := sess.PanePID()
	if n, err := strconv.Atoi(strings.TrimSpace(pid)); err == nil && n > 0 {
		if proc, err := os.FindProcess(n); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}
	return waitSessionExit(sess, timeout)
}

// tend answers sbx's own operator questions and fails on a refusal or on a
// confirmation that would swallow input.
func (k *kitRun) tend() {
	k.t.Helper()
	screen, _ := k.sess.CaptureAll()
	if line := sbxError(screen); line != "" && !retryable(line) {
		k.t.Fatalf("sbx refused the run — %s\n%s", line, tail(screen, 40))
	}
	if line := brokenPrompt(screen); line != "" {
		k.t.Fatalf("a confirmation is eating input meant for something else — %s\n%s", line, tail(screen, 40))
	}
	answerSbxPrompt(k.sess, screen)
}

// exec runs script under bash in the sandbox as the agent's user, in the
// agent's working directory with the provisioned toolchain on PATH, and
// returns its combined output and exit status.
func (k *kitRun) exec(script string, timeout time.Duration) (string, int) {
	k.t.Helper()
	prelude := "launch_dir=" + shellQuote([]string{k.dir}) + "\n" +
		`cd "${PROVEO_WORKDIR:-$launch_dir}" 2>/dev/null || cd "$launch_dir"` + "\n" +
		`[ -f "$HOME/.proveo-tool-env.sh" ] && . "$HOME/.proveo-tool-env.sh"` + "\n"
	return k.raw(prelude+script, timeout)
}

// raw runs script under bash in the sandbox from /, with nothing sourced.
func (k *kitRun) raw(script string, timeout time.Duration) (string, int) {
	k.t.Helper()
	return sbxExecStatus(k.t, k.name, script, timeout)
}

// pane is the last n lines proveo and the agent printed.
func (k *kitRun) pane(n int) string {
	screen, _ := k.sess.CaptureAll()
	return tail(screen, n)
}

// kitSandbox names the sandbox target's run created since before: the only new
// one with its name prefix, or the one whose workspaces hold dir.
func kitSandbox(before map[string]bool, target, dir string) string {
	out, err := exec.Command(sbx.Binary, "ls", "--json").Output()
	if err != nil {
		return ""
	}
	var listing struct {
		Sandboxes []struct {
			Name       string   `json:"name"`
			Workspaces []string `json:"workspaces"`
		} `json:"sandboxes"`
	}
	if json.Unmarshal(out, &listing) != nil {
		return ""
	}
	prefix := sbx.SandboxName(target, "") + "-"
	dirs := []string{filepath.Clean(dir)}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		dirs = append(dirs, real)
	}
	var fresh, holding []string
	for _, sb := range listing.Sandboxes {
		if before[sb.Name] || !strings.HasPrefix(sb.Name, prefix) || strings.HasSuffix(sb.Name, "-prepare") {
			continue
		}
		fresh = append(fresh, sb.Name)
		for _, ws := range sb.Workspaces {
			ws, _, _ = strings.Cut(ws, ":")
			for _, d := range dirs {
				if pathWithin(d, ws) || pathWithin(ws, d) {
					holding = append(holding, sb.Name)
				}
			}
		}
	}
	switch {
	case len(fresh) == 1:
		return fresh[0]
	case len(holding) > 0:
		return holding[0]
	}
	return ""
}

// pathWithin reports whether p is dir or lies under it.
func pathWithin(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
