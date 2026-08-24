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

// pathWithFakeSbx puts a stub `sbx` first on PATH that answers `version` with
// the given string. It is how the VERSION gate is assertable at all: the real
// CLI reports whatever the host has installed, so a test that wanted to see
// proveo reject an old one could otherwise only wait for the world to age.
func pathWithFakeSbx(t *testing.T, version string) []string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  version) echo 'sbx version: v%s deadbeef' ;;\n  *) echo \"fake sbx: refusing $*\" >&2; exit 1 ;;\nesac\n", version)
	if err := os.WriteFile(filepath.Join(dir, sbx.Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// pathWithoutSbx already returns a "PATH=…" entry with every real sbx removed;
	// the stub goes in FRONT of those directories, inside the same assignment.
	clean := strings.TrimPrefix(pathWithoutSbx(t)[0], "PATH=")
	return []string{"PATH=" + dir + string(os.PathListSeparator) + clean}
}

// proveo owns the sbx version, so a CLI older than the surface this build drives
// must be REFUSED at selection time and named as such. Every drift the pin
// exists for — positional workspaces, --template, `rm --force`, the Kit schema —
// fails deep inside a run otherwise, where the operator cannot see it.
func TestSandboxBackendRefusesAnOutdatedSbx(t *testing.T) {
	requireDocker(t)
	old := "0.1.0" // unambiguously below any MinVersion this build will carry
	out := printOnlyRun(t, t.TempDir(), pathWithFakeSbx(t, old), "claudecode")

	if !strings.Contains(out, "falling back to docker+egress") {
		t.Errorf("an outdated sbx must fall back, got:\n%s", out)
	}
	if !strings.Contains(out, sbx.MinVersion) {
		t.Errorf("the fallback must name the version proveo targets (%s), got:\n%s", sbx.MinVersion, out)
	}
	if !strings.Contains(out, "docker run") {
		t.Errorf("the fallback must still render a runnable docker argv, got:\n%s", out)
	}
}

// The remedy proveo prints has to match the situation: an operator who already
// has sbx needs the UPGRADE line, and one who has none needs the INSTALL line.
// Printing "brew install" to someone who installed it yesterday is how a version
// gate becomes advice they follow twice and then ignore.
func TestSandboxBackendOffersInstallOrUpgradeToMatch(t *testing.T) {
	requireDocker(t)
	if sbx.InstallCmd(false) == "" {
		t.Skip("no sbx install route on this platform")
	}

	absent := printOnlyRun(t, t.TempDir(), pathWithoutSbx(t), "claudecode")
	if want := sbx.InstallCmd(false); !strings.Contains(absent, want) {
		t.Errorf("with no sbx on PATH, want the install line %q, got:\n%s", want, absent)
	}

	outdated := printOnlyRun(t, t.TempDir(), pathWithFakeSbx(t, "0.1.0"), "claudecode")
	if want := sbx.InstallCmd(true); !strings.Contains(outdated, want) {
		t.Errorf("with an outdated sbx, want the upgrade line %q, got:\n%s", want, outdated)
	}
}

// A dry run must never mutate the host. --print is how an operator inspects a
// posture before committing to it, so if it could install a package manager's
// worth of software the flag would stop being safe to reach for.
func TestSandboxBackendPrintOnlyInstallsNothing(t *testing.T) {
	requireDocker(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "INSTALL_ATTEMPTED")
	// A stub whose *install* route would leave evidence: proveo shells the install
	// line through bash, so a fake brew on PATH records any attempt.
	brew := filepath.Join(dir, "brew")
	script := "#!/bin/sh\ntouch " + marker + "\nexit 0\n"
	if err := os.WriteFile(brew, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := pathWithFakeSbx(t, "0.1.0")
	env[0] = "PATH=" + dir + string(os.PathListSeparator) + strings.TrimPrefix(env[0], "PATH=")
	// Even fully authorized to provision, --print must not act.
	env = append(env, "PROVEO_AUTO_PROVISION=1")

	printOnlyRun(t, t.TempDir(), env, "claudecode")
	if _, err := os.Stat(marker); err == nil {
		t.Error("--print attempted an install; a dry run must only report")
	}
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

// sandboxHarnesses are the targets whose manifest declares docker: sbx, read
// from the defs rather than restated here — a harness that changes how it gets a
// daemon must not be able to drift out of this suite's coverage silently.
var sandboxHarnesses = dockerTargets(manifest.Manifest.IsSbx)

// dindHarnesses are the targets whose manifest declares docker: dind.
var dindHarnesses = dockerTargets(manifest.Manifest.IsDind)

// dockerTargets lists every target whose manifest satisfies pick, in def order.
// It runs at package init, so a defs/ tree it cannot read is a panic rather than
// a silently empty matrix that reports success by testing nothing.
func dockerTargets(pick func(manifest.Manifest) bool) []string {
	wd, err := os.Getwd()
	if err != nil {
		panic("sbx suite: cwd: " + err.Error())
	}
	ms, err := manifest.Load(filepath.Join(wd, "..", "..", "defs"))
	if err != nil {
		panic("sbx suite: load manifests: " + err.Error())
	}
	var out []string
	for _, m := range ms {
		if !pick(m) {
			continue
		}
		for name := range m.Images {
			if name == m.Name {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

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
// The dispatch above is only as honest as the two lists it dispatches on, and
// both are derived. An empty or overlapping matrix would report success by
// testing nothing, so the partition itself is asserted: every def that promises a
// daemon lands in exactly one branch.
func TestDockerAccessMatrixPartitionsEveryPromise(t *testing.T) {
	t.Parallel()
	ms, err := manifest.Load(filepath.Join(repoRoot(t), "defs"))
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	if len(sandboxHarnesses) == 0 || len(dindHarnesses) == 0 {
		t.Fatalf("both branches must have targets: sbx=%v dind=%v", sandboxHarnesses, dindHarnesses)
	}
	for _, m := range ms {
		if !m.WantsDocker() {
			continue
		}
		inSbx, inDind := contains(sandboxHarnesses, m.Name), contains(dindHarnesses, m.Name)
		if inSbx == inDind {
			t.Errorf("%s promises a daemon (docker: %s) but is in %d branches, want exactly 1 "+
				"(sbx=%v dind=%v)", m.Name, m.Docker, map[bool]int{true: 2, false: 0}[inSbx],
				inSbx, inDind)
		}
	}
	t.Logf("docker access matrix: sbx=%v dind=%v", sandboxHarnesses, dindHarnesses)
}

func TestEveryHarnessGetsTheDockerAccessItPromises(t *testing.T) {
	requireTmux(t)

	targets := append(append([]string{}, dindHarnesses...), sandboxHarnesses...)
	if len(targets) == 0 {
		t.Fatal("no def promises a docker daemon — the matrix cannot be empty")
	}
	sort.Strings(targets)

	sbxOK, sbxWhy := sbx.Available()
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			switch {
			case contains(sandboxHarnesses, target):
				if !sbxOK {
					t.Skipf("sbx not available on this host: %s", sbxWhy)
				}
				sandboxDockerProbe(t, target)
			default:
				requireDocker(t)
				dindDockerProbe(t, target)
			}
		})
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// dindDockerProbe is the dind half of the same promise: the daemon arrives as a
// privileged SIBLING sidecar rather than a per-sandbox engine inside a microVM.
// The claim is identical — `docker` reaches a daemon — so the probe is, too; only
// the posture that gets one differs, and it is narrow: the sidecar is offered
// solely on the plain-bridge tier with credentials forwarded (dind.ModeSupported
// / dind.CredentialsSupported), because exposing a Docker socket through an
// intercepting tier would defeat the egress enforcement it sits behind.
func dindDockerProbe(t *testing.T, target string) {
	t.Helper()
	proveoBin := buildProveo(t)

	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "config", "user.email", "e2e@proveo.test")
	mustRun(t, work, "git", "config", "user.name", "proveo e2e")
	// dind.ShouldStart only offers the sidecar for a scope that actually builds
	// containers, so the workspace has to carry a Dockerfile or the sidecar this
	// test exists to exercise is never started.
	if err := os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := tmux.New(fmt.Sprintf("proveo-dind-%s-%d", target, os.Getpid()), nil)
	t.Cleanup(sess.Kill)

	cmd := []string{"env"}
	cmd = append(cmd, childEnvArgs(t)...)
	cmd = append(cmd,
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		"PROVEO_DIND=1", // the sidecar is opt-in; this is the opt
		proveoBin, "run", target,
		"--egress-mode", "open", "--credentials", "forward",
		"--shell", "--input", work,
	)
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start dind session: %v", err)
	}

	w := newWatcher(t, sess)
	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 6*time.Minute)

	// The sidecar announces itself, so the test never infers the posture: a run
	// that silently declined to start it would satisfy the daemon probe from some
	// OTHER socket and prove nothing about `docker: dind`.
	w.until("the dind sidecar line", 3*time.Minute, func() bool {
		return strings.Contains(w.Screen(), "sidecar: DinD")
	})
	w.until("the agent shell prompt", timeout, func() bool { return promptReady(w.Screen()) })

	const mark = "DIND-MOUNT-OK"
	probe := "{ docker version --format '{{.Server.Version}}'; } > " + probeDocker + " 2>&1; " +
		"printf %s " + mark + " > " + probeMount
	if err := sess.SendText(probe); err != nil {
		t.Fatalf("send probe: %v", err)
	}
	if err := sess.Enter(); err != nil {
		t.Fatalf("send probe newline: %v", err)
	}
	w.until("the agent to write through the workspace mount", 3*time.Minute, func() bool {
		return strings.Contains(readIn(work, probeMount), mark) &&
			strings.TrimSpace(readIn(work, probeDocker)) != ""
	})

	assertDockerServerReachable(t, target, "dind", strings.TrimSpace(readIn(work, probeDocker)))

	_ = sess.SendText("exit")
	_ = sess.Enter()
	if _, exited := waitSessionExit(sess, 3*time.Minute); !exited {
		t.Errorf("%s: the dind session did not exit after `exit`", target)
	}
}

// assertDockerServerReachable is the shared verdict on the promise itself. Both
// postures fail the same three ways, and each one names a different repair — a
// missing client is an image problem, a refused connection is a topology problem,
// and prose where a version belongs is neither.
func assertDockerServerReachable(t *testing.T, target, how, got string) {
	t.Helper()
	low := strings.ToLower(got)
	switch {
	case strings.Contains(low, "command not found"), strings.Contains(low, "executable file not found"):
		t.Fatalf("%s declares docker: %s but its image ships no docker client: %q\n"+
			"add the static-client layer to the image and rebuild (proveo build %s)", target, how, got, target)
	case strings.Contains(low, "cannot connect to the docker daemon"),
		strings.Contains(low, "is the docker daemon running"),
		strings.Contains(low, "permission denied"):
		t.Fatalf("%s: the docker client is present but docker: %s exposed no usable daemon: %q", target, how, got)
	case !dockerVersionish(got):
		t.Fatalf("%s: `docker version` returned %q, want a server version", target, got)
	}
	t.Logf("%s: docker server reached via %s = %s", target, how, got)
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
	assertDockerServerReachable(t, target, "sbx", strings.TrimSpace(readIn(work, probeDocker)))

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
