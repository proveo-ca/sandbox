//go:build e2e

// SPEC: _spec/tests/testing-strategy.puml — credential-forwarding integrity.

package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/provider"
)

// TestCredentialForwardingIntegrity asserts that provider API keys never ride the
// sbx launch argv, over random per-run values so no real secret is involved.
func TestCredentialForwardingIntegrity(t *testing.T) {
	proveoBin := buildProveo(t)
	keys := provider.KeyVars()
	if len(keys) == 0 {
		t.Fatal("provider.KeyVars() is empty")
	}
	agents := strings.Fields(env("PROVEO_TEST_AGENTS", "opencode claudecode cecli"))

	// Isolation — deterministic, one subtest per agent (parallel: pure --print).
	for _, agent := range agents {
		agent := agent
		t.Run(agent+"_isolation", func(t *testing.T) {
			t.Parallel()
			assertPlanIsolation(t, proveoBin, agent, keys)
		})
	}
}

// assertPlanIsolation checks the agent's sbx launch argv: no provider key name
// carries a value, and no raw key value appears.
func assertPlanIsolation(t *testing.T, proveoBin, agent string, keys []string) {
	t.Helper()
	want := make(map[string]string, len(keys))
	kv := make([]string, 0, len(keys))
	for _, k := range keys {
		v := randToken()
		want[k] = v
		kv = append(kv, k+"="+v)
	}

	plan := printEnforcedPlan(t, proveoBin, agent, kv)
	if !secretLineNamesAny(plan, keys) {
		t.Errorf("%s: the plan's secret-store line names none of the %d injected keys — proveo never saw "+
			"them, so their absence from argv proves nothing:\n%s", agent, len(keys), plan)
	}
	agentCmd := agentCommandLine(t, plan)
	for _, k := range keys {
		if strings.Contains(agentCmd, want[k]) {
			t.Errorf("%s: RAW %s value appears in the agent launch command — it belongs in sbx's secret store", agent, k)
		}
		if v, ok := envValInCmd(agentCmd, k); ok {
			t.Errorf("%s: sandbox plan carries %s=%q in argv; a sandbox secret travels "+
				"over the store, and argv is visible to anyone listing processes", agent, k, v)
		}
	}
}

// TestCursorBrokersThroughTheSbxStore asserts cursor's key goes to sbx's secret
// store and never rides argv.
func TestCursorBrokersThroughTheSbxStore(t *testing.T) {
	proveoBin := buildProveo(t)

	def := runPrint(t, proveoBin, "cursor")
	defCmd := agentCommandLine(t, def)
	if hasBareEnv(defCmd, "CURSOR_API_KEY") {
		t.Errorf("cursor brokers natively — the key belongs in sbx's secret store, not a bare -e:\n%s", defCmd)
	}
	if !strings.Contains(def, "secret store first: CURSOR_API_KEY") {
		t.Errorf("cursor must name CURSOR_API_KEY for sbx's secret store:\n%s", def)
	}
	if strings.Contains(def, "invalid API key") {
		t.Errorf("cursor on its own capabilities must not warn about interception:\n%s", def)
	}
}

// ── helpers ─────────────────────────────────────────────────

// runPrint runs `proveo run <target> [extra] --print` with a probe CURSOR_API_KEY
// (and real provider keys stripped), returning combined output.
func runPrint(t *testing.T, proveoBin, target string, extra ...string) string {
	t.Helper()
	requireSbx(t)
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

// secretLineNamesAny reports whether the plan's "secret store first:" line names any of keys.
func secretLineNamesAny(plan string, keys []string) bool {
	for _, line := range strings.Split(plan, "\n") {
		_, names, ok := strings.Cut(line, "secret store first:")
		if !ok {
			continue
		}
		for _, n := range strings.FieldsFunc(names, func(r rune) bool { return r == ',' || r == ' ' || r == '(' }) {
			if slices.Contains(keys, n) {
				return true
			}
		}
	}
	return false
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

func printEnforcedPlan(t *testing.T, proveoBin, agent string, kv []string) string {
	t.Helper()
	requireSbx(t)
	work := t.TempDir()
	cmd := exec.Command(proveoBin, "run", agent, "--print", "--input", work)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(envWithoutProviderKeys(), kv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo run %s --print: %v\n%s", agent, err, out)
	}
	return string(out)
}

// agentCommandLine extracts the agent's `sbx run …` command line from --print
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

// envValInCmd finds `-e KEY=VALUE` in an sbx command line and returns VALUE.
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
	return filepath.Join(wd, "..")
}

func randToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "tst_" + hex.EncodeToString(b)
}
