//go:build e2e

// SPEC: _spec/defs/claudecode/chrome-bridge.puml

package e2e

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/chromebridge"
)

// The whole Claude in Chrome bridge minus the two ends that need a human: bytes
// written by a client inside the container must come back through all five hops.
func TestChromeBridgeCarriesTheHostSocketIntoTheContainer(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	image := env("PROVEO_CHROME_BRIDGE_IMAGE", "proveo/claudecode:local")
	if !dockerImagePresent(t, image) {
		t.Skipf("image %s not built (proveo build claudecode)", image)
	}

	dir, err := os.MkdirTemp("/tmp", "cbe2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "4242.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, err := c.Write(append([]byte("native-host-echo:"), buf[:n]...)); err != nil {
						return
					}
				}
			}()
		}
	}()

	relay, err := chromebridge.Start(chromebridge.BindAddr(), dir, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	if err := relay.SetTokenEnv(); err != nil {
		t.Fatal(err)
	}

	// What the container does once proveo run has set the two variables: start
	// the relay through the seed helper, then connect where Claude Code would.
	script := `set -e
export HOME=/tmp
source /entrypoint-lib.sh
proveo_chrome_bridge claudecode
sock="$(head -n1 /tmp/proveo-chrome-bridge.sock-path)"
echo "sock=$sock"
node -e '
  const net = require("net");
  const c = net.connect(process.argv[1], () => c.write("ping-from-claude-code"));
  c.setTimeout(8000, () => { console.error("timeout waiting for the echo"); process.exit(3); });
  c.on("data", d => { process.stdout.write("reply=" + d.toString() + "\n"); c.end(); });
  c.on("error", e => { console.error("client error", e.message); process.exit(2); });
' "$sock"
`
	args := []string{"run", "--rm", "--network=bridge",
		"--add-host=host.docker.internal:host-gateway",
		"--user", "1000:1000", "-e", "HOME=/tmp",
		"-e", chromebridge.EnvAddr + "=" + relay.ContainerAddr(),
		"-e", chromebridge.EnvToken, // bare: forwarded from this process's environment
		"--entrypoint", "bash", image, "-c", script}
	cmd := exec.Command("docker", args...)
	cmd.Env = os.Environ()
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() { out, runErr = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("container did not finish in 90s")
	}
	got := string(out)
	if runErr != nil {
		t.Fatalf("docker run: %v\n%s", runErr, got)
	}
	for _, want := range []string{
		"🧭 chrome: Claude in Chrome via the host browser",
		"sock=/tmp/claude-mcp-browser-bridge-",
		"reply=native-host-echo:ping-from-claude-code",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in container output:\n%s", want, got)
		}
	}
}
