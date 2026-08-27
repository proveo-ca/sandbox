//go:build integration

// SPEC: _spec/tests/30-infra-integration.puml

package egress

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFirewallIntegration brings up the real firewall topology
// (Squid + the Go egress proxy) via BuildPlan, then drives a curl "agent"
// container through it. It asserts the load-bearing Docker invariants:
//   - the agent reaches the internet ONLY through mitmproxy -> squid, trusting
//     the generated CA (a real HTTPS GET succeeds);
//   - the decrypted flow is recorded to flows.ndjson;
//   - the agent's internal network has no direct egress (a proxy-bypassing
//     request fails).
//
// Gated: -tags=integration and PROVEO_EGRESS_INTEGRATION=1 (needs Docker + the
// proveo/egress-proxy image + internet). Skipped otherwise.
func TestFirewallIntegration(t *testing.T) {
	if os.Getenv("PROVEO_EGRESS_INTEGRATION") != "1" {
		t.Skip("set PROVEO_EGRESS_INTEGRATION=1 to run (needs Docker + internet)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	uid, gid := fmt.Sprint(os.Getuid()), fmt.Sprint(os.Getgid())
	state := t.TempDir()
	sid := fmt.Sprintf("proveo-it-%d", os.Getpid())
	opts := Options{
		Mode: "allowlist", SessionID: sid, AgentName: "itest",
		UID: uid, GID: gid,
		ConfDir: filepath.Join(state, "mitmproxy", "confdir"), FlowsDir: filepath.Join(state, "mitmproxy", "flows"),
		SquidConfigDir: filepath.Join(state, "squid", "config"), SquidLogDir: filepath.Join(state, "squid", "logs"),
	}

	// Stage squid config (no provider pinned: reads are allowed by default) and
	// create the mount targets. Squid logs dir is world-writable so the squid
	// image's user can write regardless of its uid.
	if err := StageSquidConfig(os.DirFS(repoRoot(t)), opts.SquidConfigDir, nil, ""); err != nil {
		t.Fatalf("stage squid config: %v", err)
	}
	for _, d := range []string{opts.SquidLogDir, opts.ConfDir, opts.FlowsDir} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Chmod(opts.SquidLogDir, 0o777)

	plan, err := BuildPlan(opts)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	r := ExecRunner{Stderr: true}
	t.Cleanup(func() { plan.Teardown(r) })
	if err := plan.Apply(r); err != nil {
		t.Fatalf("bring up topology: %v", err)
	}
	if err := waitFile(plan.CAWaitPath, 25*time.Second); err != nil {
		t.Fatalf("CA never appeared: %v", err)
	}

	// 1) Chain + CA: a real HTTPS GET through the agent network must succeed,
	//    retried until the proxy/squid chain is ready (condition wait, not sleep-only).
	agentArgs := append([]string{"run", "--rm"}, plan.AgentArgs...)
	if err := waitHTTPThroughProxy(r, agentArgs, "https://example.com", 25*time.Second); err != nil {
		t.Fatalf("agent GET https://example.com through the chain: %v", err)
	}

	// 2) The decrypted flow was recorded.
	flows, err := os.ReadFile(filepath.Join(opts.FlowsDir, "flows.ndjson"))
	if err != nil {
		t.Fatalf("read flows.ndjson: %v", err)
	}
	if !strings.Contains(string(flows), "example.com") {
		t.Errorf("flows.ndjson did not record the decrypted request to example.com:\n%s", flows)
	}

	// 3) Network isolation: bypassing the proxy has no route off the --internal
	//    agent network, so a direct request must fail.
	if _, err := r.Run(append(append([]string{}, agentArgs...),
		"curlimages/curl:latest", "--noproxy", "*", "-sS", "-m", "10", "-o", "/dev/null",
		"https://example.com")...); err == nil {
		t.Error("proxy-bypassing request succeeded; the agent network must have no direct egress")
	}
}

// TestFirewallPolicyIntegration proves the egress policy (S1) end-to-end through
// the REAL MITM: a read is allowed, while a write to a non-allowlisted host, a
// GET to an exfil sink, and a request carrying a known secret are each blocked
// before leaving — and the block is recorded to flows.ndjson.
//
// Gated: -tags=integration and PROVEO_EGRESS_INTEGRATION=1.
func TestFirewallPolicyIntegration(t *testing.T) {
	if os.Getenv("PROVEO_EGRESS_INTEGRATION") != "1" {
		t.Skip("set PROVEO_EGRESS_INTEGRATION=1 to run (needs Docker + internet)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	const secret = "sk-ant-FAKE-SECRET-0123456789"
	uid, gid := fmt.Sprint(os.Getuid()), fmt.Sprint(os.Getgid())
	state := t.TempDir()
	sid := fmt.Sprintf("proveo-pol-%d", os.Getpid())

	// A pinned provider gives the policy its provider host + a secret to protect.
	injectDir := filepath.Join(state, "inject")
	if err := os.MkdirAll(injectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	brokerEnv := filepath.Join(injectDir, "broker.env")
	if err := os.WriteFile(brokerEnv, []byte("ANTHROPIC_API_KEY="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(injectDir) })

	opts := Options{
		Mode: "allowlist", SessionID: sid, AgentName: "itest", UID: uid, GID: gid,
		Providers: []string{"anthropic"}, BrokerEnvFile: brokerEnv,
		ConfDir: filepath.Join(state, "mitmproxy", "confdir"), FlowsDir: filepath.Join(state, "mitmproxy", "flows"),
		SquidConfigDir: filepath.Join(state, "squid", "config"), SquidLogDir: filepath.Join(state, "squid", "logs"),
	}
	if err := StageSquidConfig(os.DirFS(repoRoot(t)), opts.SquidConfigDir, []string{"anthropic"}, ""); err != nil {
		t.Fatalf("stage squid config: %v", err)
	}
	for _, d := range []string{opts.SquidLogDir, opts.ConfDir, opts.FlowsDir} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Chmod(opts.SquidLogDir, 0o777)

	plan, err := BuildPlan(opts)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	r := ExecRunner{Stderr: true}
	t.Cleanup(func() { plan.Teardown(r) })
	if err := plan.Apply(r); err != nil {
		t.Fatalf("bring up topology: %v", err)
	}
	if err := waitFile(plan.CAWaitPath, 25*time.Second); err != nil {
		t.Fatalf("CA never appeared: %v", err)
	}

	agentArgs := append([]string{"run", "--rm"}, plan.AgentArgs...)
	// reached reports whether a `curl -f` completed with a 2xx — i.e. the request
	// was NOT blocked by the policy and actually reached the upstream. A policy
	// block aborts the request, so curl fails (non-zero exit).
	reached := func(curlArgs ...string) bool {
		base := append(append([]string{}, agentArgs...), "curlimages/curl:latest", "-fsS", "-m", "20", "-o", "/dev/null")
		_, err := r.Run(append(base, curlArgs...)...)
		return err == nil
	}

	if err := waitCond(25*time.Second, func() bool { return reached("https://example.com/") }); err != nil {
		t.Fatal("read GET https://example.com must be allowed through the chain")
	}

	// BLOCK cases: each must NOT reach the upstream.
	blocked := map[string]bool{
		"write to non-allowlisted host": !reached("-X", "POST", "-d", "x=1", "https://example.com/"),
		"GET to exfil sink":             !reached("https://webhook.site/proveo-test"),
		"secret in query string":        !reached("https://example.com/?k=" + secret),
	}
	for name, wasBlocked := range blocked {
		if !wasBlocked {
			t.Errorf("%s: reached upstream, want a policy block", name)
		}
	}

	// The block is auditable: a "blocked" decision was recorded.
	flows, err := os.ReadFile(filepath.Join(opts.FlowsDir, "flows.ndjson"))
	if err != nil {
		t.Fatalf("read flows.ndjson: %v", err)
	}
	if !strings.Contains(string(flows), `"decision":"blocked"`) {
		t.Errorf("flows.ndjson did not record a blocked decision:\n%s", flows)
	}
	// The secret must never be written to the flow log.
	if strings.Contains(string(flows), secret) {
		t.Error("flows.ndjson leaked the secret value")
	}
}

// TestFirewallBrokerEnvMountIntegration proves host-side broker.env (as written
// from a project .env with CURSOR_API_KEY) is mounted into the proxy and the
// plan wires PROVEO_EGRESS_PROVIDER=cursor. Topology must still serve HTTPS.
func TestFirewallBrokerEnvMountIntegration(t *testing.T) {
	if os.Getenv("PROVEO_EGRESS_INTEGRATION") != "1" {
		t.Skip("set PROVEO_EGRESS_INTEGRATION=1 to run (needs Docker + internet)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	uid, gid := fmt.Sprint(os.Getuid()), fmt.Sprint(os.Getgid())
	state := t.TempDir()
	sid := fmt.Sprintf("proveo-brk-%d", os.Getpid())
	injectDir := filepath.Join(state, "inject")
	if err := os.MkdirAll(injectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	brokerEnv := filepath.Join(injectDir, "broker.env")
	if err := os.WriteFile(brokerEnv, []byte("CURSOR_API_KEY=sk-cursor-from-host-env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(injectDir) })

	opts := Options{
		Mode: "allowlist", SessionID: sid, AgentName: "itest", UID: uid, GID: gid,
		Providers: []string{"cursor"}, BrokerEnvFile: brokerEnv,
		ConfDir: filepath.Join(state, "mitmproxy", "confdir"), FlowsDir: filepath.Join(state, "mitmproxy", "flows"),
		SquidConfigDir: filepath.Join(state, "squid", "config"), SquidLogDir: filepath.Join(state, "squid", "logs"),
	}
	if err := StageSquidConfig(os.DirFS(repoRoot(t)), opts.SquidConfigDir, []string{"cursor"}, ""); err != nil {
		t.Fatalf("stage squid config: %v", err)
	}
	for _, d := range []string{opts.SquidLogDir, opts.ConfDir, opts.FlowsDir} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Chmod(opts.SquidLogDir, 0o777)

	plan, err := BuildPlan(opts)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	joined := strings.Join(flattenCmds(plan.Sidecars), " ")
	if !strings.Contains(joined, "PROVEO_EGRESS_PROVIDERS=cursor") {
		t.Errorf("proxy sidecar must pin cursor provider, got: %s", joined)
	}
	if !strings.Contains(joined, "PROVEO_EGRESS_BROKER_ENVFILE=/broker/broker.env") {
		t.Errorf("proxy sidecar must mount broker envfile, got: %s", joined)
	}
	if !strings.Contains(joined, injectDir+":/broker:ro") && !strings.Contains(joined, filepath.Dir(brokerEnv)+":/broker:ro") {
		t.Errorf("proxy sidecar must bind-mount inject dir at /broker:ro, got: %s", joined)
	}

	r := ExecRunner{Stderr: true}
	t.Cleanup(func() { plan.Teardown(r) })
	if err := plan.Apply(r); err != nil {
		t.Fatalf("bring up topology: %v", err)
	}
	if err := waitFile(plan.CAWaitPath, 25*time.Second); err != nil {
		t.Fatalf("CA never appeared: %v", err)
	}
	agentArgs := append([]string{"run", "--rm"}, plan.AgentArgs...)
	if err := waitHTTPThroughProxy(r, agentArgs, "https://example.com", 25*time.Second); err != nil {
		t.Fatalf("broker-mounted topology GET: %v", err)
	}
}

func flattenCmds(cmds []Command) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, c...)
	}
	return out
}

func waitFile(path string, timeout time.Duration) error {
	return waitCond(timeout, func() bool {
		fi, err := os.Stat(path)
		return err == nil && fi.Size() > 0
	})
}

func waitCond(timeout time.Duration, ready func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if ready() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s", timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func waitHTTPThroughProxy(r ExecRunner, agentArgs []string, url string, timeout time.Duration) error {
	var lastCode string
	var lastErr error
	err := waitCond(timeout, func() bool {
		code, runErr := r.Run(append(append([]string{}, agentArgs...),
			"curlimages/curl:latest", "-sS", "-m", "20", "-o", "/dev/null", "-w", "%{http_code}",
			url)...)
		lastCode, lastErr = strings.TrimSpace(code), runErr
		return runErr == nil && lastCode == "200"
	})
	if err != nil {
		return fmt.Errorf("%w (last code=%q err=%v)", err, lastCode, lastErr)
	}
	return nil
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..")
}
