//go:build e2e

// SPEC: _spec/internal/devports/dev-ports.puml

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/tmux"
)

const guestPort = 4310

// TestPortsPublishE2E ticks a discovered dev port in the form and reaches the sandbox's server from the host.
func TestPortsPublishE2E(t *testing.T) {
	requireTmux(t)
	if _, err := exec.LookPath(sbx.Binary); err != nil {
		t.Skipf("%s not on PATH", sbx.Binary)
	}
	if ok, why := sbx.Available(); !ok {
		t.Skipf("sbx unavailable: %s", why)
	}
	image := harnessImage(t, "claudecode")
	proveoBin := buildProveo(t)

	work := t.TempDir()
	pkg := fmt.Sprintf(`{"scripts":{"dev":"python3 -m http.server %d --bind 0.0.0.0"}}`, guestPort)
	if err := os.WriteFile(filepath.Join(work, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "PORTS_OK"), []byte("ports-e2e\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "add", "-A")
	mustRun(t, work, "git", "-c", "user.email=e2e@proveo.test", "-c", "user.name=proveo e2e", "commit", "-q", "-m", "seed")

	sess := tmux.New(fmt.Sprintf("proveo-ports-%d", os.Getpid()), nil)
	t.Cleanup(sess.Kill)
	if err := sess.Start(160, 50, "env", "PROVEO_HOME="+t.TempDir(), proveoBin, "run", "claudecode",
		"--image", image, "--input", work, "--scope", ".", "--shell"); err != nil {
		t.Fatalf("start tmux session: %v", err)
	}
	screen, err := sess.WaitFor("confirm or change", time.Minute)
	if err != nil {
		t.Fatalf("choice form never drew: %v\n%s", err, screen)
	}
	for _, want := range []string{"execution", "OS", "ports", fmt.Sprintf("%d python", guestPort)} {
		if !strings.Contains(screen, want) {
			t.Errorf("choice form lacks %q:\n%s", want, screen)
		}
	}
	for i := 0; i < 20 && !strings.Contains(screen, "› ports"); i++ {
		_ = sess.SendKeys("Down")
		time.Sleep(150 * time.Millisecond)
		screen, _ = sess.Capture()
	}
	if !strings.Contains(screen, "› ports") {
		t.Fatalf("cursor never reached the ports row:\n%s", screen)
	}
	_ = sess.SendKeys("Space")
	_ = sess.Enter()

	if _, err := sess.WaitFor(fmt.Sprintf("→ sandbox :%d", guestPort), 3*time.Minute); err != nil {
		screen, _ = sess.CaptureAll()
		t.Fatalf("publish never reported: %v\n%s", err, screen)
	}
	if err := sess.SendText(fmt.Sprintf("cd \"$(git rev-parse --show-toplevel 2>/dev/null || pwd)\" && python3 -m http.server %d --bind 0.0.0.0", guestPort)); err != nil {
		t.Fatal(err)
	}
	_ = sess.Enter()

	url := fmt.Sprintf("http://127.0.0.1:%d/PORTS_OK", hostPortFor(t, sess))
	deadline := time.Now().Add(time.Minute)
	for {
		if body, ok := get(url); ok && strings.Contains(body, "ports-e2e") {
			return
		}
		if time.Now().After(deadline) {
			screen, _ = sess.CaptureAll()
			t.Fatalf("GET %s never reached the sandbox server:\n%s", url, screen)
		}
		time.Sleep(time.Second)
	}
}

func hostPortFor(t *testing.T, sess *tmux.Session) int {
	t.Helper()
	screen, _ := sess.CaptureAll()
	for _, l := range strings.Split(screen, "\n") {
		var host, guest int
		i := strings.Index(l, "http://127.0.0.1:")
		if i < 0 {
			continue
		}
		if _, err := fmt.Sscanf(l[i:], "http://127.0.0.1:%d → sandbox :%d", &host, &guest); err == nil && guest == guestPort {
			return host
		}
	}
	t.Fatalf("no published host port for :%d on screen:\n%s", guestPort, screen)
	return 0
}

func get(url string) (string, bool) {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.StatusCode == http.StatusOK
}
