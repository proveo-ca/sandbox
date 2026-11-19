//go:build e2e && !windows

// SPEC: _spec/internal/sbx/host-browser-cdp.puml
package e2e

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/hostdev"
)

func TestCodexUsesHostChromeFromSbx(t *testing.T) {
	requireHarness(t, "codex")
	const image = "chromedp/headless-shell:latest"
	if !dockerImagePresent(t, image) {
		t.Skipf("pull %s to run the host Chrome fixture", image)
	}
	const page = "data:text/html,proveo-host-chrome-fixture"
	id, err := exec.Command("docker", "run", "-d", "--rm", "-p", "127.0.0.1::9222", image, page).CombinedOutput()
	if err != nil {
		t.Fatalf("start host Chrome fixture: %v\n%s", err, id)
	}
	container := strings.TrimSpace(string(id))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	published, err := exec.Command("docker", "port", container, "9222/tcp").CombinedOutput()
	if err != nil {
		t.Fatalf("find host Chrome port: %v\n%s", err, published)
	}
	_, portText, err := net.SplitHostPort(strings.TrimSpace(string(published)))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if !waitUntil(15*time.Second, func() bool { return hostdev.CDPReady(port) }) {
		t.Fatal("host Chrome fixture did not answer CDP discovery")
	}

	env := append(isolatedHome(t), hostdev.EnvCDPPort+"="+portText)
	k := launchKit(t, buildProveo(t), "codex", t.TempDir(), env, "--clone=false", "--addon", "chrome")
	out, status := k.exec("agent-browser --json tab list", 30*time.Second)
	if status != 0 || !strings.Contains(out, page) {
		t.Fatalf("Codex did not list the host Chrome tab (exit %d)\n%s", status, out)
	}
}
