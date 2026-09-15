//go:build e2e

// SPEC: _spec/internal/sbx/browser-viewport.puml

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/tmux"
)

// TestBrowserViewportReachesTheAgentsChromium proves the whole chain the
// browser add-on publishes by driving a real `proveo run claudecode --shell`
// with the "browser" add-on pre-selected, instead of hand-assembling the
// three pieces (a `sbx create -p hostPort:9222`, a manually started CDP
// relay, and a manual `sbx exec ... agent-browser open`) that `proveo run`
// itself wires up when the add-on is on: the target stays "claudecode" — the
// add-on, not a separate "claudecode-browser" target, is what swaps in the
// browser image and starts the relay (internal/run/run.go's
// `rs.Backend.BrowserImage` lookup keys on `target+"-browser"`, so passing
// the browser image name directly as the target would bypass that wiring).
func TestBrowserViewportReachesTheAgentsChromium(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	const target = "claudecode"
	requireTmux(t)
	harnessImage(t, target+"-browser") // the image the add-on actually swaps in

	proveoBin := buildProveo(t)
	home := t.TempDir()
	seedBrowserAddon(t, home, target)

	work := t.TempDir()
	sess := tmux.New(fmt.Sprintf("proveo-viewport-%d", time.Now().UnixNano()), nil)
	t.Cleanup(sess.Kill)

	cmd := []string{"env", "PROVEO_HOME=" + home,
		proveoBin, "run", target, "--input", work, "--shell"}
	if err := sess.Start(220, 50, cmd...); err != nil {
		t.Fatalf("start session: %v", err)
	}
	acceptChoicePrompt(t, sess, target)

	timeout := durationEnv(t, "PROVEO_TEST_TIMEOUT", 3*time.Minute)
	w := newWatcher(t, sess)
	waitForContainerShell(t, w, timeout)

	url := viewportURL(w.Screen())
	if url == "" {
		t.Fatalf("no browser viewport line printed by the run — the add-on did not "+
			"start the relay:\n%s", w.Screen())
	}

	// The AGENT's browser, opened by the agent's own tool rather than by a
	// hand-rolled Chromium — so what the host attaches to is what the agent uses.
	shellExec(t, sess, "agent-browser open about:blank >/tmp/ab.log 2>&1 & sleep 1", 20*time.Second)

	listURL := url + "/json/list"
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		targets, body := cdpTargets(listURL)
		last = body
		if len(targets) > 0 {
			t.Logf("viewport reached %d target(s) through %s; first: %s", len(targets), listURL, targets[0])
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("no CDP target reachable at %s within the budget — last answer: %q", listURL, last)
}

// viewportLineRE matches StartCDPViewport's own announcement
// (internal/backend/sandbox/sandbox.go), so this test reads the exact string
// an operator would, rather than reconstructing the port some other way.
var viewportLineRE = regexp.MustCompile(`browser viewport: (http://\S+)`)

func viewportURL(screen string) string {
	m := viewportLineRE.FindStringSubmatch(screen)
	if m == nil {
		return ""
	}
	return m[1]
}

// seedBrowserAddon pre-selects the "browser" add-on in an isolated
// agent-settings cache, so the real choiceui form proveo run renders comes up
// with the box already ticked — acceptChoicePrompt then confirms it with one
// Enter, same as every other seeded-choice e2e test.
func seedBrowserAddon(t *testing.T, proveoHome, target string) {
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
		Addons:      []string{"browser"},
	})
	if err := st.Save(proveoHome); err != nil {
		t.Fatalf("seed agent settings: %v", err)
	}
}

func cdpTargets(url string) ([]string, string) {
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Get(url)
	if err != nil {
		return nil, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var list []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
		WS   string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, strings.TrimSpace(string(body))
	}
	var out []string
	for _, tg := range list {
		// The WS URL is what a DevTools frontend or Playwright would dial; a target
		// without one is not attachable and proves nothing.
		if tg.WS == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s %s", tg.Type, tg.URL))
	}
	return out, strings.TrimSpace(string(body))
}
