//go:build e2e

// SPEC: _spec/internal/egress/teardown-and-signals.puml, _spec/internal/egress/teardown-and-signals.puml
package e2e

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// dockerNamesMatching lists every container and network whose name carries sid.
func dockerNamesMatching(t *testing.T, sid string) []string {
	t.Helper()
	var found []string
	for _, q := range [][]string{
		{"ps", "-a", "--format", "{{.Names}}"},
		{"network", "ls", "--format", "{{.Name}}"},
	} {
		out, err := exec.Command("docker", q...).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if n := strings.TrimSpace(line); n != "" && strings.Contains(n, sid) {
				found = append(found, n)
			}
		}
	}
	return found
}

// The docker+egress backend brings up sidecars and a network, and tears them down
// through ONE cleanup closure guarded by sync.Once — reached either by defer on
// the normal path or by the SIGINT handler. No golden can see that: a plan golden
// asserts what would be created, never what survives an interrupt.
//
// This is the behaviour an operator actually notices when it breaks. An orphaned
// Squid holds its network, the next run's create fails on a name clash, and the
// error names a container the operator never asked for.
//
// It runs on a REAL docker daemon against a REAL signal because the failure mode
// is precisely the one a fake cannot reproduce: cleanup that works when called
// twice from one goroutine but not when defer and the handler race.
func TestSIGINTTearsDownEgressSidecars(t *testing.T) {
	if os.Getenv("PROVEO_SIGNAL_TEST") != "1" {
		t.Skip("set PROVEO_SIGNAL_TEST=1 to run the SIGINT teardown check (needs docker, ~90s)")
	}
	requireDocker(t)
	const target = "claudecode"
	harnessImage(t, target)
	proveoBin := buildProveo(t)

	work := t.TempDir()
	args := append(childEnvArgsNoCredential(t), proveoBin, "run", target,
		"--input", work, "--egress-mode", "allowlist")
	cmd := exec.Command("env", args...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 200})

	var mu sync.Mutex
	var buf strings.Builder
	seen := func() string { mu.Lock(); defer mu.Unlock(); return buf.String() }
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := ptmx.Read(b)
			if n > 0 {
				mu.Lock()
				buf.Write(b[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	// Wait until the run has a session id AND docker actually shows something
	// under it — interrupting before the sidecars exist would pass vacuously.
	var sid string
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if sid == "" {
			sid = runIDPattern.FindString(seen())
		}
		if sid != "" && len(dockerNamesMatching(t, sid)) > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if sid == "" {
		t.Fatalf("the run never printed a session id\n%s", lastLines(seen(), 25))
	}
	before := dockerNamesMatching(t, sid)
	if len(before) == 0 {
		t.Skipf("no sidecars or network came up for %s — nothing to tear down, so this proves nothing\n%s",
			sid, lastLines(seen(), 25))
	}
	t.Logf("up: %s", strings.Join(before, ", "))

	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		for _, n := range dockerNamesMatching(t, sid) {
			_ = exec.Command("docker", "rm", "-f", n).Run()
			_ = exec.Command("docker", "network", "rm", n).Run()
		}
	})

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("signal: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("proveo did not exit within 90s of SIGINT — the handler is not reaching cleanup")
	}

	// Teardown is asynchronous only in the sense that docker takes a moment to
	// reap; give it room rather than asserting on the instant of exit.
	for i := 0; i < 15; i++ {
		if len(dockerNamesMatching(t, sid)) == 0 {
			return
		}
		time.Sleep(time.Second)
	}
	t.Errorf("SIGINT left these behind after 15s: %s\n"+
		"the once-guarded cleanup did not run, or ran before the sidecars existed\n%s",
		strings.Join(dockerNamesMatching(t, sid), ", "), lastLines(seen(), 30))
}
