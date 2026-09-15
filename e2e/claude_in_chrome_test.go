//go:build e2e

// SPEC: _spec/defs/claudecode/chrome-bridge.puml

package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/chromebridge"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestClaudeInChromeNavigatesTheRealBrowser is precondition (1) from
// chrome-bridge.puml's "FOUR PRECONDITIONS" note turned into a measurement:
// with a REAL Chrome running on this host and the REAL Claude in Chrome
// extension connected, a sandboxed claude session must be able to drive it.
//
// Unlike an earlier version of this test, nothing here hand-builds the
// container script or starts the relay itself: a real `proveo run claudecode`
// is driven end to end under tmux, exactly the way an operator would launch
// it. The "claude-in-chrome" add-on is pre-selected by seeding an isolated
// agent-settings cache (agentsettings.Store) rather than by navigating the
// picker's checkboxes — the form still renders once and is accepted with a
// single Enter (acceptChoicePrompt), the same pattern every other e2e test
// that seeds a choice already uses. From there, `proveo run` itself decides
// the backend (sbx, preferred when available for this manifest), starts the
// real chromebridge.Relay, and the container's own entrypoint chain
// (proveo_chrome_bridge -> PROVEO_CHROME_READY=1 -> --chrome) runs unmodified.
//
// Missing any of the four preconditions the puml note names — a connected
// extension, the transport, a browser-scoped credential, or the launch flag —
// is a skip, never a fake substitute. The credential specifically must be a
// persisted /login: an env token (a `claude setup-token` output, or a bare
// ANTHROPIC_API_KEY) is refused for Chrome regardless of any scope claimed
// for it, no matter how fresh.
func TestClaudeInChromeNavigatesTheRealBrowser(t *testing.T) {
	requireHarness(t, "claudecode") // tmux + the local image sbx hands over
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
	realHome := requireChromeCapableLoginHome(t)

	proveoBin := buildProveo(t)

	// An ISOLATED proveo home: proveo run's own credential refresh, and the
	// addon choice this test seeds, must never touch the operator's real
	// home — only the persisted login file itself is cloned in.
	home := t.TempDir()
	proveoHome := filepath.Join(home, "proveo")
	cloneLogin(t, realHome, proveoHome, "claudecode")
	seedChromeAddon(t, proveoHome, "claudecode")

	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	resultRel := "chrome-nav-result.txt"
	promptPath := "/app/output/" + resultRel
	// claudecode writes deliverables to the output mount; the host side may
	// land under "reports/" or at the mount root depending on layout — check
	// both, same as e2e/hello_world_test.go's claudecode case.
	hostPaths := []string{"reports/" + resultRel, resultRel}

	// The page this test alone can produce, served on loopback where the REAL
	// host Chrome can reach it directly — no bridging needed for the fetch
	// itself, only for the command that tells Chrome to make it.
	marker := fmt.Sprintf("PROVEO-E2E-CHROME-%d", time.Now().UnixNano())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<html><body><p id=\"marker\">%s</p></body></html>", marker)
	}))
	t.Cleanup(srv.Close)

	prompt := fmt.Sprintf(
		"Use the Chrome browser tool available to you to open %s in a tab and read the "+
			"visible page text. Then create a file at %s whose entire contents are exactly "+
			"that page text, with no trailing newline. Do not create, edit, or delete any "+
			"other file.",
		srv.URL, promptPath)

	sess := tmux.New(fmt.Sprintf("proveo-cic-%d", time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)

	cmd := []string{"env",
		"-u", "CLAUDE_CODE_OAUTH_TOKEN", // must not shadow the cloned /login
		"TERM=xterm-256color",
		"HOME=" + home,
		"PROVEO_HOME=" + proveoHome,
		proveoBin, "run", "claudecode", "--input", work, "--", "-p", prompt,
	}
	if err := sess.Start(200, 50, cmd...); err != nil {
		t.Fatalf("start session: %v", err)
	}
	acceptChoicePrompt(t, sess, "claudecode")

	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 4*time.Minute)
	w := newWatcher(t, sess)
	var found string
	w.until("the claude --chrome session to write its navigation result", timeout, func() bool {
		found = firstExisting(work, hostPaths)
		return found != ""
	})

	body := readFile(filepath.Join(work, found))
	if !strings.Contains(body, marker) {
		t.Fatalf("navigation result does not contain this run's marker %q — the sandbox did not "+
			"really drive the host browser to the real page:\ngot: %q\nsession output:\n%s",
			marker, body, w.Screen())
	}
	t.Logf("real Chrome navigated and returned this run's marker: %s", marker)
}

// requireChromeCapableLoginHome skips unless this host holds a persisted,
// non-blanked Claude Code login in the proveo home — the ONLY credential
// shape Chrome integration actually accepts. An env token minted via `claude
// setup-token` (or a bare ANTHROPIC_API_KEY) is refused for Chrome no matter
// what CLAUDE_CODE_OAUTH_SCOPES claims for it: Claude Code checks scope
// against the token's real server-side grant, and a setup-token's grant is
// always inference-only underneath — the client-declared scope changes
// nothing. Only an interactive /login's persisted credential carries a real,
// server-verified browser scope (chromebridge.go's ScopeGate: "a persisted
// /login ... its real scopes include user:profile"), so that home is what
// this test clones the credential file out of.
func requireChromeCapableLoginHome(t *testing.T) string {
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
	return homeRoot
}

// cloneLogin copies just the persisted login file out of realHome into an
// otherwise-empty cloneHome, at the same relative path
// credentials.PersistedLogin/LoginBlanked check — so proveo run, pointed at
// cloneHome, finds a usable /login without the operator's real home ever
// being mounted or refreshed by the run.
func cloneLogin(t *testing.T, realHome, cloneHome, target string) {
	t.Helper()
	rel := filepath.Join(".claude", ".credentials.json")
	b, err := os.ReadFile(filepath.Join(realHome, rel))
	if err != nil {
		t.Skipf("persisted login reported usable but could not be read: %v", err)
	}
	dst := filepath.Join(cloneHome, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedChromeAddon pre-selects the "claude-in-chrome" add-on in an isolated
// agent-settings cache, so the real choiceui form that proveo run renders
// comes up with the box already ticked — acceptChoicePrompt then confirms it
// with one Enter, same as every other seeded-choice e2e test.
func seedChromeAddon(t *testing.T, proveoHome, target string) {
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
	st.Remember(target, caps, agentsettings.Choice{
		Egress:      "allowlist",
		Credentials: "broker",
		Addons:      []string{chromebridge.Addon},
	})
	if err := st.Save(proveoHome); err != nil {
		t.Fatalf("seed agent settings: %v", err)
	}
}
