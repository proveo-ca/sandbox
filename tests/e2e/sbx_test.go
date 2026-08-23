//go:build e2e

// SPEC: _spec/internal/sbx/sandbox-backend.puml
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// sbxAvailable reports whether the host can run the sandbox backend.
func sbxAvailable() bool {
	ok, _ := sbx.Available()
	return ok
}

// pathWithoutSbx builds a PATH with every sbx-resolving entry removed.
func pathWithoutSbx(t *testing.T) []string {
	t.Helper()
	var parts []string
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(p, sbx.Binary)); err == nil {
			continue
		}
		parts = append(parts, p)
	}
	return []string{"PATH=" + strings.Join(parts, string(os.PathListSeparator))}
}

func printOnlyRun(t *testing.T, workdir string, extraEnv []string, target string) string {
	t.Helper()
	proveoBin := buildProveo(t)
	cmd := exec.Command(proveoBin, "run", target, "--print")
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo run %s --print-only: %v\n%s", target, err, out)
	}
	return string(out)
}

// sandboxHarnesses are the manifests that declare docker: sbx.
var sandboxHarnesses = []string{"claudecode", "cursor"}

func TestSandboxBackendPrintOnlyRendersSbx(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sbx not available on this host")
	}
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			out := printOnlyRun(t, t.TempDir(), nil, target)
			if !strings.Contains(out, "# agent") || !strings.Contains(out, "sbx run ") {
				t.Errorf("print-only should render the sbx invocation, got:\n%s", out)
			}
			if strings.Contains(out, "\ndocker run") {
				t.Errorf("sbx backend selected but docker argv rendered:\n%s", out)
			}
		})
	}
}

func TestSandboxBackendKitFlagPointsAtADirectory(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sbx not available on this host")
	}
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			out := printOnlyRun(t, t.TempDir(), nil, target)
			i := strings.Index(out, "--kit ")
			if i < 0 {
				t.Fatalf("sbx argv carries no --kit flag:\n%s", out)
			}
			kit := strings.Fields(out[i+len("--kit "):])[0]
			if strings.HasSuffix(kit, "spec.yaml") {
				t.Errorf("--kit = %q, want the directory holding spec.yaml", kit)
			}
		})
	}
}

func TestSandboxBackendFallsBackToDockerWhenSbxAbsent(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			out := printOnlyRun(t, t.TempDir(), pathWithoutSbx(t), target)
			if !strings.Contains(out, "falling back to docker+egress") {
				t.Errorf("expected a fallback notice, got:\n%s", out)
			}
			if !strings.Contains(out, "docker run") {
				t.Errorf("fallback must render the docker argv, got:\n%s", out)
			}
		})
	}
}

// TestHarnessesWithADockerDaemonShipADockerClient guards the half of the
// contract the manifest cannot state on its own. Any docker mode — sbx (daemon
// from the sandbox VM) or dind (daemon from the sidecar) — promises the AGENT a
// Docker daemon, and a daemon it has no client for is a promise the image
// breaks. claudecode declared its mode with no `docker` binary in the image,
// which is precisely how that gap presents: not a failure, just
// `docker: command not found` at the moment the agent tries to use it.
func TestHarnessesWithADockerDaemonShipADockerClient(t *testing.T) {
	ms, err := manifest.Load(filepath.Join(repoRoot(t), "defs"))
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	var targets []string
	for _, m := range ms {
		if m.WantsDocker() {
			targets = append(targets, m.Name)
		}
	}
	if len(targets) == 0 {
		t.Fatal("no harness declares a docker mode — the invariant has nothing to guard")
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			img := harnessImage(t, target)
			out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "docker", img, "--version").CombinedOutput()
			if err != nil {
				t.Errorf("%s promises the agent a Docker daemon but its image ships no docker client "+
					"(rebuild after adding the static-client layer: proveo build %s): %v\n%s",
					target, target, err, out)
			}
		})
	}
}

// ── the live sandbox: does the backend actually deliver what it promises? ────
//
// Everything above this line asserts the PLAN — the argv proveo would run, the
// Kit path, the fallback notice — because sandboxSpec is pure and `--print`
// costs nothing. None of it starts a VM, so none of it can tell you that sbx
// works on this host.
//
// TestSandboxBackendRunsDockerInsideTheSandbox is the one that can. It runs the
// harness FOR REAL on the sandbox backend and asserts the four things the
// manifest's `docker: sbx` actually claims:
//
//  1. the run took the sandbox backend (not the docker+egress fallback)
//  2. the workspace mount carries writes back to the host
//  3. `docker` inside the sandbox reaches a DAEMON — the promise itself
//  4. the sandbox is gone afterwards, VM and all
//
// It drives `--shell`, not the agent: the claim under test is the backend's,
// so there is no reason to spend a model call or a credential on it. Skipped
// unless the host can run sbx, which is also why it is the test that closes the
// "confirmed on Linux only" gap — run it on the Mac and the gap is closed or
// the failure names which of the four claims is false.
func TestSandboxBackendRunsDockerInsideTheSandbox(t *testing.T) {
	requireTmux(t)
	if ok, why := sbx.Available(); !ok {
		t.Skipf("sbx not available on this host: %s", why)
	}
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			sandboxDockerProbe(t, target)
		})
	}
}

func sandboxDockerProbe(t *testing.T, target string) {
	t.Helper()
	proveoBin := buildProveo(t)

	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "config", "user.email", "e2e@proveo.test")
	mustRun(t, work, "git", "config", "user.name", "proveo e2e")

	before, canList := sbxSandboxNames()
	if !canList {
		t.Logf("`%s ls` unavailable on this CLI — claim 4 (teardown) will not be checked", sbx.Binary)
	}

	sess := tmux.New(fmt.Sprintf("proveo-sbx-%s-%d", target, os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		removeLeakedSandboxes(t, before, canList)
	})

	// A scratch PROVEO_HOME keeps the operator's remembered add-on answer out of
	// this run: the sandbox add-on is default-ON, so an empty cache is the only
	// way to be sure the backend under test is the one that ran.
	cmd := []string{"env"}
	cmd = append(cmd, childEnvArgs(t)...)
	cmd = append(cmd,
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		proveoBin, "run", target, "--shell", "--input", work,
	)
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start sandbox session: %v", err)
	}

	w := newWatcher(t, sess)
	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 6*time.Minute)

	// Claim 1 — the backend. proveo announces its choice, so the test never has
	// to infer it: a run that quietly fell back to docker+egress would otherwise
	// satisfy claims 2 and 3 and prove nothing about sbx.
	w.until("the sbx backend line", 2*time.Minute, func() bool {
		s := w.Screen()
		if strings.Contains(s, "docker sandbox: off") {
			w.Fatalf("%s ran with the sandbox add-on unchecked — a remembered choice leaked into this run", target)
		}
		if strings.Contains(s, "falling back to docker+egress") {
			w.Fatalf("%s fell back to docker+egress on a host where sbx.Available() said yes", target)
		}
		return strings.Contains(s, "backend: docker sandboxes (sbx)")
	})

	w.until("the sandbox shell prompt", timeout, func() bool { return promptReady(w.Screen()) })

	// One command covers claims 2 and 3: ask the daemon for its version, then
	// write a marker — both through the mounted workspace, read back host-side.
	// stderr is captured too, because the INTERESTING failures ("command not
	// found", "cannot connect to the Docker daemon") only ever appear there.
	const mark = "SBX-MOUNT-OK"
	probe := "{ docker version --format '{{.Server.Version}}'; } > " + probeDocker + " 2>&1; " +
		"printf %s " + mark + " > " + probeMount
	if err := sess.SendText(probe); err != nil {
		t.Fatalf("send probe: %v", err)
	}
	if err := sess.Enter(); err != nil {
		t.Fatalf("send probe newline: %v", err)
	}

	// Claim 2 — the mount. The marker is written INSIDE the sandbox and read on
	// the host, so its arrival is the bind working in the direction that matters.
	w.until("the sandbox to write through the workspace mount", 3*time.Minute, func() bool {
		return strings.Contains(readIn(work, probeMount), mark) &&
			strings.TrimSpace(readIn(work, probeDocker)) != ""
	})

	// Claim 3 — the daemon. This is the whole point of `docker: sbx`: a client
	// with nothing behind it is the exact failure this suite existed to miss.
	got := strings.TrimSpace(readIn(work, probeDocker))
	low := strings.ToLower(got)
	switch {
	case strings.Contains(low, "command not found"), strings.Contains(low, "executable file not found"):
		t.Fatalf("%s declares docker: sbx but its image ships no docker client: %q\n"+
			"add the static-client layer to the image and rebuild (proveo build %s)", target, got, target)
	case strings.Contains(low, "cannot connect to the docker daemon"),
		strings.Contains(low, "is the docker daemon running"),
		strings.Contains(low, "permission denied"):
		t.Fatalf("%s: the docker client is present but the sandbox exposes no usable daemon: %q", target, got)
	case !dockerVersionish(got):
		t.Fatalf("%s: `docker version` inside the sandbox returned %q, want a server version", target, got)
	}
	t.Logf("%s: docker server inside the sandbox = %s", target, got)

	// Claim 4 — teardown. Exit the shell rather than killing the pane, so the
	// run's own `sbx rm` (VM + images + volumes) is what gets exercised.
	_ = sess.SendText("exit")
	_ = sess.Enter()
	if _, exited := waitSessionExit(sess, 3*time.Minute); !exited {
		t.Errorf("%s: the sandbox session did not exit after `exit`", target)
	}
	if !canList {
		return
	}
	var leaked []string
	deadline := time.Now().Add(60 * time.Second)
	for {
		leaked = newSandboxes(before)
		if len(leaked) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if len(leaked) > 0 {
		t.Errorf("%s: teardown left %v behind — the run must remove the sandbox, its images and its volumes",
			target, leaked)
	}
}

// probeMount / probeDocker are written by the sandbox into the mounted
// workspace, so the assertions read host-side files rather than scraping a pane.
const (
	probeMount  = "SBX_MOUNT.txt"
	probeDocker = "SBX_DOCKER.txt"
)

// promptReady reports whether the pane's last non-empty line looks like a shell
// prompt waiting for input. tmux trims trailing whitespace, so a prompt ends AT
// the sigil — matching on "$ " never fires.
func promptReady(screen string) bool {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimRight(lines[i], " \t")
		if l == "" {
			continue
		}
		return strings.HasSuffix(l, "$") || strings.HasSuffix(l, "#")
	}
	return false
}

// dockerVersionish reports whether s looks like a docker server version.
func dockerVersionish(s string) bool {
	return regexp.MustCompile(`^\d+\.\d+`).MatchString(s)
}

// sbxSandboxNames is the set of proveo-* sandboxes sbx currently holds, and
// whether the CLI could be listed at all. sbx is pre-GA: a listing it does not
// support is REPORTED by the caller, never quietly read as "nothing there".
func sbxSandboxNames() (map[string]bool, bool) {
	out, err := exec.Command(sbx.Binary, "ls").CombinedOutput()
	if err != nil {
		return nil, false
	}
	names := map[string]bool{}
	for _, f := range strings.Fields(string(out)) {
		if strings.HasPrefix(f, "proveo-") {
			names[f] = true
		}
	}
	return names, true
}

// newSandboxes is what this run added and has not cleaned up.
func newSandboxes(before map[string]bool) []string {
	now, ok := sbxSandboxNames()
	if !ok {
		return nil
	}
	var out []string
	for n := range now {
		if !before[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// removeLeakedSandboxes cleans up only what this test created — a failed run
// must not leave a VM behind, and must not remove anyone else's.
func removeLeakedSandboxes(t *testing.T, before map[string]bool, canList bool) {
	t.Helper()
	if !canList {
		return
	}
	for _, name := range newSandboxes(before) {
		if out, err := exec.Command(sbx.Binary, sbx.RemoveArgs(name)...).CombinedOutput(); err != nil {
			t.Logf("cleanup: %s rm %s: %v\n%s", sbx.Binary, name, err, out)
		}
	}
}
