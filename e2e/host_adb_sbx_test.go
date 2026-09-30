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

	if os.Getenv("PROVEO_ARTEMIS_E2E") != "1" {
		t.Log("artemis MCP handshake skipped: set PROVEO_ARTEMIS_E2E=1 (first install downloads several hundred MB)")
		return
	}
	mcp := fmt.Sprintf(`set -e
export HOME=/tmp %s=%d
source /tmp/entrypoint-lib.sh
proveo_host_adb_env
cfg="$(proveo_artemis_mcp_config)"
py="$(printf '%%s' "$cfg" | python3 -c 'import json,sys; print(json.load(sys.stdin)["mcpServers"]["artemis"]["command"])')"
printf '%%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"proveo","version":"1"}}}' \
  | timeout 120 "$py" -m mcp_server 2>/dev/null | head -c 4096
`, hostadb.EnvPort, port)
	ob, err = exec.Command(sbx.Binary, "exec", "-w", "/", name, "--", "bash", "-c", mcp).CombinedOutput()
	out = string(ob)
	if err != nil {
		t.Fatalf("artemis exec: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"serverInfo"`) {
		t.Fatalf("artemis did not answer an MCP initialize:\n%s", out)
	}
}
