//go:build e2e

// SPEC: _spec/internal/sbx/sandbox-backend.puml, _spec/_paradigms/retire-dind.puml, _spec/_plans/image-size-reduction.puml
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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

func TestSandboxBackendRefusesAnOutdatedSbx(t *testing.T) {
	old := "0.1.0" // unambiguously below any MinVersion this build will carry
	out, err := printOnlyRunErr(t, t.TempDir(), pathWithFakeSbx(t, old), "claudecode")
	if err == nil {
		t.Fatalf("an outdated sbx must refuse the run, got success:\n%s", out)
	}
	if !strings.Contains(out, "proveo runs agents only in sbx") || !strings.Contains(out, sbx.MinVersion) {
		t.Errorf("the refusal must say sbx is the only backend and name the version proveo targets (%s), got:\n%s", sbx.MinVersion, out)
	}
	if strings.Contains(out, "docker run") {
		t.Errorf("a refused run rendered a docker argv:\n%s", out)
	}
}

func TestSandboxBackendOffersInstallOrUpgradeToMatch(t *testing.T) {
	if sbx.InstallCmd(false) == "" {
		t.Skip("no sbx install route on this platform")
	}

	absent, _ := printOnlyRunErr(t, t.TempDir(), pathWithoutSbx(t), "claudecode")
	if want := sbx.InstallCmd(false); !strings.Contains(absent, want) {
		t.Errorf("with no sbx on PATH, want the install line %q, got:\n%s", want, absent)
	}

	outdated, _ := printOnlyRunErr(t, t.TempDir(), pathWithFakeSbx(t, "0.1.0"), "claudecode")
	if want := sbx.InstallCmd(true); !strings.Contains(outdated, want) {
		t.Errorf("with an outdated sbx, want the upgrade line %q, got:\n%s", want, outdated)
	}
}

func TestSandboxBackendPrintOnlyInstallsNothing(t *testing.T) {
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

	_, _ = printOnlyRunErr(t, t.TempDir(), env, "claudecode")
	if _, err := os.Stat(marker); err == nil {
		t.Error("--print attempted an install; a dry run must only report")
	}
}

func printOnlyRun(t *testing.T, workdir string, extraEnv []string, target string) string {
	t.Helper()
	out, err := printOnlyRunErr(t, workdir, extraEnv, target)
	if err != nil {
		t.Fatalf("proveo run %s --print-only: %v\n%s", target, err, out)
	}
	return out
}

// printOnlyRunErr is printOnlyRun for runs that may refuse: it returns the output and the error.
func printOnlyRunErr(t *testing.T, workdir string, extraEnv []string, target string) (string, error) {
	t.Helper()
	proveoBin := buildProveo(t)
	cmd := exec.Command(proveoBin, "run", target, "--print")
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

var sandboxHarnesses = dockerTargets(func(manifest.Manifest) bool { return true })

// daemonHarnesses are the sbx harnesses whose agent is promised a daemon.
var daemonHarnesses = dockerTargets(manifest.Manifest.WantsDocker)

func dockerTargets(pick func(manifest.Manifest) bool) []string {
	wd, err := os.Getwd()
	if err != nil {
		panic("sbx suite: cwd: " + err.Error())
	}
	ms, err := manifest.Load(filepath.Join(wd, "..", "defs"))
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

func TestSandboxBackendRefusesWhenSbxAbsent(t *testing.T) {
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			out, err := printOnlyRunErr(t, t.TempDir(), pathWithoutSbx(t), target)
			if err == nil || !strings.Contains(out, "proveo runs agents only in sbx") {
				t.Errorf("with no sbx, want a refusal naming sbx as the only backend, got err=%v:\n%s", err, out)
			}
			if strings.Contains(out, "docker run") || strings.Contains(out, "falling back") {
				t.Errorf("a refused run must not render or promise a docker fallback:\n%s", out)
			}
		})
	}
}

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

			wantKind := "mixin"
			if sbx.DeclaresOwnAgent(target) {
				wantKind = "sandbox" // no built-in sbx agent to mix into; the kit names its own
			}
			if kit.Kind != wantKind {
				t.Errorf("%s: kind=%q, want %q\n%s", target, kit.Kind, wantKind, raw)
			}
			// SPEC-v2 types this as a string. An int is normalised on the way in, so
			// the mistake survives validate and only shows up against a stricter reader.
			if kit.SchemaVersion != "2" {
				t.Errorf("%s: schemaVersion=%q, want the string \"2\"\n%s",
					target, kit.SchemaVersion, raw)
			}
			if !sbx.DeclaresOwnAgent(target) {
				for _, c := range kit.Credentials {
					t.Errorf("%s: mixin declares credential %q — the built-in agent owns "+
						"credentials, and declaring one twice is refused as \"defined in both\"\n%s",
						target, c.Service, raw)
				}
			}
			if builtin := sbx.BuiltinAgent(target); builtin != "" && kit.Name == builtin {
				t.Errorf("%s: Kit name %q shadows the built-in agent; sbx refuses that outright",
					target, kit.Name)
			}

			// The seed is the file-shaped half of setup, and it must run as the agent
			// user or it composes into a home the agent never reads.
			var seeded bool
			for _, st := range kit.Setup.Startup {
				if sbx.CommandNamesSeed(st.Command) {
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
			// into a process the agent never inherits.
			for _, k := range []string{"PROVEO_WORKDIR", sbx.StateHomeVar} {
				if kit.Environment.Variables[k] == "" {
					t.Errorf("%s: Kit environment omits %s\n%s", target, k, raw)
				}
			}
			if v := kit.Environment.Variables[sbx.StateHomeVar]; v != "" && !filepath.IsAbs(v) {
				t.Errorf("%s: %s=%q is not an absolute host path\n%s", target, sbx.StateHomeVar, v, raw)
			}
			for _, k := range []string{"HOME", "PROVEO_HOME"} {
				if v, ok := kit.Environment.Variables[k]; ok {
					t.Errorf("%s: Kit sets %s=%q — the HOME redirect is deliberately deleted on "+
						"the sbx backend; reinstating it orphans the proxy-written credential\n%s",
						target, k, v, raw)
				}
			}
		})
	}
}

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

func brokenPrompt(screen string) string {
	for _, want := range []string{
		"Overwrite? (y/N)",
		"Delete selected secret? (y/N)",
		"does not exist. Would you like to create it?",
	} {
		if strings.Contains(screen, want) {
			return want
		}
	}
	return ""
}

// consentPrompt is the services sbx asks the operator to approve when screen shows its
// credential prompt; a prompt without sbx's frame names "its credentials".
func consentPrompt(screen string) (string, bool) {
	lines := strings.Split(screen, "\n")
	at := -1
	for i, l := range lines {
		if strings.Contains(l, "[A]pprove all") {
			at = i
		}
	}
	if at < 0 {
		return "", false
	}
	var services []string
	for i := at - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "This kit wants to use these credentials") {
			return strings.Join(services, ", "), true
		}
		if f := strings.Fields(l); len(f) > 0 {
			services = append([]string{f[0]}, services...)
		}
	}
	return "its credentials", true
}

// consentFailure is how a leg fails when sbx asks consent for services: the e2e never approves.
func consentFailure(services, target string) string {
	return fmt.Sprintf("sbx asks to approve %s; approve once with an interactive `proveo run %s`, then rerun — the e2e never approves a credential binding", services, target)
}

func renderKit(t *testing.T, target string) string {
	t.Helper()
	proveoBin := buildProveo(t)
	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", ".")
	state := t.TempDir()

	cmd := exec.Command(proveoBin, "run", target, "--print", "--input", work)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(),
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_STATE_ROOT="+state,
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

func TestEveryHarnessGetsTheDockerAccessItPromises(t *testing.T) {
	requireTmux(t)

	targets := append([]string{}, daemonHarnesses...)
	if len(targets) == 0 {
		t.Fatal("no def promises a docker daemon — the matrix cannot be empty")
	}
	sort.Strings(targets)

	sbxOK, sbxWhy := sbx.Available()
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			if !sbxOK {
				t.Skipf("sbx not available on this host: %s", sbxWhy)
			}
			sandboxBoundaryProbe(t, target)
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

func assertDockerServerReachable(t *testing.T, target, how, got string) {
	t.Helper()
	low := strings.ToLower(got)
	switch {
	case strings.Contains(low, "command not found"), strings.Contains(low, "executable file not found"):
		t.Fatalf("%s declares docker: %s but no docker client is present: %q\n"+
			"the image ships none by design (the static tarball overlapped sbx), so this means "+
			"sbx supplied no client either — fix it sbx-side or stop declaring docker: %s\n"+
			"see _spec/_plans/image-size-reduction.puml", target, how, got, how)
	case strings.Contains(low, "cannot connect to the docker daemon"),
		strings.Contains(low, "is the docker daemon running"),
		strings.Contains(low, "permission denied"):
		t.Fatalf("%s: the docker client is present but docker: %s exposed no usable daemon: %q", target, how, got)
	case !dockerVersionish(got):
		t.Fatalf("%s: `docker version` returned %q, want a server version", target, got)
	}
	t.Logf("%s: docker server reached via %s = %s", target, how, got)
}

// assertSandboxSuppliesDocker fails unless the probe reached a docker server in the sandbox.
// SPEC: _spec/_paradigms/retire-dind.puml
func assertSandboxSuppliesDocker(t *testing.T, target, got string) {
	t.Helper()
	_, server, ok := strings.Cut(got, "SERVER:")
	if !ok {
		t.Errorf("%s: the docker probe wrote no SERVER: line, so nothing was actually tested: %q", target, got)
		return
	}
	assertDockerServerReachable(t, target, "sbx", strings.TrimSpace(strings.SplitN(server, "\n", 2)[0]))
	t.Logf("%s: the sandbox supplies docker: %s", target, strings.Join(strings.Fields(got), " · "))
}

func sandboxBoundaryProbe(t *testing.T, target string) {
	t.Helper()
	requireSbx(t)
	requireHarnessCredential(t, target)
	proveoBin := buildProveo(t)

	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "config", "user.email", "e2e@proveo.test")
	mustRun(t, work, "git", "config", "user.name", "proveo e2e")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("boundary probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "add", "-A")
	mustRun(t, work, "git", "commit", "-qm", "seed")
	hostHead := strings.TrimSpace(mustOutput(t, work, "git", "rev-parse", "HEAD"))

	before, canList := sbxSandboxNames()
	if !canList {
		t.Logf("`%s ls` unavailable on this CLI — claim 4 (teardown) will not be checked", sbx.Binary)
	}

	sess := tmux.New(fmt.Sprintf("proveo-sbx-%s-%d", target, os.Getpid()), nil)
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
	cmd = append(cmd,
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=false",
		proveoBin, "run", target, "--input", work,
	)
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start sandbox session: %v", err)
	}

	w := newWatcher(t, sess)

	w.until("the sbx backend line", 2*time.Minute, func() bool {
		s := w.Screen()
		if strings.Contains(s, "docker sandbox: off") {
			w.Fatalf("%s ran with the sandbox add-on unchecked — a remembered choice leaked into this run", target)
		}
		return strings.Contains(s, "backend: docker sandboxes (sbx)")
	})

	k := awaitKit(t, sess, before, target, work)

	const mark = "SBX-MOUNT-OK"
	probe := "{ ls /var/run/docker.sock >/dev/null 2>&1 && echo HAS-SOCKET || echo NO-SOCKET; " +
		"printf SERVER:; timeout 20 docker version --format '{{.Server.Version}}' 2>/dev/null; echo; }; " +
		"printf 'AGENT_HEAD=%s\\n' \"$(git rev-parse HEAD 2>/dev/null || echo none)\"; " +
		"printf %s " + mark + " > " + probeMount
	pane, status := k.exec(probe, 3*time.Minute)
	if status != 0 {
		t.Fatalf("%s: the boundary probe exited %d\n--- pane ---\n%s", target, status, pane)
	}

	// Claim 2 — the workspace. On the default (clone) plan nothing the agent writes
	// reaches the host until teardown, so the reachable claim is that the tree it
	// stands in is THIS workspace: same commit, from the run's own git daemon. Only
	// a live bind can be asserted by a file arriving host-side.
	if agentHead := paneValue(pane, "AGENT_HEAD="); agentHead != hostHead {
		t.Errorf("%s: the sandbox stands on commit %q, want the workspace's %q — "+
			"the clone did not come from this checkout", target, agentHead, hostHead)
	}
	if screen, _ := sess.CaptureAll(); !strings.Contains(screen, "private clone") {
		w.until("the sandbox to write through the workspace mount", 2*time.Minute, func() bool {
			return strings.Contains(readIn(work, probeMount), mark)
		})
	}

	// Claim 3 — the daemon.
	assertSandboxSuppliesDocker(t, target, probeReport(pane))

	// Claim 4 — teardown. SIGTERM rather than killing the pane, so the run's own
	// `sbx rm` (VM + images + volumes) is what gets exercised.
	screen, exited := k.end()
	if !exited {
		t.Errorf("%s: proveo did not finish its teardown after SIGTERM\n%s", target, screen)
	}
	if !canList {
		return
	}
	retained := strings.Contains(screen, "retained") && strings.Contains(screen, k.name)
	var leaked []string
	deadline := time.Now().Add(60 * time.Second)
	for {
		leaked = slices.DeleteFunc(newSandboxes(before), func(n string) bool { return retained && n == k.name })
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

const probeMount = "SBX_MOUNT.txt"

// SPEC: _spec/_experiments/docker-sandbox.puml
func probeReport(pane string) string {
	var keep []string
	for _, line := range strings.Split(pane, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "HAS-SOCKET", line == "NO-SOCKET":
			keep = append(keep, line)
		case strings.HasPrefix(line, "SERVER:") && !strings.Contains(line, "printf"):
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

func paneValue(pane, key string) string {
	for _, line := range strings.Split(pane, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, key) || strings.Contains(line, "rev-parse") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, key))
	}
	return ""
}

// dockerVersionish reports whether s looks like a docker server version.
func dockerVersionish(s string) bool {
	return regexp.MustCompile(`^\d+\.\d+`).MatchString(s)
}

// sbxSandboxNames lists the sandboxes a test could have made: those whose
// primary workspace lies under the test process's temp root. An operator's
// sandbox never qualifies, so no cleanup built on this can remove one.
func sbxSandboxNames() (map[string]bool, bool) {
	out, err := exec.Command(sbx.Binary, "ls", "--json").Output()
	if err != nil {
		return nil, false
	}
	var listing struct {
		Sandboxes []struct {
			Name       string   `json:"name"`
			Workspaces []string `json:"workspaces"`
		} `json:"sandboxes"`
	}
	if json.Unmarshal(out, &listing) != nil {
		return nil, false
	}
	names := map[string]bool{}
	for _, sb := range listing.Sandboxes {
		if strings.HasPrefix(sb.Name, sbx.NamePrefix) && len(sb.Workspaces) > 0 && underTestTemp(sb.Workspaces[0]) {
			names[sb.Name] = true
		}
	}
	return names, true
}

func underTestTemp(path string) bool {
	for _, root := range testTempRoots() {
		if rel, err := filepath.Rel(root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func testTempRoots() []string {
	root := filepath.Clean(os.TempDir())
	roots := []string{root}
	if r, err := filepath.EvalSymlinks(root); err == nil && r != root {
		roots = append(roots, r)
	}
	return roots
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

func TestSandboxBackendPublishesStateHomeAndNoRedirect(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sbx not available on this host")
	}
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			out := printOnlyRun(t, t.TempDir(), nil, target)
			state := envValueInArgv(out, sbx.StateHomeVar)
			if state == "" {
				t.Errorf("sbx argv sets no %s — the agent's transcripts land in volumes "+
					"teardown removes, so `--resume` has nothing to offer:\n%s", sbx.StateHomeVar, out)
			} else if !filepath.IsAbs(state) {
				t.Errorf("%s=%q is not an absolute host path:\n%s", sbx.StateHomeVar, state, out)
			}
			// The guard: neither name may come back by accident.
			for _, k := range []string{"HOME", "PROVEO_HOME"} {
				if v := envValueInArgv(out, k); v != "" {
					t.Errorf("sbx argv sets %s=%q — the redirect is retired on this backend "+
						"because it orphaned the proxy-written credential:\n%s", k, v, out)
				}
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

// TestSweepNeverClaimsAnOperatorSandbox pins the ownership rule: a sandbox
// the operator starts while the suite runs is not "new since the test began".
func TestSweepNeverClaimsAnOperatorSandbox(t *testing.T) {
	t.Parallel()
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, "Projects", "pluvo", "worktrees", "dev"), "/Users/op/repo", "/"} {
		if underTestTemp(p) {
			t.Errorf("%s counts as test-owned; the sweep would `sbx rm --force` the operator's sandbox on it", p)
		}
	}
	tmp := t.TempDir()
	if !underTestTemp(tmp) {
		t.Errorf("%s is a test workspace and must be swept", tmp)
	}
	if r, err := filepath.EvalSymlinks(tmp); err == nil && !underTestTemp(r) {
		t.Errorf("%s — sbx reports resolved paths (/private/var/…), which must still count", r)
	}
}
