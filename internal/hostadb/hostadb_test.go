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
	"sync"
	"testing"
	"time"
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

// adbServer is a fake host adb server: the nth host:devices request reads bodies[n], the last one repeats.
type adbServer struct {
	mu     sync.Mutex
	bodies []string
	seen   []string
}

func (s *adbServer) Seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

func (s *adbServer) answer(svc string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, svc)
	return s.bodies[min(len(s.seen), len(s.bodies))-1]
}

// fakeServer answers host:devices over the adb smart-socket framing.
func fakeServer(t *testing.T, bodies ...string) (port int, srv *adbServer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv = &adbServer{bodies: bodies}
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
				body := srv.answer(string(svc))
				fmt.Fprintf(c, "OKAY%04x%s", len(body), body)
			}
			_ = c.Close()
		}
	}()
	orig := hostAddr
	t.Cleanup(func() { hostAddr = orig })
	addr := ln.Addr().String()
	hostAddr = func(int) string { return addr }
	return 1, srv
}

func TestDevicesKeepsOnlyUsableSerials(t *testing.T) {
	port, srv := fakeServer(t, "emulator-5554\tdevice\n127.0.0.1:5555\tunauthorized\nemulator-5556\toffline\n")
	got, err := Devices(port)
	if err != nil || !slices.Equal(got, []string{"emulator-5554"}) {
		t.Errorf("Devices = %v, %v", got, err)
	}
	if seen := srv.Seen(); len(seen) != 1 || seen[0] != "host:devices" {
		t.Errorf("server saw %v; want one host:devices", seen)
	}
}

func TestAllDevicesKeepsEveryState(t *testing.T) {
	port, _ := fakeServer(t, "emulator-5554\tdevice\n127.0.0.1:5555\tunauthorized\nemulator-5556\toffline\n")
	got, err := AllDevices(port)
	want := []Device{{"emulator-5554", "device"}, {"127.0.0.1:5555", "unauthorized"}, {"emulator-5556", "offline"}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("AllDevices = %v, %v; want %v", got, err, want)
	}
	if got[1].Emulator() || !got[2].Emulator() {
		t.Errorf("Emulator() must hold for emulator-* serials only: %v", got)
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

// hostTools stubs every adb and emulator command Ensure runs, and counts them.
type hostTools struct {
	env                                 func(string) string
	starts, reconnects, boots, launches int
	boot                                func(serial string) string
	exit                                error // non-nil: the launched emulator exits with it at once
	said                                []string
}

func (h *hostTools) report(f string, a ...any) { h.said = append(h.said, fmt.Sprintf(f, a...)) }

func stubHostTools(t *testing.T) *hostTools {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{"emulator/emulator", "platform-tools/adb"} {
		_ = os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		_ = os.WriteFile(filepath.Join(root, p), nil, 0o755)
	}
	oList, oStart, oRe, oBoot, oEmu, oPoll, oWait := listAVDs, startAdb, reconnect, bootProp, startEmulator, pollEvery, readyWait
	t.Cleanup(func() {
		listAVDs, startAdb, reconnect, bootProp, startEmulator, pollEvery, readyWait = oList, oStart, oRe, oBoot, oEmu, oPoll, oWait
	})
	h := &hostTools{env: env(map[string]string{"ANDROID_HOME": root}), boot: func(string) string { return "1" }}
	listAVDs = func(string) ([]byte, error) { return []byte("Pixel_9\n"), nil }
	startAdb = func(string, int) error { h.starts++; return nil }
	reconnect = func(string, int) error { h.reconnects++; return nil }
	bootProp = func(_ string, _ int, serial string) string { h.boots++; return h.boot(serial) }
	startEmulator = func(string, string, int) (<-chan error, error) {
		h.launches++
		exited := make(chan error, 1)
		if h.exit != nil {
			exited <- h.exit
		}
		return exited, nil
	}
	pollEvery, readyWait = time.Millisecond, 5*time.Second
	return h
}

func TestEnsureReusesARunningDeviceOnceItsBootHolds(t *testing.T) {
	fakeServer(t, "emulator-5554\tdevice\n")
	h := stubHostTools(t)
	if err := Ensure(h.env, 1, h.report); err != nil {
		t.Fatal(err)
	}
	if h.starts != 0 || h.launches != 0 {
		t.Errorf("a listed device must be reused: start-server=%d launches=%d", h.starts, h.launches)
	}
	if h.boots != StablePolls {
		t.Errorf("boot checks = %d; want %d in a row", h.boots, StablePolls)
	}
	if last := h.said[len(h.said)-1]; !strings.Contains(last, "reusing emulator-5554") {
		t.Errorf("report = %q", h.said)
	}
}

func TestEnsureReusesARunningDeviceWithoutAdb(t *testing.T) {
	fakeServer(t, "emulator-5554\tdevice\n")
	origLook, origStat, origBoot := lookPath, statFile, bootProp
	t.Cleanup(func() { lookPath, statFile, bootProp = origLook, origStat, origBoot })
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	bootProp = func(string, int, string) string { t.Fatal("no adb, no boot check"); return "" }

	var said string
	if err := Ensure(env(nil), 1, func(f string, a ...any) { said = fmt.Sprintf(f, a...) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said, "reusing emulator-5554") {
		t.Errorf("report = %q", said)
	}
}

func TestEnsureWaitsForABootThatHoldsAfterLaunch(t *testing.T) {
	fakeServer(t, "", "", "emulator-5554\tdevice\n", "emulator-5554\toffline\n", "emulator-5554\tdevice\n")
	h := stubHostTools(t)
	if err := Ensure(h.env, 1, h.report); err != nil {
		t.Fatal(err)
	}
	if h.starts != 1 || h.launches != 1 {
		t.Errorf("start-server=%d launches=%d; want one each", h.starts, h.launches)
	}
	if h.reconnects != 1 {
		t.Errorf("reconnects = %d; want one for the offline blip", h.reconnects)
	}
	if h.boots != 1+StablePolls {
		t.Errorf("boot checks = %d; want 1 before the blip and %d in a row after it", h.boots, StablePolls)
	}
	if last := h.said[len(h.said)-1]; last != "android: emulator-5554 booted" {
		t.Errorf("report = %q", h.said)
	}
}

func TestEnsureReconnectsAnOfflineEmulatorInsteadOfLaunchingAnother(t *testing.T) {
	fakeServer(t, "emulator-5554\toffline\n", "emulator-5554\toffline\n", "emulator-5554\tdevice\n")
	h := stubHostTools(t)
	if err := Ensure(h.env, 1, h.report); err != nil {
		t.Fatal(err)
	}
	if h.launches != 0 || h.starts != 0 {
		t.Errorf("an offline emulator must not get a second one: launches=%d start-server=%d", h.launches, h.starts)
	}
	if h.reconnects != 1 {
		t.Errorf("reconnects = %d; want 1", h.reconnects)
	}
	if !strings.Contains(h.said[0], "emulator-5554 offline") || !strings.Contains(h.said[len(h.said)-1], "reusing emulator-5554") {
		t.Errorf("report = %q", h.said)
	}
}

func TestEnsureReportsAnEmulatorThatExitsBeforeBoot(t *testing.T) {
	fakeServer(t, "")
	h := stubHostTools(t)
	h.exit = errors.New("exit status 1")
	err := Ensure(h.env, 1, h.report)
	if err == nil || !strings.Contains(err.Error(), "Pixel_9") || !strings.Contains(err.Error(), "exited before it booted") {
		t.Errorf("err = %v", err)
	}
	if h.launches != 1 {
		t.Errorf("launches = %d; want 1", h.launches)
	}
}

func TestEnsureGivesUpOnABootThatNeverHolds(t *testing.T) {
	fakeServer(t, "", "", "emulator-5554\tdevice\n")
	h := stubHostTools(t)
	readyWait = 100 * time.Millisecond
	h.boot = func(string) string { return strconv.Itoa(h.boots % 2) }
	err := Ensure(h.env, 1, h.report)
	if err == nil || !strings.Contains(err.Error(), "polls in a row") {
		t.Errorf("err = %v", err)
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

func TestOpencodeLaunchUsesNativeV2MobileMCP(t *testing.T) {
	launch := Launches["opencode"]
	if !Supports("opencode") || len(launch.Flags) != 0 {
		t.Fatalf("OpenCode must take mobile MCP through config: %+v", launch)
	}
	content, err := json.Marshal(launch.Opencode)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers map[string]struct {
			Type     string   `json:"type"`
			Command  []string `json:"command"`
			Disabled bool     `json:"disabled"`
		} `json:"servers"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		t.Fatalf("OpenCode MCP config is not native v2 JSON: %v\n%s", err, content)
	}
	m, ok := cfg.Servers["mobile"]
	want := []string{"bash", "-c", "source /entrypoint-lib.sh && proveo_mobile_mcp_exec"}
	if len(cfg.Servers) != 1 || !ok || m.Type != "local" || m.Disabled || !slices.Equal(m.Command, want) {
		t.Errorf("mobile MCP server = %+v", cfg.Servers)
	}
}
