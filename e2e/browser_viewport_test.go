//go:build e2e

// SPEC: _spec/_plans/isolated-browser-profiles.puml

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/guestbrowser"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestIsolatedBrowserViewportAcrossHarnesses(t *testing.T) {
	requireSbx(t)
	requireTmux(t)
	proveoBin := buildProveo(t)
	for _, target := range []string{"claudecode", "codex", "cursor", "opencode", "hermes"} {
		t.Run(target, func(t *testing.T) {
			image := target
			if target != "hermes" {
				image += "-browser"
			}
			harnessImage(t, image)
			home, work := t.TempDir(), t.TempDir()
			k := launchKit(t, proveoBin, target, work, []string{"PROVEO_HOME=" + home}, "--addon", "browser")
			view := awaitBrowserViewport(t, home)
			assertSandboxBrowserPorts(t, k.name)
			if out, rc := k.exec("test -z \"$(find /var/lib/proveo/browser -name DevToolsActivePort -print -quit)\"", time.Minute); rc != 0 {
				t.Fatalf("Chromium started before browser use: %s", out)
			}
			const page = "data:text/html,<title>proveo-viewport-e2e</title>"
			switch target {
			case "hermes":
				openHermesViewportTab(t, k.name, page)
			default:
				if out, rc := k.exec("agent-browser open "+shellQuote([]string{page}), time.Minute); rc != 0 {
					t.Fatalf("agent-browser exited %d: %s", rc, out)
				}
			}
			awaitViewportTab(t, view, "proveo-viewport-e2e")
			if _, ok := k.end(); !ok {
				t.Fatal("run did not exit")
			}
			assertViewportClosed(t, view)
			if sbx.Exists(k.name) {
				if out, rc := k.exec("test -z \"$(find /var/lib/proveo/browser -name 'fresh-*' -print -quit)\"", time.Minute); rc != 0 {
					t.Fatalf("retained sandbox still holds fresh browser data: %s", out)
				}
			}
		})
	}
}

func TestBrowserRunFallsBackToPublishedImage(t *testing.T) {
	requireSbx(t)
	requireTmux(t)
	requireDocker(t)
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	proveoBin := buildProveo(t)
	bin, home, work := t.TempDir(), t.TempDir(), t.TempDir()
	pulled := filepath.Join(bin, "pulled")
	const dockerShim = `#!/bin/sh
case "$*" in
  'image inspect proveo/claudecode-browser:local') exit 0 ;;
  'image inspect proveo/claudecode-browser:latest') test -f "$BROWSER_TEST_PULL" || exit 1 ;;
  'image inspect --format {{.Created}} proveo/claudecode-browser:local') printf '%s\n' '2099-01-01T00:00:00Z'; exit 0 ;;
  'image inspect --format {{.Id}} proveo/claudecode-browser:local') printf '%s\n' 'stale-browser-image'; exit 0 ;;
  'run '*' stale-browser-image browser check') printf '%s\n' 'browser launcher missing'; exit 1 ;;
  'pull proveo/claudecode-browser:latest')
    "$BROWSER_TEST_DOCKER" "$@" && touch "$BROWSER_TEST_PULL"
    exit $? ;;
esac
exec "$BROWSER_TEST_DOCKER" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(dockerShim), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"BROWSER_TEST_DOCKER=" + docker, "BROWSER_TEST_PULL=" + pulled,
		"PROVEO_HOME=" + home, "PROVEO_AGENT_UPDATE=off", "PROVEO_AUTO_PROVISION=off",
	}
	k := launchKit(t, proveoBin, "claudecode", work, env, "--addon", "browser")
	if _, err := os.Stat(pulled); err != nil {
		t.Fatalf("the run did not pull the published image: %v", err)
	}
	log := readIn(filepath.Join(home, "logs"), "latest.log")
	if !strings.Contains(log, "browser launcher missing") {
		t.Fatalf("the run never rejected the stale local image\n%s", log)
	}
	awaitBrowserViewport(t, home)
	if _, ok := k.end(); !ok {
		t.Fatal("run did not exit")
	}
}

func TestNamedBrowserProfileResumesAndRejectsConcurrentUse(t *testing.T) {
	requireHarness(t, "claudecode-browser")
	proveoBin := buildProveo(t)
	home, work := t.TempDir(), t.TempDir()
	env := []string{"PROVEO_HOME=" + home, "XDG_CACHE_HOME=" + t.TempDir()}
	args := []string{"--browser-profile", "work"}
	first := launchKit(t, proveoBin, "claudecode", work, env, args...)
	view := awaitBrowserViewport(t, home)
	firstID := assertSandboxBrowserPorts(t, first.name)
	const cookie = "proveo_browser_e2e"
	if out, rc := first.exec(fmt.Sprintf("agent-browser cookies set %s persisted --url http://127.0.0.1/ --expires %d",
		cookie, time.Now().Add(time.Hour).Unix()), time.Minute); rc != 0 {
		t.Fatal(out)
	}
	result := func(argv ...string) (string, error) {
		cmd := exec.Command(proveoBin, argv...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := result("run", "claudecode", "--yes", "--input", work, "--browser-profile", "work"); err == nil || !strings.Contains(out, view) {
		t.Fatalf("competing run did not point at the existing viewport: %v\n%s", err, out)
	}
	if _, ok := first.end(); !ok || sbx.Running(first.name) || !sbx.Exists(first.name) {
		t.Fatalf("named sandbox %s did not stop and stay", first.name)
	}
	assertViewportClosed(t, view)
	if out, err := result("rm", first.name); err == nil || !strings.Contains(out, "--delete-browser-profile") {
		t.Fatalf("implicit profile deletion was accepted: %v\n%s", err, out)
	}
	second := launchKitWithExisting(t, proveoBin, "claudecode", work, env, first.name, args...)
	if next := awaitBrowserViewport(t, home); next == view {
		t.Fatal("resumed run reused an expired viewport URL")
	}
	if id := assertSandboxBrowserPorts(t, second.name); id == "" || id != firstID {
		t.Fatalf("named profile resumed in sbx ID %q, want %q", id, firstID)
	}
	if second.name != first.name {
		t.Fatalf("named profile moved from %s to %s", first.name, second.name)
	}
	if out, rc := second.exec("agent-browser cookies get", time.Minute); rc != 0 || !strings.Contains(out, cookie) {
		t.Fatalf("named login did not survive: exit %d\n%s", rc, out)
	}
	if _, ok := second.end(); !ok {
		t.Fatal("resumed run did not exit")
	}
	if out, err := result("rm", "--delete-browser-profile", first.name); err != nil {
		t.Fatalf("explicit profile deletion failed: %v\n%s", err, out)
	}
}

func awaitBrowserViewport(t *testing.T, home string) string {
	t.Helper()
	var address string
	client := &http.Client{Timeout: time.Second}
	if !waitUntil(time.Minute, func() bool {
		data, _ := os.ReadFile(filepath.Join(home, "logs", "latest.log"))
		address = viewportURL(string(data))
		if address == "" {
			return false
		}
		resp, err := client.Get(address + "health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusNoContent
	}) {
		t.Fatal("run log never pointed to a live viewport")
	}
	return address
}

var viewportLineRE = regexp.MustCompile(`browser viewport: (http://\S+)`)

func viewportURL(log string) string {
	m := viewportLineRE.FindStringSubmatch(log)
	if m == nil {
		return ""
	}
	return m[1]
}

func awaitViewportTab(t *testing.T, view, title string) {
	t.Helper()
	client := &http.Client{Timeout: 20 * time.Second}
	var last string
	if !waitUntil(45*time.Second, func() bool {
		resp, err := client.Get(view + "tabs")
		if err != nil {
			last = err.Error()
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		last = string(body)
		var tabs []struct{ Title, TargetID string }
		if json.Unmarshal(body, &tabs) == nil {
			for _, tab := range tabs {
				if strings.Contains(tab.Title, title) && tab.TargetID != "" {
					return true
				}
			}
		}
		return false
	}) {
		t.Fatalf("viewport tabs lack %q: %s", title, last)
	}
}

func assertSandboxBrowserPorts(t *testing.T, name string) string {
	t.Helper()
	rows, ok := sbx.List()
	if !ok {
		t.Fatal("sbx did not list its browser port bindings")
	}
	for _, row := range rows {
		if row.Name != name {
			continue
		}
		found := false
		for _, p := range row.Ports {
			switch p.SandboxPort {
			case guestbrowser.ViewportPort:
				found = true
				if p.HostPort <= 0 || !net.ParseIP(p.HostIP).IsLoopback() {
					t.Errorf("%s published the viewport outside host loopback: %+v", name, p)
				}
			case guestbrowser.AgentPort:
				t.Errorf("%s published its guest-only CDP port: %+v", name, p)
			}
		}
		if !found {
			t.Errorf("%s has no viewport binding: %+v", name, row.Ports)
		}
		return row.ID
	}
	t.Fatalf("sbx listed no sandbox named %s", name)
	return ""
}

func assertViewportClosed(t *testing.T, view string) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	if resp, err := client.Get(view + "health"); err == nil {
		resp.Body.Close()
		t.Fatalf("viewport still answers after teardown (HTTP %d)", resp.StatusCode)
	}
}
