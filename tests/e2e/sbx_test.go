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

	"gopkg.in/yaml.v3"

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

// TestDindHarnessesShipADockerClient guards the half of the contract the
// manifest cannot state on its own: `docker: dind` promises the AGENT a daemon,
// and a daemon it has no client for is a promise the image breaks — not a
// failure, just `docker: command not found` at the moment the agent tries to use
// it.
//
// Only the dind branch is held to this. `docker: sbx` promises the opposite (see
// sandboxBoundaryProbe), and sbx strips the client from the image anyway, so
// requiring one there would demand a binary the sandbox deletes.
func TestDindHarnessesShipADockerClient(t *testing.T) {
	ms, err := manifest.Load(filepath.Join(repoRoot(t), "defs"))
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	var targets []string
	for _, m := range ms {
		if m.IsDind() {
			targets = append(targets, m.Name)
		}
	}
	if len(targets) == 0 {
		t.Fatal("no harness declares docker: dind — the invariant has nothing to guard")
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
//  3. the sandbox REPLACES docker — no client, no socket (the promise itself)
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
// Every sbx defect this suite exists to catch was found by hand first: `-w` and
// `-v` rejected, an image in the agent positional, a Kit schema sbx would not
// parse, an agent name that had to match the Kit's, a stale template silently
// served. The run probe below catches them only by TIMING OUT, which names none of
// them — so these three assertions cover the same ground cheaply and say what
// broke.
//
// Renders the Kit proveo would write and hands it to sbx's own validator. This is
// the assertion that would have caught the shipped Kit outright: `image` and
// `credentialsEnv` are not fields of spec.SpecFile, and every sandbox run died at
// "resolve kits" until they moved.
func TestSandboxKitValidatesAgainstTheRealCLI(t *testing.T) {
	if ok, why := sbx.Available(); !ok {
		t.Skipf("sbx not available on this host: %s", why)
	}
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			kitDir := renderKit(t, target)
			out, err := exec.Command(sbx.Binary, "kit", "validate", kitDir).CombinedOutput()
			spec, _ := os.ReadFile(filepath.Join(kitDir, "spec.yaml"))
			if err != nil {
				t.Fatalf("%s: sbx rejected the Kit proveo writes: %v\n--- sbx ---\n%s\n--- spec.yaml ---\n%s",
					target, err, out, spec)
			}
			// A deprecation warning is sbx telling us the schema moved under us,
			// which is the drift this whole file is guarding against.
			if strings.Contains(string(out), "deprecated") {
				t.Errorf("%s: Kit uses a deprecated field — the schema has moved:\n%s\n--- spec.yaml ---\n%s",
					target, out, spec)
			}
		})
	}
}

// The Kit proveo writes is a MIXIN beside one of sbx's own agents, and the division
// is not stylistic. sbx's agent registry is closed, so an identity of proveo's own
// receives no artifact, skips the binding gate and abandons the session within
// seconds — which is what every "exited with code 137" turned out to be.
//
// What follows from that shape is what this asserts. A mixin must declare NO
// credentials: repeating a service the built-in agent already declares is rejected
// outright ("defined in both"), so the correct count is zero rather than "only
// permitted ones" — the older assertion this replaces scanned for over-declared
// services and, once the block was removed entirely, passed by finding nothing.
func TestSandboxKitIsAMixinCarryingNoCredentials(t *testing.T) {
	if ok, why := sbx.Available(); !ok {
		t.Skipf("sbx not available on this host: %s", why)
	}
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(renderKit(t, target), "spec.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var kit struct {
				SchemaVersion string `yaml:"schemaVersion"`
				Kind          string `yaml:"kind"`
				Name          string `yaml:"name"`
				Credentials   []struct {
					Service string `yaml:"service"`
				} `yaml:"credentials"`
				Environment struct {
					Variables map[string]string `yaml:"variables"`
				} `yaml:"environment"`
				Setup struct {
					Startup []struct {
						Command []string `yaml:"command"`
						User    string   `yaml:"user"`
					} `yaml:"startup"`
				} `yaml:"setup"`
			}
			if err := yaml.Unmarshal(raw, &kit); err != nil {
				t.Fatalf("%s: Kit is not parseable YAML: %v\n%s", target, err, raw)
			}

			if kit.Kind != "mixin" {
				t.Errorf("%s: kind=%q; a sandbox kind names an agent sbx does not know\n%s",
					target, kit.Kind, raw)
			}
			// SPEC-v2 types this as a string. An int is normalised on the way in, so
			// the mistake survives validate and only shows up against a stricter reader.
			if kit.SchemaVersion != "2" {
				t.Errorf("%s: schemaVersion=%q, want the string \"2\"\n%s",
					target, kit.SchemaVersion, raw)
			}
			for _, c := range kit.Credentials {
				t.Errorf("%s: mixin declares credential %q — the built-in agent owns "+
					"credentials, and declaring one twice is refused as \"defined in both\"\n%s",
					target, c.Service, raw)
			}
			if builtin := sbx.BuiltinAgent(target); builtin != "" && kit.Name == builtin {
				t.Errorf("%s: Kit name %q shadows the built-in agent; sbx refuses that outright",
					target, kit.Name)
			}

			// The seed is the file-shaped half of setup, and it must run as the agent
			// user or it composes into a home the agent never reads.
			var seeded bool
			for _, st := range kit.Setup.Startup {
				if len(st.Command) > 0 && strings.HasSuffix(st.Command[0], "proveo-seed") {
					seeded = true
					if st.User != "1000" {
						t.Errorf("%s: seed runs as user %q, want the agent's 1000", target, st.User)
					}
				}
			}
			if !seeded {
				t.Errorf("%s: no proveo-seed step in setup.startup — subagents, settings and "+
					"workspace trust would never be composed\n%s", target, raw)
			}

			// Env-shaped work is resolved host-side because a setup command exports
			// into a process the agent never inherits. PROVEO_HOME in particular
			// cannot be read from $HOME: sbx runs startup as user 1000, which reloads
			// HOME from /etc/passwd.
			for _, k := range []string{"HOME", "PROVEO_HOME", "PROVEO_WORKDIR"} {
				if kit.Environment.Variables[k] == "" {
					t.Errorf("%s: Kit environment omits %s\n%s", target, k, raw)
				}
			}
			if h, s := kit.Environment.Variables["HOME"], kit.Environment.Variables["PROVEO_HOME"]; h != s {
				t.Errorf("%s: PROVEO_HOME=%q must equal HOME=%q, or the seed targets "+
					"a different directory than the agent", target, s, h)
			}
		})
	}
}

// proveo OMITS a suppressed credential rather than stating it as "-e VAR=", and
// that choice rests entirely on a claim about sbx: that a secret sitting in its
// GLOBAL store does not reach the container as an environment variable. This test
// is that claim, held against the real CLI.
//
// It matters because the empty value is not inert. An agent reads a SET variable as
// a chosen credential whatever it holds, and claudecode ranks ANTHROPIC_API_KEY and
// CLAUDE_CODE_OAUTH_TOKEN above the login on disk — so if proveo ever goes back to
// stating them empty, a blank one takes the slot the mounted login needed and an
// unattended run stalls asking a human to approve a key that authenticates nothing.
// If sbx starts exporting stored secrets as env vars, omission stops being safe and
// this is where that shows up, rather than in a run that dies twenty seconds in.
//
// Read-only by design: it asserts against whatever the operator's store already
// holds and writes nothing to it, so it skips rather than manufacturing a secret.
func TestSandboxStoreDoesNotExportSecretsAsEnvVars(t *testing.T) {
	if ok, why := sbx.Available(); !ok {
		t.Skipf("sbx not available on this host: %s", why)
	}
	stored, err := exec.Command(sbx.Binary, "secret", "ls").CombinedOutput()
	if err != nil {
		t.Skipf("cannot read the secret store: %v\n%s", err, stored)
	}
	var probe []string
	for _, name := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if strings.Contains(string(stored), name) {
			probe = append(probe, name)
		}
	}
	if len(probe) == 0 {
		t.Skip("no auth var in the store — nothing to prove about injection")
	}

	name := fmt.Sprintf("proveo-envprobe-%d", time.Now().UnixNano())
	ws := t.TempDir()
	if out, err := exec.Command(sbx.Binary, "create", "--name", name, "claude", ws).CombinedOutput(); err != nil {
		t.Fatalf("create %s: %v\n%s", name, err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(sbx.Binary, "rm", "--force", name).CombinedOutput(); err != nil {
			t.Logf("probe sandbox %s not removed: %v\n%s", name, err, out)
		}
	})

	// `env` prints only what is SET, so absence from this listing is the assertion.
	// Keep the command short: an exec that runs for tens of seconds is torn down with
	// the sandbox underneath it and returns no output at all.
	out, err := exec.Command(sbx.Binary, "exec", name, "--", "env").CombinedOutput()
	if err != nil {
		t.Fatalf("exec env in %s: %v\n%s", name, err, out)
	}
	for _, k := range probe {
		if regexp.MustCompile(`(?m)^` + k + `=`).Match(out) {
			t.Errorf("%s is in sbx's global store and REACHED the container as an env var — "+
				"omitting a suppressed credential no longer keeps it out, so sandboxSpec must "+
				"neutralize again (see _spec/_paradigms/credential-boundary.puml)", k)
		}
	}
}

// sbxError returns sbx's own error line from the pane, if it printed one. It is
// how a red test names its cause instead of reporting a timeout.
// retryable marks the sbx errors proveo answers with one reload-and-retry, so the
// probe waits for that second attempt instead of failing on the first.
func retryable(line string) bool {
	return strings.Contains(line, "failed to run sandbox container") ||
		strings.Contains(line, "failed to create sandbox")
}

func sbxError(screen string) string {
	for _, line := range strings.Split(screen, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ERROR:") {
			return line
		}
	}
	return ""
}

// brokenPrompt spots the interactive questions that mean something is WRONG, as
// opposed to the ones that are simply sbx asking the operator a question.
//
// The distinction matters and it was got wrong once. A `proveo run` is interactive
// by design — there is a whole choice form in front of it — so sbx asking which
// credentials a Kit may use is a normal gate that a PTY-driven run answers, and
// answerSbxPrompt below does. What is NOT normal is a confirmation that eats
// input meant for something else: `sbx secret set` reads the value from stdin,
// and on a re-run its "Overwrite?" question consumes that piped value and cancels
// the write, leaving the agent on a stale credential with no error. That one is
// answered by --force, so seeing it again means the flag regressed.
func brokenPrompt(screen string) string {
	for _, want := range []string{
		"Overwrite? (y/N)",
		"Delete selected secret? (y/N)",
		// sbx will not invent a missing workspace path, it asks. proveo creates the
		// output dir up front so this never appears; seeing it means something is
		// handing sbx a path that does not exist, and the run stops dead.
		"does not exist. Would you like to create it?",
	} {
		if strings.Contains(screen, want) {
			return want
		}
	}
	return ""
}

// answerSbxPrompt takes the default on sbx's own operator questions, which a run
// on a PTY is expected to answer rather than trip over.
func answerSbxPrompt(sess *tmux.Session, screen string) bool {
	if !strings.Contains(screen, "[A]pprove all") {
		return false
	}
	_ = sess.SendText("A")
	_ = sess.Enter()
	return true
}

// renderKit performs a real run far enough to write the Kit, then returns its
// directory. --print does NOT write one (only runSandbox does), which is why this
// drives the run and kills it once the file exists.
func renderKit(t *testing.T, target string) string {
	t.Helper()
	// --print, not a live session. The Kit is written while the launch is RESOLVED,
	// before any agent starts, so a dry run produces exactly the document a real run
	// hands sbx — the same property that makes --print show the true argv.
	//
	// Driving a live run instead cost three minutes per target and made a Kit
	// assertion depend on whether the agent could authenticate and hold a session,
	// which is a different claim entirely and one this file already covers elsewhere.
	proveoBin := buildProveo(t)
	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", ".")
	state := t.TempDir()

	cmd := exec.Command(proveoBin, "run", target, "--print", "--input", work)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(),
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_EGRESS_ROOT="+state,
		"PROVEO_WIZARD=off",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("proveo run %s --print: %v\n%s", target, err, out)
	}

	// <state>/egress/<session>/sbx/kit — the "egress" segment is stateDir()'s own
	// layout, not the session dir.
	matches, _ := filepath.Glob(filepath.Join(state, "egress", "*", "sbx", "kit", "spec.yaml"))
	if len(matches) == 0 {
		t.Fatalf("%s: --print rendered no Kit under %s", target, state)
	}
	return filepath.Dir(matches[0])
}

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
			t.Errorf("%s declares docker: %s but is in %d branches, want exactly 1 "+
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
				sandboxBoundaryProbe(t, target)
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
	//
	// Two phrasings, because there are two legitimate ways in and they print
	// different lines: an explicitly CHECKED add-on says "sidecar: DinD (same
	// image)" from cmd/proveo, while PROVEO_DIND=1 goes through dind.ShouldStart
	// and says "Starting sibling Docker-in-Docker". Keying on only the first is
	// what made this wait time out against a sidecar that was already running.
	w.until("the dind sidecar line", 3*time.Minute, func() bool {
		scr := w.Screen()
		return strings.Contains(scr, "sidecar: DinD") ||
			strings.Contains(scr, "Starting sibling Docker-in-Docker")
	})
	w.until("the agent shell prompt", timeout, func() bool { return promptReady(w.Screen()) })

	const mark = "DIND-MOUNT-OK"
	// The probe RETRIES, because `docker:dind` starts its daemon asynchronously —
	// several seconds after the container is up — while proveo hands the agent its
	// shell immediately. A one-shot probe therefore raced the daemon and recorded
	// "Cannot connect" as if the posture were broken. Each attempt overwrites the
	// file, so the last write is either a version or the error worth reporting.
	probe := "printf %s " + mark + " > " + probeMount + "; " +
		"for i in $(seq 1 45); do " +
		"if docker version --format '{{.Server.Version}}' > " + probeDocker + " 2>&1; then break; fi; " +
		"sleep 2; done"
	if err := sess.SendText(probe); err != nil {
		t.Fatalf("send probe: %v", err)
	}
	if err := sess.Enter(); err != nil {
		t.Fatalf("send probe newline: %v", err)
	}
	w.until("the agent to write through the workspace mount", 3*time.Minute, func() bool {
		return strings.Contains(readIn(work, probeMount), mark)
	})

	// Poll rather than assert once: the loop above is still retrying, so the first
	// content the file holds may be a connection error that a later attempt fixes.
	deadline := time.Now().Add(2 * time.Minute)
	var got string
	for {
		got = strings.TrimSpace(readIn(work, probeDocker))
		if dockerVersionish(got) || time.Now().After(deadline) {
			break
		}
		if !w.tick() {
			break
		}
		time.Sleep(3 * time.Second)
	}
	assertDockerServerReachable(t, target, "dind", got)

	_ = sess.SendText("exit")
	_ = sess.Enter()
	if _, exited := waitSessionExit(sess, 3*time.Minute); !exited {
		t.Errorf("%s: the dind session did not exit after `exit`", target)
	}
}

// assertDockerServerReachable is the verdict on what `docker: dind` promises.
// It fails three ways, and each names a different repair — a missing client is an
// image problem, a refused connection is a topology problem, and prose where a
// version belongs is neither.
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

// assertSandboxReplacesDocker is the verdict on what `docker: sbx` promises, and
// it is the inverse of the dind one. sbx hands the workload no daemon: the
// microVM IS the isolation a daemon would otherwise be asked for, so it binds no
// socket and sets no DOCKER_HOST, and `docker version` inside it answers
// "Cannot connect to the Docker daemon" on both defs.
//
// The CLIENT is deliberately not asserted on, in either direction. Whether the
// binary survives into the sandbox is not proveo's to promise and not stable:
// cursor's sandbox has it and claudecode's does not, from images that both
// install it, because `sbx create` re-bakes the template (see
// _spec/_experiments/docker-sandbox.puml) and the claude-flavoured bake drops it.
// Asserting either way would encode an sbx implementation detail as a proveo
// contract and break on the next sbx release.
//
// What IS the contract is that nothing reaches a daemon. A socket appearing
// inside the sandbox would punch through the boundary the backend was chosen for.
func assertSandboxReplacesDocker(t *testing.T, target, got string) {
	t.Helper()
	if !strings.Contains(got, "NO-SOCKET") {
		t.Errorf("%s declares docker: sbx but a docker socket is exposed inside the sandbox: %q\n"+
			"that punches through the isolation boundary the sandbox backend was chosen for", target, got)
	}
	if _, server, ok := strings.Cut(got, "SERVER:"); ok {
		if v := strings.TrimSpace(strings.SplitN(server, "\n", 2)[0]); dockerVersionish(v) {
			t.Errorf("%s declares docker: sbx, which promises the sandbox REPLACES docker, "+
				"but a daemon answered inside it with server version %q\n"+
				"the sandbox is meant to BE the isolation — reaching a daemon from inside it "+
				"means the def belongs on docker: dind, or a socket leaked in", target, v)
		}
	} else {
		t.Errorf("%s: the docker probe wrote no SERVER: line, so nothing was actually tested: %q", target, got)
	}
	t.Logf("%s: the sandbox replaces docker (no socket, no daemon): %s", target,
		strings.Join(strings.Fields(got), " · "))
}

// sbxShellHoldsInADetachedPane reports whether sbx's own shell agent survives being
// driven from a detached tmux pane. It does not, today: `sbx run -t <image> shell
// <workspace>` exits within seconds there with proveo uninvolved, so the probe below
// cannot reach any of its four claims.
//
// Checked rather than assumed, and checked against SBX rather than proveo, so this
// turns back into coverage by itself the day sbx holds — instead of staying a
// permanent red that the next real regression could hide behind.
func sbxShellHoldsInADetachedPane(t *testing.T) bool {
	t.Helper()
	work := t.TempDir()
	name := fmt.Sprintf("proveo-shellprobe-%d", os.Getpid())
	sess := tmux.New(name, nil)
	t.Cleanup(func() {
		sess.Kill()
		_ = exec.Command(sbx.Binary, "rm", "--force", name).Run()
	})
	if err := sess.Start(120, 40, sbx.Binary, "run", "--name", name, "-t", "proveo/cursor:local", "shell", work); err != nil {
		return false
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		screen, err := sess.CaptureAll()
		if err != nil {
			return false // the pane is gone: the shell did not hold
		}
		if promptReady(screen) || strings.Contains(screen, "$ ") {
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}

func sandboxBoundaryProbe(t *testing.T, target string) {
	t.Helper()
	if !sbxShellHoldsInADetachedPane(t) {
		t.Skipf("sbx's own shell agent does not survive a detached tmux pane on this host, "+
			"so %s cannot be driven to a prompt here; the sandbox Kit and backend selection "+
			"stay covered by TestSandboxKit* and TestSandboxBackend*", target)
	}
	// The agent has to hold a session for any of the four claims below to mean
	// anything, and it cannot hold one without a credential to spend. Checked up
	// front so the reason is stated in one line rather than inferred from a probe
	// that waited out its deadline against an agent that had already exited.
	requireHarnessCredential(t, target)
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
	// The env file must carry THIS harness's credential. childEnvArgs writes an
	// anthropic-only file, so a cursor run received no CURSOR_API_KEY at all and its
	// agent exited before the backend line — a probe about the sandbox boundary
	// failing on a credential it had declined to provide.
	cmd := []string{"env"}
	if secrets := harnessSecrets(t, target); len(secrets) > 0 {
		cmd = append(cmd, childEnvArgsFor(t, secrets[0])...)
	} else {
		cmd = append(cmd, childEnvArgs(t)...)
	}
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

	// Fail on sbx's OWN error rather than waiting out the clock. Every adapter
	// defect so far — a rejected flag, an unparseable Kit, an agent name that did
	// not match the Kit's — printed a one-line ERROR here and then went quiet, so
	// the only signal was a timeout six minutes later that named none of them.
	//
	// A start failure is the exception: proveo retries it ONCE on a freshly loaded
	// template, because sbx's stored template can go bad on its own (see
	// _spec/_experiments/docker-sandbox.puml). Failing on sight would call that
	// run broken while the repair was still in flight, so the retry is allowed to
	// finish and the error is only fatal if the session dies with it on screen.
	w.until("the sandbox shell prompt", timeout, func() bool {
		if line := sbxError(w.Screen()); line != "" && !retryable(line) {
			w.Fatalf("%s: sbx refused the run — %s", target, line)
		}
		if line := brokenPrompt(w.Screen()); line != "" {
			w.Fatalf("%s: a confirmation is eating input meant for something else — %s\n"+
				"`sbx secret set` reads the secret from stdin, so this prompt consumes it and "+
				"cancels the write; --force is what keeps that from happening", target, line)
		}
		// sbx asks the operator which credentials the Kit may use. That is a normal
		// gate, not a fault — answer it the way an operator would.
		answerSbxPrompt(sess, w.Screen())
		return promptReady(w.Screen())
	})

	// One command covers claims 2 and 3: ask the daemon for its version, then
	// write a marker — both through the mounted workspace, read back host-side.
	// stderr is captured too, because the INTERESTING failures ("command not
	// found", "cannot connect to the Docker daemon") only ever appear there.
	const mark = "SBX-MOUNT-OK"
	probe := "{ ls /var/run/docker.sock >/dev/null 2>&1 && echo HAS-SOCKET || echo NO-SOCKET; " +
		"printf SERVER:; timeout 20 docker version --format '{{.Server.Version}}' 2>/dev/null; echo; } > " +
		probeDocker + " 2>&1; printf %s " + mark + " > " + probeMount
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

	// Claim 3 — the boundary. This is the whole point of `docker: sbx`.
	assertSandboxReplacesDocker(t, target, strings.TrimSpace(readIn(work, probeDocker)))

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

// sbx runs a Kit's setup.startup command as `user: "1000"`, and that reloads HOME
// from /etc/passwd — so $HOME inside the seed is the IMAGE's home, not the one the
// agent will run with. PROVEO_HOME carries the same path under a name no launcher
// rewrites. Without it the seed composed subagents into /home/claude while the
// agent ran with the mounted proveo home and read none of them, silently: seeding
// reported success, and only the startup log named the directory it had used.
func TestSandboxBackendCarriesProveoHomeBesideHome(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sbx not available on this host")
	}
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			out := printOnlyRun(t, t.TempDir(), nil, target)
			home := envValueInArgv(out, "HOME")
			seed := envValueInArgv(out, "PROVEO_HOME")
			if home == "" {
				t.Fatalf("sbx argv sets no HOME:\n%s", out)
			}
			if seed != home {
				t.Errorf("PROVEO_HOME=%q must equal HOME=%q, or the seed targets a\n"+
					"different directory than the agent:\n%s", seed, home, out)
			}
		})
	}
}

// envValueInArgv returns the value of the last `-e NAME=VALUE` pair in a rendered
// argv. Last wins, matching how the runtime resolves a repeated flag.
func envValueInArgv(argv, name string) string {
	want := name + "="
	got := ""
	fields := strings.Fields(argv)
	for i, f := range fields {
		if f != "-e" || i+1 >= len(fields) {
			continue
		}
		if v, ok := strings.CutPrefix(fields[i+1], want); ok {
			got = v
		}
	}
	return got
}
