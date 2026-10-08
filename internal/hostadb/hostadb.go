// SPEC: _spec/internal/sbx/host-android-adb.puml
package hostadb

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	Addon       = "android (host emulator)"
	AddonIOS    = "ios (host simulator)"
	IOSWhy      = "coming soon — not built yet; iOS Simulators also need a macOS host with Xcode"
	EnvPort     = "PROVEO_HOST_ADB_PORT"
	EnvAVD      = "PROVEO_HOST_AVD"
	DefaultPort = 5037
)

// MCPConfig is the --mcp-config JSON that launches mobile-mcp through the image's entrypoint lib.
const MCPConfig = `{"mcpServers":{"mobile":{"command":"bash","args":["-c","source /entrypoint-lib.sh && proveo_mobile_mcp_exec"]}}}`

// mcpCommand is the mobile server's command line inside the sandbox.
var mcpCommand = []string{"bash", "-c", "source /entrypoint-lib.sh && proveo_mobile_mcp_exec"}

// Launch is how one harness takes the mobile MCP server for a single launch.
type Launch struct {
	Flags    []string       // prepended to the agent command
	Opencode map[string]any // merged into OPENCODE_CONFIG_CONTENT under "mcp"
	Why      string         // set: the harness gets no android row
}

// Launches is every harness's row; a harness absent here gets no android row either.
var Launches = map[string]Launch{
	"claudecode": {Flags: []string{"--mcp-config", MCPConfig}},
	"opencode": {Opencode: map[string]any{
		"servers": map[string]any{
			"mobile": map[string]any{"type": "local", "command": mcpCommand, "disabled": false},
		},
	}},
	"codex":  {Why: "not wired yet: codex takes -c mcp_servers.mobile.* per launch"},
	"cecli":  {Why: "not wired yet: cecli takes --mcp-servers / CECLI_MCP_SERVERS per launch"},
	"cursor": {Why: "cursor-agent reads MCP servers only from a persistent ~/.cursor/mcp.json"},
	"hermes": {Why: "hermes reads MCP servers only from its persistent config.yaml"},
}

// Supports reports whether harness has a per-launch way to take the mobile server.
func Supports(harness string) bool {
	l, ok := Launches[harness]
	return ok && l.Why == ""
}

// GuestEnv is what the agent's own processes need to reach the host adb server by name.
func GuestEnv(port int) []string {
	return []string{
		fmt.Sprintf("%s=%d", EnvPort, port),
		fmt.Sprintf("ADB_SERVER_SOCKET=tcp:host.docker.internal:%d", port),
	}
}

// InstallHosts is what the guest needs to install the pinned mobile-mcp.
var InstallHosts = []string{"registry.npmjs.org"}

var (
	hostAddr  = func(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }
	readyWait = 3 * time.Minute
	homeDir   = os.UserHomeDir
	lookPath  = exec.LookPath
	statFile  = os.Stat
	listAVDs  = func(emulator string) ([]byte, error) { return exec.Command(emulator, "-list-avds").Output() }
	startAdb  = func(adb string, port int) error {
		return exec.Command(adb, "-P", strconv.Itoa(port), "start-server").Run()
	}
	bootProp = func(adb string, port int, serial string) string {
		out, _ := exec.Command(adb, "-P", strconv.Itoa(port), "-s", serial, "shell", "getprop", "sys.boot_completed").Output()
		return strings.TrimSpace(string(out))
	}
)

// Port is the host adb server port: PROVEO_HOST_ADB_PORT, else 5037.
func Port(getenv func(string) string) (int, error) {
	v := strings.TrimSpace(getenv(EnvPort))
	if v == "" {
		return DefaultPort, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%s=%q is not a TCP port", EnvPort, v)
	}
	return n, nil
}

// PolicyHost is the sbx allow rule for the host adb server.
func PolicyHost(port int) string { return fmt.Sprintf("localhost:%d", port) }

// query sends one adb host service request and returns its payload.
func query(port int, service string) (string, error) {
	c, err := net.DialTimeout("tcp", hostAddr(port), 2*time.Second)
	if err != nil {
		return "", err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := fmt.Fprintf(c, "%04x%s", len(service), service); err != nil {
		return "", err
	}
	status := make([]byte, 4)
	if _, err := io.ReadFull(c, status); err != nil {
		return "", err
	}
	size := make([]byte, 4)
	if _, err := io.ReadFull(c, size); err != nil {
		return "", err
	}
	n, err := strconv.ParseUint(string(size), 16, 32)
	if err != nil {
		return "", fmt.Errorf("adb: bad length %q", size)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(c, body); err != nil {
		return "", err
	}
	if string(status) != "OKAY" {
		return "", fmt.Errorf("adb %s: %s", service, body)
	}
	return string(body), nil
}

// Devices lists the serials the host adb server reports in state "device".
func Devices(port int) ([]string, error) {
	body, err := query(port, "host:devices")
	if err != nil {
		return nil, err
	}
	var out []string
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[1] == "device" {
			out = append(out, f[0])
		}
	}
	return out, nil
}

// Ready reports whether the host adb server lists at least one usable device.
func Ready(port int) bool {
	d, err := Devices(port)
	return err == nil && len(d) > 0
}

// SDKRoot is ANDROID_HOME, else ANDROID_SDK_ROOT, else the Android Studio default for this OS.
func SDKRoot(getenv func(string) string) string {
	for _, k := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
	}
	home, err := homeDir()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Android", "sdk")
	}
	return filepath.Join(home, "Android", "Sdk")
}

func sdkTool(getenv func(string) string, rel, name string) string {
	if root := SDKRoot(getenv); root != "" {
		p := filepath.Join(root, rel, name)
		if fi, err := statFile(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := lookPath(name); err == nil {
		return p
	}
	return ""
}

// EmulatorBinary finds the Android emulator in the SDK, else on PATH.
func EmulatorBinary(getenv func(string) string) string {
	return sdkTool(getenv, "emulator", "emulator")
}

// AdbBinary finds adb in the SDK, else on PATH.
func AdbBinary(getenv func(string) string) string { return sdkTool(getenv, "platform-tools", "adb") }

// PickAVD is PROVEO_HOST_AVD when it names an existing AVD, else the first one listed.
func PickAVD(getenv func(string) string, avds []string) (string, error) {
	if want := strings.TrimSpace(getenv(EnvAVD)); want != "" {
		if !slices.Contains(avds, want) {
			return "", fmt.Errorf("%s=%q is not an AVD on this host (have: %s)", EnvAVD, want, strings.Join(avds, ", "))
		}
		return want, nil
	}
	if len(avds) == 0 {
		return "", fmt.Errorf("no Android Virtual Device on this host — create one in Android Studio's Device Manager")
	}
	return avds[0], nil
}

// LaunchArgs opens a windowed emulator on avd, so the operator can watch and log in.
func LaunchArgs(avd string) []string { return []string{"-avd", avd} }

func parseAVDs(out []byte) []string {
	var avds []string
	for l := range strings.SplitSeq(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "INFO") && !strings.Contains(l, "|") {
			avds = append(avds, l)
		}
	}
	return avds
}

// Ensure reuses a device the host adb server already lists, or boots an emulator and waits for it.
func Ensure(getenv func(string) string, port int, report func(string, ...any)) error {
	say := func(f string, a ...any) {
		if report != nil {
			report(f, a...)
		}
	}
	if d, err := Devices(port); err == nil && len(d) > 0 {
		say("android: reusing %s on the host adb server 127.0.0.1:%d", strings.Join(d, ", "), port)
		return nil
	}
	adb := AdbBinary(getenv)
	if adb == "" {
		return fmt.Errorf("no adb found — install Android SDK platform-tools or set ANDROID_HOME")
	}
	if err := startAdb(adb, port); err != nil {
		return fmt.Errorf("adb start-server on port %d: %w", port, err)
	}
	if d, err := Devices(port); err == nil && len(d) > 0 {
		say("android: reusing %s on the host adb server 127.0.0.1:%d", strings.Join(d, ", "), port)
		return nil
	}
	emu := EmulatorBinary(getenv)
	if emu == "" {
		return fmt.Errorf("no Android emulator found — install it from Android Studio's SDK Manager or set ANDROID_HOME")
	}
	out, err := listAVDs(emu)
	if err != nil {
		return fmt.Errorf("emulator -list-avds: %w", err)
	}
	avd, err := PickAVD(getenv, parseAVDs(out))
	if err != nil {
		return err
	}
	cmd := exec.Command(emu, LaunchArgs(avd)...)
	cmd.Env = append(os.Environ(), "ANDROID_ADB_SERVER_PORT="+strconv.Itoa(port))
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start emulator: %w", err)
	}
	_ = cmd.Process.Release()
	say("android: booting AVD %s (adb server 127.0.0.1:%d)", avd, port)
	deadline := time.Now().Add(readyWait)
	for time.Now().Before(deadline) {
		if d, err := Devices(port); err == nil && len(d) > 0 && bootProp(adb, port, d[0]) == "1" {
			say("android: %s booted", d[0])
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("AVD %s did not finish booting within %s", avd, readyWait)
}
