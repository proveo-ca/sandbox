//go:build e2e

// SPEC: _spec/internal/sbx/sandbox-backend.puml, _spec/_paradigms/retire-dind.puml, _spec/_devops/image-size-reduction.puml
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

func TestSandboxBackendCreatesFromItsOwnWorkloadDirectory(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sbx not available on this host")
	}
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			out := printOnlyRun(t, t.TempDir(), nil, target)
			kit := printedKitDir(t, out)
			argv := shellWords(printedLine(t, out, "# sandbox"))
			if !slices.Contains(argv, kit) {
				t.Errorf("sbx create does not name the workload %s positionally:\n%v", kit, argv)
			}
			for _, v2 := range []string{"--kit", "-t"} {
				if slices.Contains(argv, v2) {
					t.Errorf("sbx create carries %s — the v2 kit path; a v3 workload is positional and its recipe names the image:\n%v", v2, argv)
				}
			}
			agent := filepath.Base(kit)
			for _, f := range []string{agent + ".yaml", agent + ".dockerfile"} {
				if _, err := os.Stat(filepath.Join(kit, f)); err != nil {
					t.Errorf("workload lacks %s: %v", f, err)
				}
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

func TestSandboxWorkloadValidatesAgainstTheRealCLI(t *testing.T) {
	if ok, why := sbx.Available(); !ok {
		t.Skipf("sbx not available on this host: %s", why)
	}
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			kitDir, _ := renderWorkload(t, target)
			out, err := exec.Command(sbx.Binary, "kit", "validate", kitDir).CombinedOutput()
			if strings.Contains(string(out), "no kit builder configured") {
				t.Skipf("this sbx validates v3 source kits only through a kit builder (`sbx kit builder`):\n%s", out)
			}
			desc, _ := os.ReadFile(filepath.Join(kitDir, filepath.Base(kitDir)+".yaml"))
			if err != nil {
				t.Fatalf("%s: sbx rejected the workload proveo writes: %v\n--- sbx ---\n%s\n--- descriptor ---\n%s",
					target, err, out, desc)
			}
			if strings.Contains(string(out), "deprecated") {
				t.Errorf("%s: the workload uses a deprecated field — the schema has moved:\n%s\n--- descriptor ---\n%s",
					target, out, desc)
			}
		})
	}
}

func TestSandboxWorkloadHasTheV3Shape(t *testing.T) {
	if ok, why := sbx.Available(); !ok {
		t.Skipf("sbx not available on this host: %s", why)
	}
	for _, target := range sandboxHarnesses {
		t.Run(target, func(t *testing.T) {
			kitDir, printed := renderWorkload(t, target)
			agent := filepath.Base(kitDir)
			raw, err := os.ReadFile(filepath.Join(kitDir, agent+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(raw), sbx.KitSyntaxV3+"\n") {
				t.Errorf("%s: descriptor does not open with %q — sbx then reads it as v2\n%s", target, sbx.KitSyntaxV3, raw)
			}
			var w struct {
				SchemaVersion string `yaml:"schemaVersion"`
				Kind          string `yaml:"kind"`
				Capabilities  []struct {
					Type   string         `yaml:"type"`
					Config map[string]any `yaml:"config"`
				} `yaml:"capabilities"`
			}
			if err := yaml.Unmarshal(raw, &w); err != nil {
				t.Fatalf("%s: descriptor is not parseable YAML: %v\n%s", target, err, raw)
			}
			if w.SchemaVersion != sbx.KitSchemaVersionV3 || w.Kind != "workload" {
				t.Errorf("%s: schemaVersion=%q kind=%q, want %q workload\n%s", target, w.SchemaVersion, w.Kind, sbx.KitSchemaVersionV3, raw)
			}
			if agent != sbx.AgentName(target) || slices.Contains(sbx.BuiltinAgents, agent) {
				t.Errorf("%s: workload dir %q; sbx registers the dir name, and a built-in name selects v2", target, agent)
			}
			if len(w.Capabilities) == 0 || w.Capabilities[0].Type != sbx.CapSbx {
				t.Errorf("%s: sbx@1 is not the first capability — without it the image ENTRYPOINT is PID 1\n%s", target, raw)
			}
			services := map[string]int{}
			var seed map[string]any
			for _, c := range w.Capabilities {
				switch c.Type {
				case sbx.CapCredential:
					svc, _ := c.Config["service"].(string)
					services[svc]++
				case sbx.CapLifecycle:
					hooks, _ := c.Config["startup"].([]any)
					for _, h := range hooks {
						hm, _ := h.(map[string]any)
						if argv, ok := hm["command"].([]any); ok && sbx.CommandNamesSeed(anyStrings(argv)) {
							seed = hm
						}
					}
				}
			}
			for svc, n := range services {
				if n > 1 {
					t.Errorf("%s: credential %q declared %d times — one owner per service\n%s", target, svc, n, raw)
				}
			}
			if seed == nil {
				t.Fatalf("%s: no proveo seed in lifecycle startup — subagents, settings and workspace trust "+
					"would never be composed\n%s", target, raw)
			}
			if seed["user"] != "1000" {
				t.Errorf("%s: seed runs as user %v, want the agent's 1000", target, seed["user"])
			}

			// Env-shaped work rides `-e` on create, and the seed hook names what it reads.
			create := printedLine(t, printed, "# sandbox")
			declared := anyStrings(asSlice(seed["env"]))
			for _, k := range []string{"PROVEO_WORKDIR", sbx.StateHomeVar} {
				v, ok := envValInCmd(create, k)
				if !ok || v == "" {
					t.Errorf("%s: sbx create omits -e %s\n%s", target, k, create)
				}
				if !slices.Contains(declared, k) {
					t.Errorf("%s: the seed hook does not declare %s; a hook sees only the env it names", target, k)
				}
			}
			if v, _ := envValInCmd(create, sbx.StateHomeVar); v != "" && !filepath.IsAbs(v) {
				t.Errorf("%s: %s=%q is not an absolute host path", target, sbx.StateHomeVar, v)
			}
			for _, k := range []string{"HOME", "PROVEO_HOME"} {
				if v, ok := envValInCmd(create, k); ok {
					t.Errorf("%s: sbx create sets %s=%q — the HOME redirect orphans the proxy-written credential", target, k, v)
				}
			}
		})
	}
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func anyStrings(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
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
	at, numbered := -1, false
	for i, l := range lines {
		if strings.Contains(l, "[A]pprove all") || strings.Contains(l, "Approve these credentials?") {
			at, numbered = i, strings.Contains(l, "Approve these credentials?")
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
		f := strings.Fields(l)
		switch {
		case len(f) == 0:
		case numbered && strings.HasSuffix(f[0], ":"): // sbx 0.49: "openai: API key sent to api.openai.com"
			services = append([]string{strings.TrimSuffix(f[0], ":")}, services...)
		case !numbered: // before 0.49: "opencode     API key → sent to .opencode.ai"
			services = append([]string{f[0]}, services...)
		}
	}
	return "its credentials", true
}

// consentFailure is how a leg fails when sbx asks consent for services: the e2e never approves.
func consentFailure(services, target string) string {
	return fmt.Sprintf("sbx asks to approve %s; approve once with an interactive `proveo run %s`, then rerun — the e2e never approves a credential binding", services, target)
}

// renderWorkload runs `proveo run <target> --print` and returns the workload directory and the printed output.
func renderWorkload(t *testing.T, target string) (string, string) {
	t.Helper()
	proveoBin := buildProveo(t)
	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", ".")

	cmd := exec.Command(proveoBin, "run", target, "--print", "--input", work)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(),
		"PROVEO_HOME="+t.TempDir(),
		"PROVEO_STATE_ROOT="+t.TempDir(),
		"PROVEO_WIZARD=off",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo run %s --print: %v\n%s", target, err, out)
	}
	return printedKitDir(t, string(out)), string(out)
}

// printedMounts is the host side of every workspace mount in a printed `sbx create` argv.
func printedMounts(create []string) []string {
	valued := map[string]bool{"--name": true, "-t": true, "-m": true, "--cpus": true, "-p": true, "-e": true, "--env-file": true}
	var positional []string
	for i := 1; i < len(create) && create[i] != "--"; i++ {
		switch a := create[i]; {
		case valued[a]:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			positional = append(positional, strings.TrimSuffix(a, ":ro"))
		}
	}
	if len(positional) == 0 {
		return nil
	}
	return positional[1:]
}

func TestEveryMountThePrintedCreateLineNamesExists(t *testing.T) {
	for _, target := range dockerTargets(func(m manifest.Manifest) bool { return len(m.Home.Files) > 0 }) {
		t.Run(target, func(t *testing.T) {
			_, out := renderWorkload(t, target)
			create := createArgv(printedLine(t, out, "# sandbox"))
			mounts := printedMounts(create)
			if len(mounts) == 0 {
				t.Fatalf("the printed create line mounts nothing: %q", create)
			}
			for _, m := range mounts {
				if _, err := os.Stat(m); err != nil {
					t.Errorf("the printed create line mounts %s, which --print left missing — sbx stops at \"The selected workspace does not exist\"", m)
				}
			}
		})
	}
}

func TestPrintedMountsSkipsFlagValuesAndTheKit(t *testing.T) {
	t.Parallel()
	create := []string{"create", "--name", "n", "-t", "img", "--clone", "-e", "A=/x", "/kit", "/work", "/home-files:ro", "--", "/cmd"}
	if got, want := printedMounts(create), []string{"/work", "/home-files"}; !slices.Equal(got, want) {
		t.Errorf("printedMounts = %q, want %q", got, want)
	}
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
		t.Fatalf("%s is promised a daemon via %s but no docker client is present: %q\n"+
			"sbx supplied no client — fix it sbx-side or set `daemon: false`\n"+
			"see _spec/_devops/image-size-reduction.puml", target, how, got)
	case strings.Contains(low, "cannot connect to the docker daemon"),
		strings.Contains(low, "is the docker daemon running"),
		strings.Contains(low, "permission denied"):
		t.Fatalf("%s: the docker client is present but %s exposed no usable daemon: %q", target, how, got)
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

	// Claim 2 — the workspace: the sandbox stands on this checkout's commit.
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
type listedSandbox struct {
	Name       string   `json:"name"`
	Workspaces []string `json:"workspaces"`
}

// sbxListing reads `sbx ls --json`: a bare array since sbx 0.48, {"sandboxes": [...]} before it.
func sbxListing(out []byte) ([]listedSandbox, error) {
	var bare []listedSandbox
	if err := json.Unmarshal(out, &bare); err == nil {
		return bare, nil
	}
	var wrapped struct {
		Sandboxes []listedSandbox `json:"sandboxes"`
	}
	if err := json.Unmarshal(out, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Sandboxes, nil
}

func TestSbxListingReadsTheInstalledSbx(t *testing.T) {
	requireSbx(t)
	out, err := exec.Command(sbx.Binary, "ls", "--json").Output()
	if err != nil {
		t.Skipf("sbx ls --json: %v", err)
	}
	if _, err := sbxListing(out); err != nil {
		t.Errorf("sbx ls --json changed shape (%v); every sandbox wait and sweep reads it:\n%.400s", err, out)
	}
}

func sbxSandboxNames() (map[string]bool, bool) {
	out, err := exec.Command(sbx.Binary, "ls", "--json").Output()
	if err != nil {
		return nil, false
	}
	listing, err := sbxListing(out)
	if err != nil {
		return nil, false
	}
	names := map[string]bool{}
	for _, sb := range listing {
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
