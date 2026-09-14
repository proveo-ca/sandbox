//go:build e2e

// SPEC: _spec/defs/claudecode/chrome-bridge.puml, _spec/_plans/claude-in-chrome-reachability.puml

package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/chromebridge"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/proveohome"
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
// credential, or the launch flag — is a skip, never a fake substitute. The
// credential specifically must be a persisted /login: an env token (a
// `claude setup-token` output, or a bare ANTHROPIC_API_KEY) is refused for
// Chrome regardless of any scope claimed for it, no matter how fresh.
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
	credsJSON := requireChromeCapableCredential(t)

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

	// The credential travels as the SAME file shape `claude` reads on a real
	// login — not an env var — so the container's own claude binary refreshes
	// it itself via the refresh token, exactly like a real `proveo run` session.
	// base64 sidesteps quoting the JSON through two layers of shell.
	script := fmt.Sprintf(`set -e
export HOME=/tmp
export %s=%q
export %s=%q
mkdir -p /tmp/.claude
base64 -d <<'PROVEO_CREDS_B64' > /tmp/.claude/.credentials.json
%s
PROVEO_CREDS_B64
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
		base64.StdEncoding.EncodeToString(credsJSON),
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

// requireChromeCapableCredential skips unless this host holds a persisted,
// non-blanked Claude Code login in the proveo home — the ONLY credential
// shape Chrome integration actually accepts. An env token minted via `claude
// setup-token` (or a bare ANTHROPIC_API_KEY) is refused for Chrome no matter
// what CLAUDE_CODE_OAUTH_SCOPES claims for it: Claude Code checks scope
// against the token's real server-side grant, and a setup-token's grant is
// always inference-only underneath — the client-declared scope changes
// nothing. Only an interactive /login's persisted credential carries a real,
// server-verified browser scope (chromebridge.go's ScopeGate: "a persisted
// /login ... its real scopes include user:profile"), so that persisted file
// is what this test forwards into the sandbox — the SAME source `proveo run
// claudecode` itself uses, not a value someone has to separately mint and
// keep fresh.
func requireChromeCapableCredential(t *testing.T) []byte {
	t.Helper()
	homeRoot := proveohome.Root(os.Getenv)
	if credentials.LoginBlanked("claudecode", homeRoot) {
		t.Skipf("the login in the proveo home (%s) is empty — macOS moved the token to the "+
			"Keychain and blanked the file, which the sandbox cannot read either; run "+
			"`proveo run claudecode --shell` and /login INSIDE that session to put a usable "+
			"one in the proveo home", homeRoot)
	}
	if ok, _ := credentials.PersistedLogin("claudecode", homeRoot); !ok {
		t.Skipf("no persisted Claude Code login in the proveo home (%s) — run `proveo run "+
			"claudecode --shell` and /login INSIDE that session once; a `claude setup-token` "+
			"or a bare ANTHROPIC_API_KEY cannot drive Chrome no matter how fresh it is", homeRoot)
	}
	b, err := os.ReadFile(filepath.Join(homeRoot, ".claude", ".credentials.json"))
	if err != nil {
		t.Skipf("persisted login reported usable but could not be read: %v", err)
	}
	return b
}
