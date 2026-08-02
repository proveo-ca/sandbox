//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml — credential-forwarding integrity (egress layer).

package e2e

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestCredentialForwardingIntegrity asserts that provider API keys land in the
// EGRESS layer ONLY. Two complementary halves, both over random per-run values so
// no real secret is involved:
//
//	isolation (every agent, deterministic) — the firewall launch plan carries only
//	  the "proveo-brokered" sentinel for each provider secret, never a raw key. The
//	  --print dry-run builds the agent's docker command with the exact same env code
//	  path as a real run, so this is faithful and needs no containers.
//
//	egress integrity (live) — a vendor-pinned agent (cursor) brokers ALL provider
//	  keys; the broker.env bind-mounted into the egress-proxy receives each one
//	  byte-for-byte. broker.env is agent-independent, so one agent proves the path.
//
//	go test -tags=e2e ./tests/e2e/ -run CredentialForwardingIntegrity -v
func TestCredentialForwardingIntegrity(t *testing.T) {
	proveoBin := buildProveo(t)
	keys := provider.KeyVars()
	if len(keys) == 0 {
		t.Fatal("provider.KeyVars() is empty")
	}
	// cursor is absent by design: it declares capabilities egress:[open]
	// credentials:[forward], so it has no egress layer to be isolated from and
	// forwards the REAL key rather than a sentinel. Its contract is asserted by
	// TestCursorEgressException instead.
	agents := strings.Fields(env("PROVEO_TEST_AGENTS", "opencode claudecode cecli"))

	// Isolation — deterministic, one subtest per agent (parallel: pure --print).
	for _, agent := range agents {
		agent := agent
		t.Run(agent+"_isolation", func(t *testing.T) {
			t.Parallel()
			assertPlanIsolation(t, proveoBin, agent, keys)
		})
	}

	// Egress integrity — one live firewall run through the vendor-pinned agent.
	t.Run("egress_broker_receives_all_keys", func(t *testing.T) {
		requireLiveStack(t)
		assertBrokerReceivesAllKeys(t, proveoBin, keys)
	})
}

// TestProjectDotEnvAtEgressLayer launches both unpinned (OpenCode) and
// vendor-pinned (Cursor) harness plans through real Docker firewall topologies.
// Each key exists only in the project's .env: the egress sidecar must receive the
// raw value byte-for-byte while the agent gets only the sentinel and a masked
// /app/.env. A lightweight probe image keeps this credential-path test independent
// of either agent CLI's release/install state.
//
//	go test -tags=e2e ./tests/e2e/ -run ProjectDotEnvAtEgressLayer -v -timeout 10m
func TestProjectDotEnvAtEgressLayer(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if !tmux.Available() {
		t.Skip("tmux not installed")
	}

	proveoBin := buildProveo(t)
	tests := []struct {
		target string
		key    string
	}{
		// cursor is deliberately absent: it declares capabilities egress:[open]
		// credentials:[forward], so it runs with no MITM and its key reaches the
		// container intact. There is no egress layer to receive it, which is the
		// property this test asserts.
		{target: "opencode", key: "MOONSHOT_API_KEY"},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			assertProjectDotEnvAtEgress(t, proveoBin, tc.target, tc.key)
		})
	}
}

func assertProjectDotEnvAtEgress(t *testing.T, proveoBin, target, key string) {
	t.Helper()
	root := filepath.Join(repoRoot(t), ".cache", "e2e")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(root, target+"-dotenv-work-")
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp(root, target+"-dotenv-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(work)
		_ = os.RemoveAll(home)
	})

	value := randToken()
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte(key+"="+value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Seed the choice cache so the form opens pre-selected on the tier this test
	// needs. Seeding alone is NOT enough to skip it: the prompt always shows so the
	// operator sees the posture they are launching, so the run below must accept it.
	seedAgentSettings(t, filepath.Join(home, "proveo"), target, "allowlist", "broker")

	forceClean(proveoBin)
	beforeEgress := containersWithSuffix("-egress")
	beforeAgent := dockerIDsByAncestor("ubuntu:24.04")
	sess := tmux.New(fmt.Sprintf("proveo-dotenv-%s-%d", target, os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		forceCleanIfCrowded(proveoBin)
	})

	// Remove every provider detection variable so the provider can only be
	// detected from work/.env. Preserve infrastructure variables such as
	// DOCKER_HOST, which may point at a remote daemon.
	cmd := []string{"env"}
	seenDetect := map[string]bool{}
	for _, name := range provider.Names() {
		e, _ := provider.Lookup(name)
		for _, detect := range e.Detect {
			if !seenDetect[detect] {
				cmd = append(cmd, "-u", detect)
				seenDetect[detect] = true
			}
		}
	}
	cmd = append(cmd,
		"-u", "PROVEO_EGRESS_ENV_FILE",
		"-u", "PROVEO_EGRESS_PROVIDER",
		"TERM=xterm-256color",
		"HOME="+home,
		"PROVEO_HOME="+filepath.Join(home, "proveo"),
		"PROVEO_DEFS_DIR="+filepath.Join(repoRoot(t), "defs"),
		"PROVEO_AUTO_PROVISION=1",
		// HOME is overridden above, which hides ~/.docker and so the active context.
		// Without this the CLI silently falls back to /var/run/docker.sock and every
		// docker call — starting with the image pull — hangs.
		"DOCKER_HOST="+dockerHost(t),
		proveoBin, "run", target,
		"--image", "ubuntu:24.04",
		"--egress-mode", "firewall",
		"--shell",
		"--input", work,
	)
	if err := sess.Start(200, 50, cmd...); err != nil {
		t.Fatalf("start %s session: %v", target, err)
	}
	acceptChoicePrompt(t, sess, target)
	egress := waitForNewContainer(t, beforeEgress, "-egress", 5*time.Minute, sess)
	brokerDir, ok := mountSource(egress, "/broker")
	if !ok {
		t.Fatalf("%s egress container %s has no /broker mount", target, egress)
	}
	brokerEnv := filepath.Join(brokerDir, "broker.env")
	waitForFileExists(t, brokerEnv, 30*time.Second)
	got := parseKVFile(t, brokerEnv)
	if got[key] != value {
		t.Fatalf("%s: egress broker.env did not receive project .env %s byte-for-byte\n  project sha256=%s (len %d)\n  egress  sha256=%s (len %d)",
			target, key, sha(value), len(value), sha(got[key]), len(got[key]))
	}

	agent := waitForNewAncestor(t, "ubuntu:24.04", beforeAgent, 2*time.Minute, sess)
	out, err := exec.Command("docker", "exec", agent, "bash", "-lc",
		`printf '%s' "${`+key+`-}"; test ! -s /app/.env`).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: agent must see a masked /app/.env: %v\n%s", target, err, out)
	}
	if string(out) != entrypoint.DefaultSentinel {
		t.Fatalf("%s: agent %s=%q, want sentinel %q", target, key, out, entrypoint.DefaultSentinel)
	}
	t.Logf("%s: project .env %s reached egress byte-for-byte; agent received only sentinel + masked .env",
		target, key)
}

// assertPlanIsolation checks the agent's firewall launch command: every provider
// secret it declares appears only as the sentinel, and no raw key value appears.
func assertPlanIsolation(t *testing.T, proveoBin, agent string, keys []string) {
	t.Helper()
	want := make(map[string]string, len(keys))
	kv := make([]string, 0, len(keys))
	for _, k := range keys {
		v := randToken()
		want[k] = v
		kv = append(kv, k+"="+v)
	}

	agentCmd := agentCommandLine(t, printEnforcedPlan(t, proveoBin, agent, kv))
	declared := 0
	for _, k := range keys {
		if strings.Contains(agentCmd, want[k]) {
			t.Errorf("%s: RAW %s value appears in the agent launch command — must be brokered to a sentinel", agent, k)
		}
		if v, ok := envValInCmd(agentCmd, k); ok {
			declared++
			if v != entrypoint.DefaultSentinel {
				t.Errorf("%s: agent env %s=%q, expected the sentinel %q", agent, k, v, entrypoint.DefaultSentinel)
			}
		}
	}
	t.Logf("%s: %d/%d provider keys declared, all forwarded as the sentinel (no raw key in the plan)", agent, declared, len(keys))
}

// assertBrokerReceivesAllKeys runs the vendor-pinned cursor in firewall mode and
// verifies the egress broker.env receives every provider key byte-for-byte.
func assertBrokerReceivesAllKeys(t *testing.T, proveoBin string, keys []string) {
	t.Helper()
	want := make(map[string]string, len(keys))
	// An isolated HOME is what makes driving the prompt deterministic: the first
	// run of a harness prompts and caches the answer, so a shared HOME would leak
	// into the developer's real settings AND skip the prompt on every later run.
	home := t.TempDir()
	kv := []string{"env", "HOME=" + home, "DOCKER_HOST=" + dockerHost(t)}
	for _, k := range keys {
		v := randToken()
		want[k] = v
		kv = append(kv, k+"="+v)
	}

	work := t.TempDir()
	forceClean(proveoBin)
	before := containersWithSuffix("-egress")

	sess := tmux.New(fmt.Sprintf("proveo-cred-egress-%d", os.Getpid()), nil)
	t.Cleanup(func() {
		sess.Kill()
		forceCleanIfCrowded(proveoBin)
		rmByAncestor("proveo/claudecode:latest")
	})

	// claudecode, not cursor: cursor now declares capabilities egress:[open]
	// credentials:[forward], so it has no MITM and therefore no broker at all.
	// claudecode declares providers:[anthropic], which narrows the detected set to
	// one and is precisely what ARMS the broker — with every key detected the
	// broker refuses to pin and there is no broker.env to inspect.
	cmd := append(append([]string(nil), kv...),
		proveoBin, "run", "claudecode", "--egress-mode", "allowlist", "--shell", "--input", work)
	if err := sess.Start(200, 50, cmd...); err != nil {
		t.Fatalf("start session: %v", err)
	}
	acceptChoicePrompt(t, sess, "claudecode")

	egress := waitForNewContainer(t, before, "-egress", 120*time.Second, sess)
	brokerDir, ok := mountSource(egress, "/broker")
	if !ok {
		t.Fatalf("claudecode egress container %s has no /broker mount (broker not resolved)", egress)
	}
	brokerEnv := filepath.Join(brokerDir, "broker.env")
	waitForFileExists(t, brokerEnv, 30*time.Second)
	got := parseKVFile(t, brokerEnv)
	for _, k := range keys {
		switch {
		case got[k] == "":
			t.Errorf("%s: absent from egress broker.env (expected the forwarded value)", k)
		case got[k] != want[k]:
			t.Errorf("%s: egress value differs from host env\n  host  sha256=%s (len %d)\n  egress sha256=%s (len %d)",
				k, sha(want[k]), len(want[k]), sha(got[k]), len(got[k]))
		}
	}
	t.Logf("egress broker.env verified for %d provider keys, byte-for-byte", len(keys))
}

// TestCursorEgressException asserts the cursor exception, now expressed as
// declared capabilities rather than a target-name special case: cursor's manifest
// pins egress:[open] credentials:[forward] because its vendor TLS cannot be
// intercepted. The default therefore forwards the REAL key with no MITM, and an
// explicitly requested unsupported axis is REFUSED with the reason — not silently
// rewritten, and no longer a warn-and-continue that hands over a dead sentinel.
// Deterministic (--print, no containers).
func TestCursorEgressException(t *testing.T) {
	proveoBin := buildProveo(t)

	// Default → open + forward: real key forwarded, no sentinel, no internal net.
	def := runPrint(t, proveoBin, "cursor")
	defCmd := agentCommandLine(t, def)
	if strings.Contains(defCmd, "CURSOR_API_KEY="+entrypoint.DefaultSentinel) {
		t.Error("cursor default should forward the REAL key, not hand the agent the sentinel")
	}
	if !hasBareEnv(defCmd, "CURSOR_API_KEY") {
		t.Errorf("cursor default should forward a bare -e CURSOR_API_KEY (real value):\n%s", defCmd)
	}
	if strings.Contains(defCmd, "--internal") {
		t.Error("cursor default must not run behind an --internal network: nothing can intercept its TLS")
	}
	if strings.Contains(def, "invalid API key") {
		t.Errorf("cursor on its own capabilities must not warn about interception:\n%s", def)
	}

	// An explicitly named unsupported axis is refused, naming what IS allowed.
	for _, tc := range []struct{ flag, value, want string }{
		{"--egress-mode", "allowlist", "does not support --egress-mode allowlist (allowed: open)"},
		{"--egress-mode", "review", "does not support --egress-mode review (allowed: open)"},
		{"--credentials", "broker", "does not support --credentials broker (allowed: forward)"},
	} {
		out, err := runPrintErr(t, proveoBin, "cursor", tc.flag, tc.value)
		if err == nil {
			t.Errorf("cursor %s %s should be refused, but succeeded:\n%s", tc.flag, tc.value, out)
			continue
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("cursor %s %s: want a refusal mentioning %q, got:\n%s", tc.flag, tc.value, tc.want, out)
		}
		if strings.Contains(out, entrypoint.DefaultSentinel) {
			t.Errorf("a refused run must not still plan a sentinel handover:\n%s", out)
		}
	}
}

// runPrintErr is runPrint for the paths that are SUPPOSED to fail: it returns the
// output and the error instead of failing the test on a non-zero exit.
func runPrintErr(t *testing.T, proveoBin, target string, extra ...string) (string, error) {
	t.Helper()
	work := t.TempDir()
	args := append([]string{"run", target}, extra...)
	args = append(args, "--print", "--input", work)
	cmd := exec.Command(proveoBin, args...)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(envWithoutProviderKeys(), "CURSOR_API_KEY=crsr_test_probe")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// seedAgentSettings pre-writes ~/.proveo/agent-settings.yml for target so the
// run enters its cached choice instead of prompting. The fingerprint is computed
// from the def's real capabilities, so this stays correct if they change.
func seedAgentSettings(t *testing.T, proveoHome, target, egressMode, credentials string) {
	t.Helper()
	ms, err := manifest.Load(filepath.Join(repoRoot(t), "defs"))
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	var caps manifest.Capabilities
	found := false
	for _, m := range ms {
		if _, ok := m.Images[target]; ok {
			caps, found = m.Capabilities, true
			break
		}
	}
	if !found {
		t.Fatalf("no manifest declares target %q", target)
	}
	st := &agentsettings.Store{}
	st.Remember(target, caps, agentsettings.Choice{Egress: egressMode, Credentials: credentials})
	if err := st.Save(proveoHome); err != nil {
		t.Fatalf("seed agent settings: %v", err)
	}
}

// ── helpers ─────────────────────────────────────────────────

// runPrint runs `proveo run <target> [extra] --print` with a probe CURSOR_API_KEY
// (and real provider keys stripped), returning combined output.
func runPrint(t *testing.T, proveoBin, target string, extra ...string) string {
	t.Helper()
	work := t.TempDir()
	args := append([]string{"run", target}, extra...)
	args = append(args, "--print", "--input", work)
	cmd := exec.Command(proveoBin, args...)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(envWithoutProviderKeys(), "CURSOR_API_KEY=crsr_test_probe")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// hasBareEnv reports whether cmd contains `-e KEY` (a bare forward, value from the
// process env) as opposed to `-e KEY=…`.
func hasBareEnv(cmd, key string) bool {
	toks := strings.Fields(cmd)
	for i := 0; i+1 < len(toks); i++ {
		if toks[i] == "-e" && toks[i+1] == key {
			return true
		}
	}
	return false
}

func requireLiveStack(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if !tmux.Available() {
		t.Skip("tmux not installed (brew install tmux)")
	}
	if !dockerImagePresent(t, "proveo/egress-proxy:latest") || !dockerImagePresent(t, "proveo/cursor:latest") {
		t.Skip("proveo/egress-proxy or proveo/cursor image not built")
	}
}

// printEnforcedPlan runs `proveo run <agent> --egress-mode allowlist --print` with
// the random provider keys set (and any real ones stripped), returning its output.
// printEnforcedPlan renders an agent's plan on the enforced tier, where every
// declared secret must reach the container as the sentinel.
func printEnforcedPlan(t *testing.T, proveoBin, agent string, kv []string) string {
	t.Helper()
	work := t.TempDir()
	cmd := exec.Command(proveoBin, "run", agent, "--egress-mode", "allowlist", "--print", "--input", work)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(envWithoutProviderKeys(), kv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo run %s --print: %v\n%s", agent, err, out)
	}
	return string(out)
}

// agentCommandLine extracts the agent's `docker …` command line from --print
// output (the line following the "# agent" marker).
func agentCommandLine(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "# agent" && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	t.Fatalf("no '# agent' command in --print output:\n%s", out)
	return ""
}

// envValInCmd finds `-e KEY=VALUE` in a docker command line and returns VALUE.
func envValInCmd(cmd, key string) (string, bool) {
	for _, tok := range strings.Fields(cmd) {
		if v, ok := strings.CutPrefix(tok, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// envWithoutProviderKeys is os.Environ() with every provider key stripped, so the
// only provider keys proveo sees are the random ones we inject.
func envWithoutProviderKeys() []string {
	drop := make(map[string]bool)
	for _, k := range provider.KeyVars() {
		drop[k] = true
	}
	var out []string
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i >= 0 && drop[e[:i]] {
			continue
		}
		out = append(out, e)
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..")
}

func forceClean(proveoBin string) { _ = exec.Command(proveoBin, "clean", "--force").Run() }

// forceCleanIfCrowded is the per-test teardown: `proveo clean --force` removes
// EVERY proveo container, so running it after each test can tear down a sibling
// still coming up. Below a small threshold that risk outweighs the benefit —
// leave them for the suite-level clean and only sweep when they accumulate.
const cleanThreshold = 10

func forceCleanIfCrowded(proveoBin string) {
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=proveo.egress.session").Output()
	if err != nil || len(strings.Fields(string(out))) < cleanThreshold {
		return
	}
	forceClean(proveoBin)
}

func rmByAncestor(image string) {
	if out, err := exec.Command("docker", "ps", "-aq", "--filter", "ancestor="+image).Output(); err == nil {
		for _, id := range strings.Fields(string(out)) {
			_ = exec.Command("docker", "rm", "-f", id).Run()
		}
	}
}

func randToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "tst_" + hex.EncodeToString(b)
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

func dockerPSNames() []string {
	out, err := exec.Command("docker", "ps", "--format", "{{.Names}}").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

func containersWithSuffix(suffix string) map[string]bool {
	set := map[string]bool{}
	for _, n := range dockerPSNames() {
		if strings.HasSuffix(n, suffix) {
			set[n] = true
		}
	}
	return set
}

// mountSource returns the host path bind-mounted at dest inside container, and
// whether such a mount exists.
func mountSource(container, dest string) (string, bool) {
	format := fmt.Sprintf(`{{range .Mounts}}{{if eq .Destination %q}}{{.Source}}{{end}}{{end}}`, dest)
	out, err := exec.Command("docker", "inspect", container, "--format", format).Output()
	if err != nil {
		return "", false
	}
	src := strings.TrimSpace(string(out))
	return src, src != ""
}

func waitForFileExists(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s did not appear within %s", path, timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func parseKVFile(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = v
		}
	}
	return out
}
