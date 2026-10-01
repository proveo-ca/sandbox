//go:build e2e

// SPEC: _spec/internal/sbx/host-android-adb.puml, _spec/_experiments/mobile-device-reach.puml

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestHostADBReachesTheHostEmulatorFromTheSandbox(t *testing.T) {
	if _, err := exec.LookPath(sbx.Binary); err != nil {
		t.Skipf("%s not on PATH", sbx.Binary)
	}
	if ok, why := sbx.Available(); !ok {
		t.Skipf("sbx unavailable: %s", why)
	}
	if hostadb.EmulatorBinary(os.Getenv) == "" || hostadb.AdbBinary(os.Getenv) == "" {
		t.Skip("no Android SDK emulator/adb on this host")
	}
	image := env("PROVEO_HOST_ADB_IMAGE", "proveo/claudecode:local")
	if !dockerImagePresent(t, image) {
		t.Skipf("image %s not built (proveo build claudecode)", image)
	}
	port, err := hostadb.Port(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}

	booted := !hostadb.Ready(port)
	if err := hostadb.Ensure(os.Getenv, port, t.Logf); err != nil {
		t.Skipf("host emulator: %v", err)
	}
	if booted {
		t.Cleanup(func() {
			_ = exec.Command(hostadb.AdbBinary(os.Getenv), "-P", strconv.Itoa(port), "emu", "kill").Run()
		})
	}

	if err := sbx.EnsureTemplate(image, func(string, ...any) {}); err != nil {
		t.Skipf("sbx template for %s: %v", image, err)
	}
	name := fmt.Sprintf("proveo-adbsbx-%d", time.Now().UnixNano())
	if out, err := exec.Command(sbx.Binary, "create", "--name", name,
		"-t", image, sbx.BuiltinAgent("claudecode"), t.TempDir()).CombinedOutput(); err != nil {
		t.Skipf("sbx create: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(sbx.Binary, "rm", "--force", name).CombinedOutput(); err != nil {
			t.Logf("probe sandbox %s not removed: %v\n%s", name, err, out)
		}
	})
	if out, err := exec.Command(sbx.Binary, "policy", "allow", "network", "--sandbox", name,
		hostadb.PolicyHost(port)).CombinedOutput(); err != nil {
		t.Fatalf("policy allow: %v\n%s", err, out)
	}

	// The working-tree lib, not the baked one: the image lags the source until a rebuild.
	lib := filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh")
	if out, err := exec.Command(sbx.Binary, "cp", lib, name+":/tmp/entrypoint-lib.sh").CombinedOutput(); err != nil {
		t.Fatalf("sbx cp: %v\n%s", err, out)
	}

	probe := fmt.Sprintf(`set -e
export HOME=/tmp %s=%d
source /tmp/entrypoint-lib.sh
proveo_host_adb_env
echo "adb_host=$ADB_HOST adb_port=$ADB_PORT"
python3 - <<'PY'
import os, socket
s = socket.create_connection((os.environ["ADB_HOST"], int(os.environ["ADB_PORT"])), timeout=5)
q = b"host:devices"
s.sendall(b"%%04x%%s" %% (len(q), q))
print("status=" + s.recv(4).decode())
n = int(s.recv(4), 16)
print("devices=" + s.recv(n).decode().replace("\n", ";"))
PY
`, hostadb.EnvPort, port)
	ob, err := exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "bash", "-c", probe).CombinedOutput()
	out := string(ob)
	if err != nil {
		t.Fatalf("probe exec: %v\n%s", err, out)
	}
	if !strings.Contains(out, "status=OKAY") || !strings.Contains(out, "\tdevice") {
		t.Fatalf("the sandbox did not see a usable device on the host adb server:\n%s", out)
	}
	if !strings.Contains(out, "adb_port="+strconv.Itoa(port)) || strings.Contains(out, "adb_host= ") {
		t.Fatalf("proveo_host_adb_env did not export the gateway address:\n%s", out)
	}
	t.Logf("guest view: %s", strings.TrimSpace(out))

	session := fmt.Sprintf(`set -e
export HOME=/tmp %s=%d
source /tmp/entrypoint-lib.sh
proveo_host_adb_env
proveo_adb_mirror_start
python3 - <<'PY'
import json, subprocess, sys
p = subprocess.Popen(["bash", "-c", "source /tmp/entrypoint-lib.sh && proveo_mobile_mcp_exec"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
def call(i, method, params):
    p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": i, "method": method, "params": params}) + "\n"); p.stdin.flush()
    while True:
        m = json.loads(p.stdout.readline())
        if m.get("id") == i: return m["result"]
def tool(i, name, args):
    r = call(i, "tools/call", {"name": name, "arguments": args})
    kinds = [c["type"] for c in r.get("content", [])]
    text = " ".join(c.get("text", "") for c in r.get("content", []) if c["type"] == "text")
    print("%%s error=%%s kinds=%%s text=%%s" %% (name, r.get("isError", False), ",".join(kinds), text[:160].replace("\n", " ")))
    return text
call(1, "initialize", {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "proveo", "version": "1"}})
p.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n"); p.stdin.flush()
dev = json.loads(tool(2, "mobile_list_available_devices", {}))["devices"][0]["id"]
tool(3, "mobile_launch_app", {"device": dev, "packageName": "com.android.settings"})
tool(4, "mobile_list_elements_on_screen", {"device": dev})
tool(5, "mobile_click_on_screen_at_coordinates", {"device": dev, "x": 540, "y": 640})
tool(6, "mobile_take_screenshot", {"device": dev})
tool(7, "mobile_press_button", {"device": dev, "button": "HOME"})
p.terminate()
PY
`, hostadb.EnvPort, port)
	ob, err = exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "bash", "-c", session).CombinedOutput()
	out = string(ob)
	if err != nil {
		t.Fatalf("mobile-mcp session: %v\n%s", err, out)
	}
	for _, want := range []string{
		"mobile_launch_app error=False",
		"mobile_list_elements_on_screen error=False",
		"mobile_click_on_screen_at_coordinates error=False",
		"mobile_take_screenshot error=False kinds=text,image",
		"mobile_press_button error=False",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mobile-mcp session missing %q:\n%s", want, out)
		}
	}
	t.Logf("mobile-mcp session:\n%s", strings.TrimSpace(out))
	if !strings.Contains(out, "@e1") {
		t.Errorf("list_elements returned no element refs — the forwarded device server is unreachable:\n%s", out)
	}
}
