// SPEC: _spec/_plans/isolated-browser-profiles.puml
package guestbrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlWaitsForAgentCommands(t *testing.T) {
	var c controls
	if !c.start() {
		t.Fatal("agent cannot start")
	}
	done := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	go func() { done <- c.take(ctx, "operator") }()
	for {
		c.mu.Lock()
		taking := c.owner != ""
		c.mu.Unlock()
		if taking {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("control request never started")
		}
		time.Sleep(time.Millisecond)
	}
	if c.start() {
		t.Fatal("agent can start during handoff")
	}
	if c.input("operator", func() error { return nil }) == nil {
		t.Fatal("operator can start during handoff")
	}
	c.complete(1)
	if !<-done {
		t.Fatal("operator could not take control")
	}
	if err := c.input("operator", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	c.release("operator")
	if !c.start() {
		t.Fatal("agent could not return")
	}
	c.complete(1)
}

func TestChromiumSharesViewportAndAgentProfiles(t *testing.T) {
	exe := os.Getenv("PROVEO_TEST_CHROMIUM")
	if exe == "" {
		t.Skip("set PROVEO_TEST_CHROMIUM to a Chromium executable")
	}
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/signed-in":
			if _, err := r.Cookie("login"); err != nil {
				fmt.Fprint(w, "<title>Signed out</title>")
				return
			}
			fmt.Fprint(w, "<title>Signed in</title>")
		case "/authenticate":
			http.SetCookie(w, &http.Cookie{Name: "login", Value: "authorized", Path: "/", MaxAge: 3600})
			http.Redirect(w, r, "/signed-in", http.StatusSeeOther)
		default:
			fmt.Fprint(w, `<title>Login</title><body style="margin:0"><input id="name" style="position:absolute;top:10px;left:10px">
				<a href="/authenticate" target="_blank" style="position:absolute;display:block;top:75px;left:10px;width:200px;height:50px">Sign in</a></body>`)
		}
	}))
	defer login.Close()
	root, freshRoot := t.TempDir(), t.TempDir()
	named := startTestBrowser(t, exe, root, true)
	if _, err := os.Stat(filepath.Join(root, "named", "DevToolsActivePort")); !os.IsNotExist(err) {
		t.Fatalf("Chromium launched during service startup: %v", err)
	}
	tabs := named.tabs(t)
	if len(tabs) != 1 {
		t.Fatalf("initial tabs: %+v", tabs)
	}
	named.post(t, "/take", "", 204)
	named.post(t, "/input", fmt.Sprintf(`{"tab":%q,"kind":"navigate","url":%q}`, tabs[0].ID, login.URL), 204)
	named.awaitTitle(t, "Login")
	ws := named.endpoint(t)
	if err := command(t.Context(), ws, "Target.getTargets", nil, nil); err == nil || !strings.Contains(err.Error(), "Operator has browser control") {
		t.Fatalf("agent command during handoff: %v", err)
	}
	for _, input := range []string{
		`"kind":"mousePressed","x":30,"y":20,"button":"left","clickCount":1`,
		`"kind":"mouseReleased","x":30,"y":20,"button":"left","clickCount":1`,
		`"kind":"keyDown","key":"x","code":"KeyX","keyCode":88,"text":"x"`,
		`"kind":"keyUp","key":"x","code":"KeyX","keyCode":88`,
	} {
		named.post(t, "/input", fmt.Sprintf(`{"tab":%q,%s}`, tabs[0].ID, input), 204)
	}
	if jpg := named.get(t, "/frame?tab="+tabs[0].ID); len(jpg) < 3 || jpg[0] != 0xff || jpg[1] != 0xd8 {
		t.Fatal("viewport did not return a JPEG")
	}
	for _, event := range []string{"mousePressed", "mouseReleased"} {
		named.post(t, "/input", fmt.Sprintf(`{"tab":%q,"kind":%q,"x":25,"y":90,"button":"left","clickCount":1}`, tabs[0].ID, event), 204)
	}
	named.awaitTitle(t, "Signed in")
	named.post(t, "/return", "", 204)
	base, _, _ := strings.Cut(ws, "/devtools/")
	pageWS := base + "/devtools/page/" + tabs[0].ID
	var value struct{ Result struct{ Value string } }
	if err := command(t.Context(), pageWS, "Runtime.evaluate", map[string]any{"expression": "document.querySelector('#name').value", "returnByValue": true}, &value); err != nil || value.Result.Value != "x" {
		t.Fatalf("agent cannot see the operator's input: %v, %+v", err, value)
	}
	if client := os.Getenv("PROVEO_TEST_AGENT_BROWSER"); client != "" {
		cmd := exec.CommandContext(t.Context(), client, "--cdp", named.agent, "tab", "list")
		cmd.Env = append(os.Environ(), "AGENT_BROWSER_SOCKET_DIR="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "Signed in") {
			t.Fatalf("agent-browser did not see the viewport popup: %v\n%s", err, out)
		}
	}

	fresh := startTestBrowser(t, exe, freshRoot, false)
	fresh.post(t, "/take", "", 204)
	fresh.post(t, "/input", fmt.Sprintf(`{"kind":"new","url":%q}`, login.URL+"/signed-in"), 204)
	fresh.awaitTitle(t, "Signed out")
	fresh.stop(t)
	files, err := os.ReadDir(freshRoot)
	if len(files) != 0 || err != nil {
		t.Fatalf("fresh profile stayed on disk: %v, %v", files, err)
	}
	named.stop(t)

	resumed := startTestBrowser(t, exe, root, true)
	resumed.post(t, "/take", "", 204)
	resumed.post(t, "/input", fmt.Sprintf(`{"kind":"new","url":%q}`, login.URL+"/signed-in"), 204)
	resumed.awaitTitle(t, "Signed in")
	resumed.stop(t)
}

type testBrowser struct {
	agent, viewport string
	cancel          context.CancelFunc
	done            <-chan error
}

func startTestBrowser(t *testing.T, executable, root string, persistent bool) testBrowser {
	t.Helper()
	listen := func() net.Listener {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		return l
	}
	agent, view := listen(), listen()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, agent, view, Options{Executable: executable, Root: root, Persistent: persistent, Token: "test-token"})
	}()
	b := testBrowser{agent: "http://" + agent.Addr().String(), viewport: "http://" + view.Addr().String(), cancel: cancel, done: done}
	b.request(t, "GET", "/health", "", 204)
	return b
}

func (b testBrowser) stop(t *testing.T) {
	t.Helper()
	b.cancel()
	select {
	case err := <-b.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("browser did not shut down")
	}
	if resp, err := http.Get(b.viewport + "/health"); err == nil {
		resp.Body.Close()
		t.Fatal("the viewport survived teardown")
	}
}

func (b testBrowser) request(t *testing.T, method, path, input string, status int) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, b.viewport+path, strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(TokenHeader, "test-token")
	req.Header.Set("X-Viewport-Client", "12345678-1234-1234-1234-123456789abc")
	resp, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != status {
		t.Fatalf("%s %s: HTTP %d %s: %v", method, path, resp.StatusCode, data, err)
	}
	return data
}

func (b testBrowser) get(t *testing.T, path string) []byte {
	t.Helper()
	return b.request(t, "GET", path, "", 200)
}

func (b testBrowser) post(t *testing.T, path, input string, status int) {
	t.Helper()
	b.request(t, "POST", path, input, status)
}

func (b testBrowser) tabs(t *testing.T) []target {
	t.Helper()
	var tabs []target
	if err := json.Unmarshal(b.get(t, "/tabs"), &tabs); err != nil {
		t.Fatal(err)
	}
	return tabs
}

func (b testBrowser) awaitTitle(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		tabs := b.tabs(t)
		for _, tab := range tabs {
			if tab.Title == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing tab %q: %+v", want, tabs)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (b testBrowser) endpoint(t *testing.T) string {
	t.Helper()
	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Get(b.agent + "/json/version")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ WebSocketDebuggerURL string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.WebSocketDebuggerURL == "" {
		t.Fatalf("CDP discovery: %+v, %v", out, err)
	}
	return out.WebSocketDebuggerURL
}
