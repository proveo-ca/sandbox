//go:build e2e

// SPEC: _spec/defs/claudecode/chrome-bridge.puml, _spec/internal/sbx/sandbox-backend.puml

package e2e

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/chromebridge"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// The sbx twin of TestChromeBridgeCarriesTheHostSocketIntoTheContainer. Asserts
// the two things that differ there: the relay survives the startup exec that
// backgrounds it, and the connect comes from a SEPARATE exec.
//
//	go test -tags=e2e ./e2e/ -run ChromeBridgeOnSbx -v
func TestChromeBridgeOnSbxReachesTheHostFromTheSandbox(t *testing.T) {
	if _, err := exec.LookPath(sbx.Binary); err != nil {
		t.Skipf("%s not on PATH", sbx.Binary)
	}
	if ok, why := sbx.Available(); !ok {
		t.Skipf("sbx unavailable: %s", why)
	}
	image := env("PROVEO_CHROME_BRIDGE_IMAGE", "proveo/claudecode:local")
	if !dockerImagePresent(t, image) {
		t.Skipf("image %s not built (proveo build claudecode)", image)
	}

	// The native host's end: an echo server on the socket Claude Code's own
	// native messaging host would hold.
	dir, err := os.MkdirTemp("/tmp", "cbsbx")
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

	// Loopback, deliberately: the point of the measurement is that a sandbox
	// reaches the host WITHOUT the relay being exposed beyond 127.0.0.1.
	relay, err := chromebridge.Start("127.0.0.1:0", dir, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	if err := relay.SetTokenEnv(); err != nil {
		t.Fatal(err)
	}

	// sbx keeps its OWN image store, so the harness image has to be handed over
	// before a Kit can name it — the same hop `proveo run` makes.
	if err := sbx.EnsureTemplate(image, func(string, ...any) {}); err != nil {
		t.Skipf("sbx template for %s: %v", image, err)
	}
	ws := t.TempDir()
	name := fmt.Sprintf("proveo-cbsbx-%d", time.Now().UnixNano())
	if out, err := exec.Command(sbx.Binary, "create", "--name", name,
		"-t", image, sbx.BuiltinAgent("claudecode"), ws).CombinedOutput(); err != nil {
		t.Skipf("sbx create: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(sbx.Binary, "rm", "--force", name).CombinedOutput(); err != nil {
			t.Logf("probe sandbox %s not removed: %v\n%s", name, err, out)
		}
	})

	// 1. The startup-shaped exec: the seed's helper backgrounds the relay, then
	//    this exec exits. Env is passed here because the Kit's env is what
	//    carries it in a real run.
	start := fmt.Sprintf(`set -e
export HOME=/tmp
export %s=%q
export %s=%q
source /entrypoint-lib.sh
proveo_chrome_bridge claudecode
head -n1 /tmp/proveo-chrome-bridge.sock-path
`, chromebridge.EnvAddr, relay.ContainerAddr(), chromebridge.EnvToken, relay.Token())
	ob, err := exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "bash", "-c", start).CombinedOutput()
	out := string(ob)
	if err != nil {
		t.Fatalf("startup exec: %v\n%s", err, out)
	}
	if !strings.Contains(out, "🧭 chrome: Claude in Chrome via the host browser") {
		t.Fatalf("the relay did not report itself up:\n%s", out)
	}
	sock := ""
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "/") {
			sock = strings.TrimSpace(l)
		}
	}
	if sock == "" {
		t.Fatalf("no socket path on stdout:\n%s", out)
	}
	t.Logf("relay socket inside the sandbox: %s", sock)

	// 2. A SEPARATE exec, after the first one exited: this is the assertion that
	//    the backgrounded relay survived, and it stands exactly where Claude
	//    Code's claude-in-chrome MCP server connects.
	probe := fmt.Sprintf(`set -e
test -S %q || { echo "socket gone after the startup exec exited"; exit 4; }
node -e '
  const net = require("net");
  const c = net.connect(process.argv[1], () => c.write("ping-from-claude-code"));
  c.setTimeout(15000, () => { console.error("timeout waiting for the echo"); process.exit(3); });
  c.on("data", d => { process.stdout.write("reply=" + d.toString() + "\n"); c.end(); });
  c.on("error", e => { console.error("client error", e.message); process.exit(2); });
' %q
`, sock, sock)
	ob, err = exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "bash", "-c", probe).CombinedOutput()
	out = string(ob)
	if err != nil {
		t.Fatalf("probe exec: %v\n%s", err, out)
	}
	if !strings.Contains(out, "reply=native-host-echo:ping-from-claude-code") {
		t.Fatalf("the five hops did not carry the bytes:\n%s", out)
	}

	// 3. The token is what stands between anything else on the machine and the
	//    operator's browser. A connection that does not present it is closed
	//    before a byte reaches the native host.
	bad := fmt.Sprintf(`set -e
node -e '
  const net = require("net");
  const c = net.connect({host: %q, port: %s}, () => c.write("wrong-token\n"));
  let got = "";
  c.setTimeout(10000, () => { console.log("closed=timeout"); process.exit(0); });
  c.on("data", d => { got += d.toString(); });
  c.on("close", () => { console.log("closed=1 got=" + JSON.stringify(got)); });
  c.on("error", () => { console.log("closed=1 got=error"); });
'
`, relayHost(relay.ContainerAddr()), relayPort(relay.ContainerAddr()))
	ob, err = exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "bash", "-c", bad).CombinedOutput()
	out = string(ob)
	if err != nil {
		t.Fatalf("token probe: %v\n%s", err, out)
	}
	if strings.Contains(out, "native-host-echo") {
		t.Fatalf("an unauthenticated connection reached the native host:\n%s", out)
	}
	t.Logf("unauthenticated connection refused: %s", strings.TrimSpace(out))
}

func relayHost(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return h
}

func relayPort(addr string) string {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "0"
	}
	return p
}
