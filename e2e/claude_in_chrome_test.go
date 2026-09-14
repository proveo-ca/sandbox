//go:build e2e

// SPEC: _spec/defs/claudecode/chrome-bridge.puml, _spec/_plans/claude-in-chrome-reachability.puml

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/chromebridge"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// TestClaudeInChromeNavigatesTheRealBrowser is the first precondition (1) from
// the reachability plan turned into a measurement: with a REAL Chrome running
// on this host and the REAL Claude in Chrome extension connected, a sandboxed
// claude session must be able to drive it. Nothing here is faked — no stand-in
// native host, no invented wire protocol. Where chrome_bridge_sbx_test.go
// proves bytes cross the five hops, this proves the far end is a real page:
// the sandbox's own claude --chrome session navigates a real Chrome tab to a
// page this test generated, and the marker only that page could have produced
// comes back through the whole chain. Missing any of the four preconditions
// the plan names — a connected extension, the transport, a browser-scoped
// credential, or the launch flag — is a skip, never a fake substitute.
func TestClaudeInChromeNavigatesTheRealBrowser(t *testing.T) {
	if _, err := exec.LookPath(sbx.Binary); err != nil {
		t.Skipf("%s not on PATH", sbx.Binary)
	}
	if ok, why := sbx.Available(); !ok {
		t.Skipf("sbx unavailable: %s", why)
	}
	// Precondition (1): a live extension. chromebridge.Available is the exact
	// probe production code runs in chromeUnavailable — reused rather than
	// reimplemented, so this test and the real picker agree on what "live" means.
	if ok, why := chromebridge.Available(chromebridge.HostSocketDir()); !ok {
		t.Skipf("no live Claude in Chrome native host on this machine: %s — "+
			"open Chrome with the extension connected and retry", why)
	}
	// Precondition (3): the credential has to be browser-capable, not just present.
	oauthToken, oauthScopes := requireChromeCapableCredential(t)

	image := harnessImage(t, "claudecode")
	if err := sbx.EnsureTemplate(image, func(string, ...any) {}); err != nil {
		t.Skipf("sbx template for %s: %v", image, err)
	}

	// The page this test alone can produce, served on loopback where the REAL
	// host Chrome can reach it directly — no bridging needed for the fetch
	// itself, only for the command that tells Chrome to make it.
	marker := fmt.Sprintf("PROVEO-E2E-CHROME-%d", time.Now().UnixNano())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<html><body><p id=\"marker\">%s</p></body></html>", marker)
	}))
	t.Cleanup(srv.Close)

	// Precondition (2), the transport: dial the REAL socket directory, not a
	// fake echo listener. If Available() above said yes, this reaches the
	// actual native host the extension spawned.
	relay, err := chromebridge.Start("127.0.0.1:0", chromebridge.HostSocketDir(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	if err := relay.SetTokenEnv(); err != nil {
		t.Fatal(err)
	}

	ws := t.TempDir()
	name := fmt.Sprintf("proveo-cic-%d", time.Now().UnixNano())
	if out, err := exec.Command(sbx.Binary, "create", "--name", name,
		"-t", image, sbx.BuiltinAgent("claudecode"), ws).CombinedOutput(); err != nil {
		t.Skipf("sbx create: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(sbx.Binary, "rm", "--force", name).CombinedOutput(); err != nil {
			t.Logf("probe sandbox %s not removed: %v\n%s", name, err, out)
		}
	})

	// proveo_chrome_bridge sets PROVEO_CHROME_READY=1 once the relay socket is
	// confirmed live inside the sandbox; that is the same variable
	// entrypoint.sh reads to append --chrome (precondition 4) to the real
	// claude invocation. Nothing here reimplements that decision.
	resultFile := "/tmp/chrome-nav-result.txt"
	prompt := fmt.Sprintf(
		"Use the Chrome browser tool available to you to open %s in a tab and read the "+
			"visible page text. Then run exactly this one bash command, with PAGE_TEXT "+
			"replaced by the exact text you read, and do nothing else: "+
			"printf '%%s' \"PAGE_TEXT\" > %s",
		srv.URL, resultFile)

	script := fmt.Sprintf(`set -e
export HOME=/tmp
export %s=%q
export %s=%q
export %s=%q
export %s=%q
source /entrypoint-lib.sh
proveo_chrome_bridge claudecode
if [[ "${PROVEO_CHROME_READY:-}" != 1 ]]; then
  echo "PROVEO_CHROME_READY never became 1 — the relay did not come up in-sandbox" >&2
  exit 5
fi
claude --dangerously-skip-permissions --chrome -p %q
`,
		chromebridge.EnvAddr, relay.ContainerAddr(),
		chromebridge.EnvToken, relay.Token(),
		chromebridge.EnvOAuthToken, oauthToken,
		chromebridge.EnvOAuthScopes, oauthScopes,
		prompt)

	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 4*time.Minute)
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, sbx.Binary, "exec", "-w", "/", name, "--", "bash", "-c", script)
	out, err := cmd.CombinedOutput()
	got := string(out)
	if credentialUnavailable.MatchString(got) {
		t.Skipf("the agent could not spend its credential — supply a funded one to exercise this path:\n%s", got)
	}
	if err != nil {
		t.Fatalf("claude --chrome session failed: %v\n%s", err, got)
	}

	readResult, err := exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "cat", resultFile).CombinedOutput()
	if err != nil {
		t.Fatalf("no navigation result written to %s (extension not really connected, or the "+
			"model did not call the browser tool): %v\nsession output:\n%s\ncat error:\n%s",
			resultFile, err, got, readResult)
	}
	if !strings.Contains(string(readResult), marker) {
		t.Fatalf("navigation result does not contain this run's marker %q — the sandbox did not "+
			"really drive the host browser to the real page:\ngot: %q\nsession output:\n%s",
			marker, readResult, got)
	}
	t.Logf("real Chrome navigated and returned this run's marker: %s", marker)
}

// requireChromeCapableCredential skips unless the host holds a Claude Code
// credential that ScopeGate would actually let through — an API key or a
// `claude setup-token` session cannot drive Chrome, only a real OAuth login
// can (chromebridge.go, ScopeGate). It returns the token and scopes to forward
// into the sandbox.
func requireChromeCapableCredential(t *testing.T) (token, scopes string) {
	t.Helper()
	token = strings.TrimSpace(os.Getenv(chromebridge.EnvOAuthToken))
	if token == "" {
		t.Skipf("no browser-capable Claude Code credential on this host — set %s "+
			"(from an interactive /login, not `claude setup-token`) and %s naming one of %s",
			chromebridge.EnvOAuthToken, chromebridge.EnvOAuthScopes, strings.Join(chromebridge.BrowserScopes, "/"))
	}
	scopes = strings.TrimSpace(os.Getenv(chromebridge.EnvOAuthScopes))
	if why := chromebridge.ScopeGate(os.Getenv, false); why != "" {
		t.Skipf("this host's %s lacks browser scope: %s", chromebridge.EnvOAuthToken, why)
	}
	return token, scopes
}
