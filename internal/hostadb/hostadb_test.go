// SPEC: _spec/internal/sbx/host-android-adb.puml
package hostadb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func env(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

func TestPortDefaultsAndValidates(t *testing.T) {
	if p, err := Port(env(nil)); err != nil || p != DefaultPort {
		t.Errorf("unset = %d, %v; want %d", p, err, DefaultPort)
	}
	if p, err := Port(env(map[string]string{EnvPort: " 5038 "})); err != nil || p != 5038 {
		t.Errorf("5038 = %d, %v", p, err)
	}
	for _, bad := range []string{"0", "70000", "adb"} {
		if _, err := Port(env(map[string]string{EnvPort: bad})); err == nil {
			t.Errorf("%s=%q must be refused", EnvPort, bad)
		}
	}
}

func TestPolicyHostIsTheLoopbackServerPort(t *testing.T) {
	if got := PolicyHost(5037); got != "localhost:5037" {
		t.Errorf("PolicyHost = %q", got)
	}
}

// fakeServer answers host:devices with body over the adb smart-socket framing.
func fakeServer(t *testing.T, body string) (port int, seen *[]string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var got []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hdr := make([]byte, 4)
			if _, err := io.ReadFull(c, hdr); err == nil {
				n, _ := strconv.ParseUint(string(hdr), 16, 32)
				svc := make([]byte, n)
				_, _ = io.ReadFull(c, svc)
				got = append(got, string(svc))
				fmt.Fprintf(c, "OKAY%04x%s", len(body), body)
			}
			_ = c.Close()
		}
	}()
	orig := hostAddr
	t.Cleanup(func() { hostAddr = orig })
	addr := ln.Addr().String()
	hostAddr = func(int) string { return addr }
	return 1, &got
}

func TestDevicesKeepsOnlyUsableSerials(t *testing.T) {
	port, seen := fakeServer(t, "emulator-5554\tdevice\n127.0.0.1:5555\tunauthorized\nemulator-5556\toffline\n")
	got, err := Devices(port)
	if err != nil || !slices.Equal(got, []string{"emulator-5554"}) {
		t.Errorf("Devices = %v, %v", got, err)
	}
	if len(*seen) != 1 || (*seen)[0] != "host:devices" {
		t.Errorf("server saw %v; want one host:devices", *seen)
	}
}

func TestReadyNeedsAUsableDevice(t *testing.T) {
	fakeServer(t, "127.0.0.1:5555\tunauthorized\n")
	if Ready(1) {
		t.Error("an unauthorized device is not ready")
	}
}

func TestReadyIsFalseWithNoServer(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	orig := hostAddr
	t.Cleanup(func() { hostAddr = orig })
	hostAddr = func(int) string { return addr }
	if Ready(1) {
		t.Error("no listener must read as not ready")
	}
}

func TestSDKRootPrefersAndroidHome(t *testing.T) {
	if got := SDKRoot(env(map[string]string{"ANDROID_HOME": "/a", "ANDROID_SDK_ROOT": "/b"})); got != "/a" {
		t.Errorf("SDKRoot = %q; want /a", got)
	}
	if got := SDKRoot(env(map[string]string{"ANDROID_SDK_ROOT": "/b"})); got != "/b" {
		t.Errorf("SDKRoot = %q; want /b", got)
	}
}

func TestToolsResolveFromTheSDKBeforePath(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"emulator/emulator", "platform-tools/adb"} {
		_ = os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		_ = os.WriteFile(filepath.Join(root, p), nil, 0o755)
	}
	origLook := lookPath
	t.Cleanup(func() { lookPath = origLook })
	lookPath = func(string) (string, error) { t.Fatal("SDK hit must not fall through to PATH"); return "", nil }
	get := env(map[string]string{"ANDROID_HOME": root})
	if got := EmulatorBinary(get); got != filepath.Join(root, "emulator", "emulator") {
		t.Errorf("EmulatorBinary = %q", got)
	}
	if got := AdbBinary(get); got != filepath.Join(root, "platform-tools", "adb") {
		t.Errorf("AdbBinary = %q", got)
	}
}

func TestPickAVD(t *testing.T) {
	avds := []string{"Medium_Phone_API_36.1", "Pixel_9"}
	if got, err := PickAVD(env(nil), avds); err != nil || got != "Medium_Phone_API_36.1" {
		t.Errorf("default = %q, %v", got, err)
	}
	if got, err := PickAVD(env(map[string]string{EnvAVD: "Pixel_9"}), avds); err != nil || got != "Pixel_9" {
		t.Errorf("%s = %q, %v", EnvAVD, got, err)
	}
	if _, err := PickAVD(env(map[string]string{EnvAVD: "Nope"}), avds); err == nil {
		t.Error("an unknown AVD must be refused")
	}
	if _, err := PickAVD(env(nil), nil); err == nil {
		t.Error("no AVD must be an error")
	}
}

func TestParseAVDsSkipsEmulatorChatter(t *testing.T) {
	out := []byte("INFO    | Storing crashdata in: /tmp\nMedium_Phone_API_36.1\n\nPixel_9\n")
	if got := parseAVDs(out); !slices.Equal(got, []string{"Medium_Phone_API_36.1", "Pixel_9"}) {
		t.Errorf("parseAVDs = %v", got)
	}
}

func TestLaunchArgsAreWindowed(t *testing.T) {
	args := LaunchArgs("Pixel_9")
	if !slices.Equal(args[:2], []string{"-avd", "Pixel_9"}) {
		t.Errorf("args = %v", args)
	}
	if slices.Contains(args, "-no-window") {
		t.Error("the operator watches and logs in through the window")
	}
}

func TestEnsureReusesARunningDevice(t *testing.T) {
	fakeServer(t, "emulator-5554\tdevice\n")
	origLook, origStat := lookPath, statFile
	t.Cleanup(func() { lookPath, statFile = origLook, origStat })
	lookPath = func(string) (string, error) { t.Fatal("must not look for a tool to launch"); return "", nil }
	statFile = func(string) (os.FileInfo, error) { t.Fatal("must not look for a tool to launch"); return nil, nil }

	var said string
	if err := Ensure(env(nil), 1, func(f string, a ...any) { said = fmt.Sprintf(f, a...) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said, "reusing emulator-5554") {
		t.Errorf("report = %q", said)
	}
}

func TestEnsureExplainsAMissingAdb(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	origAddr, origLook, origStat := hostAddr, lookPath, statFile
	t.Cleanup(func() { hostAddr, lookPath, statFile = origAddr, origLook, origStat })
	hostAddr = func(int) string { return addr }
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }

	err := Ensure(env(map[string]string{"ANDROID_HOME": "/none"}), 1, nil)
	if err == nil || !strings.Contains(err.Error(), "no adb found") {
		t.Errorf("err = %v", err)
	}
}

func TestGuestEnvNamesTheHostByName(t *testing.T) {
	got := GuestEnv(5038)
	want := []string{EnvPort + "=5038", "ADB_SERVER_SOCKET=tcp:host.docker.internal:5038"}
	if !slices.Equal(got, want) {
		t.Errorf("GuestEnv = %v; want %v", got, want)
	}
}

func TestMCPConfigLaunchesThroughTheEntrypointLib(t *testing.T) {
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(MCPConfig), &cfg); err != nil {
		t.Fatalf("MCPConfig is not JSON: %v", err)
	}
	m, ok := cfg.MCPServers["mobile"]
	if !ok || m.Command != "bash" || len(m.Args) != 2 || !strings.Contains(m.Args[1], "proveo_mobile_mcp_exec") {
		t.Errorf("mobile server = %+v", m)
	}
}
