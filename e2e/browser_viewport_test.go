//go:build e2e

// SPEC: _spec/internal/sbx/browser-viewport.puml

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// TestBrowserViewportReachesTheAgentsChromium proves the whole chain the browser
// add-on publishes, using the same three builders the run uses. The assertion is
// a TARGET LIST containing the page the agent's own tool opened.
func TestBrowserViewportReachesTheAgentsChromium(t *testing.T) {
	if !sbxAvailable() {
		t.Skip("sandbox backend unavailable")
	}
	image := harnessImage(t, "claudecode-browser")

	work := t.TempDir()
	name := fmt.Sprintf("proveo-viewport-%d", time.Now().Unix())
	hostPort := sandbox.FreeLoopbackPort()
	if hostPort == 0 {
		t.Fatal("no free loopback port")
	}

	create := exec.Command(sbx.Binary, "create", "--name", name, "-t", image,
		"-p", fmt.Sprintf("%d:%d", hostPort, sbx.CDPRelayPort),
		"-e", "AGENT_BROWSER_ARGS="+sbx.BrowserCDPArgs(""),
		"shell", work)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("sbx create: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(sbx.Binary, "rm", "--force", name).Run() })

	// The relay, exactly as the run starts it. Held for the test's life, because
	// its whole point is that the exec's lifetime bounds the exposure.
	relay := exec.Command(sbx.Binary, sbx.CDPRelayArgs(name)...)
	if err := relay.Start(); err != nil {
		t.Fatalf("start relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Process.Kill() })

	// The AGENT's browser, opened by the agent's own tool rather than by a
	// hand-rolled Chromium — so what the host attaches to is what the agent uses.
	open := exec.Command(sbx.Binary, "exec", "-w", "/", name, "--",
		"bash", "-lc", "agent-browser open about:blank >/tmp/ab.log 2>&1 &")
	if out, err := open.CombinedOutput(); err != nil {
		t.Fatalf("agent-browser open: %v\n%s", err, out)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/json/list", hostPort)
	deadline := time.Now().Add(durationEnv(t, "PROVEO_TEST_TIMEOUT", 3*time.Minute))
	var last string
	for time.Now().Before(deadline) {
		targets, body := cdpTargets(url)
		last = body
		if len(targets) > 0 {
			t.Logf("viewport reached %d target(s) through 127.0.0.1:%d; first: %s",
				len(targets), hostPort, targets[0])
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("no CDP target reachable at %s within the budget — last answer: %q", url, last)
}

// cdpTargets asks the DevTools endpoint for its page list, returning the target
// types it named. A reset connection is an empty list, not a failure: the relay
// answers before the agent has opened anything.
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
