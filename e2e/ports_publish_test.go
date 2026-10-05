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

// TestPortsPublishE2E ticks a discovered port once; proveo starts its server in the sandbox, publishes it,
// and a later --yes run on the same path publishes it again from memory.
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
	home := t.TempDir()

	work := t.TempDir()
	proc := fmt.Sprintf("web: python3 -m http.server %d\n", guestPort)
	if err := os.WriteFile(filepath.Join(work, "Procfile"), []byte(proc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "PORTS_OK"), []byte("ports-e2e\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "init", "-q", ".")
	mustRun(t, work, "git", "add", "-A")
	mustRun(t, work, "git", "-c", "user.email=e2e@proveo.test", "-c", "user.name=proveo e2e", "commit", "-q", "-m", "seed")

	first := startRun(t, proveoBin, home, image, work, "first")
	screen, err := first.WaitFor("confirm or change", time.Minute)
	if err != nil {
		t.Fatalf("choice form never drew: %v\n%s", err, screen)
	}
	for _, want := range []string{"execution", "OS", "ports", fmt.Sprintf("%d python", guestPort)} {
		if !strings.Contains(screen, want) {
			t.Errorf("choice form lacks %q:\n%s", want, screen)
		}
	}
	for i := 0; i < 20 && !strings.Contains(screen, "› ports"); i++ {
		_ = first.SendKeys("Down")
		time.Sleep(150 * time.Millisecond)
		screen, _ = first.Capture()
	}
	if !strings.Contains(screen, "› ports") {
		t.Fatalf("cursor never reached the ports row:\n%s", screen)
	}
	_ = first.SendKeys("Space")
	_ = first.Enter()
	assertServed(t, first)
	_ = first.SendText("exit")
	_ = first.Enter()
	if _, ok := waitSessionExit(first, 2*time.Minute); !ok {
		t.Fatal("first run never exited")
	}

	second := startRun(t, proveoBin, home, image, work, "second", "--yes")
	assertServed(t, second)
}

func startRun(t *testing.T, proveoBin, home, image, work, tag string, extra ...string) *tmux.Session {
	t.Helper()
	sess := tmux.New(fmt.Sprintf("proveo-ports-%s-%d", tag, os.Getpid()), nil)
	t.Cleanup(sess.Kill)
	args := append([]string{"env", "PROVEO_HOME=" + home, proveoBin, "run", "claudecode",
		"--image", image, "--input", work, "--scope", ".", "--shell"}, extra...)
	if err := sess.Start(160, 50, args...); err != nil {
		t.Fatalf("start tmux session: %v", err)
	}
	return sess
}

// assertServed waits for the publish report, then GETs the sandbox's own server from the host.
func assertServed(t *testing.T, sess *tmux.Session) {
	t.Helper()
	if _, err := sess.WaitFor(fmt.Sprintf("→ sandbox :%d", guestPort), 3*time.Minute); err != nil {
		screen, _ := sess.CaptureAll()
		t.Fatalf("publish never reported: %v\n%s", err, screen)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/PORTS_OK", hostPortFor(t, sess))
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if body, ok := get(url); ok && strings.Contains(body, "ports-e2e") {
			return
		}
		if time.Now().After(deadline) {
			screen, _ := sess.CaptureAll()
			t.Fatalf("GET %s never reached the server proveo started:\n%s", url, screen)
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
