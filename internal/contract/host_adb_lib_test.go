// SPEC: _spec/internal/sbx/host-android-adb.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const mobileMCPVersion = "1.0.6"

// adbLib runs script against the entrypoint lib with getent, npm, adb and proveo-entrypoint stubbed on PATH.
func adbLib(t *testing.T, env []string, script string) (stdout, stderr, calls string) {
	t.Helper()
	bash := bashOrSkip(t)
	bin, tools := t.TempDir(), t.TempDir()
	log := filepath.Join(t.TempDir(), "calls.log")
	stub := map[string]string{
		"getent":            `[ "$2" = host.docker.internal ] && printf '169.254.1.1     STREAM host.docker.internal\n'`,
		"npm":               `printf 'npm %s\n' "$*" >>"` + log + `"`,
		"adb":               `exit 0`,
		"pgrep":             `exit 1`,
		"proveo-entrypoint": `printf 'mirror %s %s:%s\n' "$*" "$ADB_HOST" "$ADB_PORT" >>"` + log + `"`,
	}
	for name, body := range stub {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	full := `export PATH="` + bin + `:/usr/bin:/bin" _PROVEO_TOOL_HOME="` + tools + `"
source "$1/packages/lib/entrypoint-lib.sh"
` + strings.ReplaceAll(script, "$TOOLS", tools)
	cmd := exec.Command(bash, "-c", full, "bash", repoRoot(t))
	cmd.Env = append(os.Environ(), env...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	_ = cmd.Run()
	time.Sleep(50 * time.Millisecond)
	b, _ := os.ReadFile(log)
	return out.String(), errb.String(), string(b)
}

func TestHostADBEnvIsInertWithoutTheAddon(t *testing.T) {
	out, _, _ := adbLib(t, []string{"PROVEO_HOST_ADB_PORT=", "ADB_SERVER_SOCKET="}, `proveo_host_adb_env; echo "rc=$? host=${ADB_HOST:-}"`)
	if !strings.Contains(out, "rc=1 host=\n") {
		t.Errorf("no adb address must export nothing: %q", out)
	}
}

func TestHostADBEnvPrefersTheSocketProveoSet(t *testing.T) {
	out, _, _ := adbLib(t, []string{"ADB_SERVER_SOCKET=tcp:host.docker.internal:5037", "PROVEO_HOST_ADB_PORT=5037"},
		`proveo_host_adb_env && echo "$ADB_HOST $ADB_PORT $ADB_SERVER_SOCKET"`)
	if strings.TrimSpace(out) != "host.docker.internal 5037 tcp:host.docker.internal:5037" {
		t.Errorf("env = %q", out)
	}
}

func TestHostADBEnvFallsBackToTheGatewayIP(t *testing.T) {
	out, _, _ := adbLib(t, []string{"ADB_SERVER_SOCKET=", "PROVEO_HOST_ADB_PORT=5037"},
		`proveo_host_adb_env && echo "$ADB_HOST $ADB_PORT $ADB_SERVER_SOCKET"`)
	if strings.TrimSpace(out) != "169.254.1.1 5037 tcp:169.254.1.1:5037" {
		t.Errorf("env = %q", out)
	}
}

// installedServer fakes a mobile-mcp install whose server prints the env it was exec'd with.
const installedServer = `
d="$TOOLS/mobile-mcp/node_modules"; mkdir -p "$d/@mobilenext/mobile-mcp" "$d/.bin"
printf '{\n  "version": "` + mobileMCPVersion + `",\n}\n' >"$d/@mobilenext/mobile-mcp/package.json"
printf '#!/bin/sh\necho "served telemetry_off=$MOBILEMCP_DISABLE_TELEMETRY socket=$ADB_SERVER_SOCKET"\n' >"$d/.bin/mcp-server-mobile"
chmod +x "$d/.bin/mcp-server-mobile"
`

func TestMobileMCPExecRunsTheInstalledServer(t *testing.T) {
	out, _, calls := adbLib(t, []string{"ADB_SERVER_SOCKET=tcp:host.docker.internal:5037", "TMPDIR=" + t.TempDir()},
		installedServer+`proveo_mobile_mcp_exec`)
	if strings.Contains(calls, "npm ") {
		t.Errorf("a matching version must not reinstall; npm ran: %q", calls)
	}
	if strings.TrimSpace(out) != "served telemetry_off=1 socket=tcp:host.docker.internal:5037" {
		t.Errorf("stdout = %q; want only the server's own output, telemetry off", out)
	}
	if !strings.Contains(calls, "mirror adb-mirror host.docker.internal:5037") {
		t.Errorf("calls = %q; want the mirror started on the socket's address", calls)
	}
}

func TestMobileMCPExecInstallsThePinnedVersionWhenStale(t *testing.T) {
	_, _, calls := adbLib(t, []string{"ADB_SERVER_SOCKET=tcp:host.docker.internal:5037", "TMPDIR=" + t.TempDir()},
		`proveo_mobile_mcp_exec`)
	if !strings.Contains(calls, "npm install") || !strings.Contains(calls, "@mobilenext/mobile-mcp@"+mobileMCPVersion) {
		t.Errorf("calls = %q; want an npm install of the pinned version", calls)
	}
}

func TestMobileMCPExecNeedsAnADBAddress(t *testing.T) {
	out, _, calls := adbLib(t, []string{"ADB_SERVER_SOCKET=", "PROVEO_HOST_ADB_PORT="}, `proveo_mobile_mcp_exec; echo "rc=$?"`)
	if strings.TrimSpace(out) != "rc=1" || calls != "" {
		t.Errorf("no adb address must serve nothing and install nothing: out=%q calls=%q", out, calls)
	}
}

func TestAdbMirrorStartsWithTheSocketAddress(t *testing.T) {
	out, _, calls := adbLib(t, []string{"ADB_SERVER_SOCKET=tcp:host.docker.internal:5037", "TMPDIR=" + t.TempDir()},
		`proveo_host_adb_env && proveo_adb_mirror_start; echo "rc=$?"`)
	if strings.TrimSpace(out) != "rc=0" || !strings.Contains(calls, "mirror adb-mirror host.docker.internal:5037") {
		t.Errorf("out=%q calls=%q; want proveo-entrypoint adb-mirror with ADB_HOST/ADB_PORT", out, calls)
	}
}

func TestAdbMirrorIsInertWithoutTheADBEnv(t *testing.T) {
	out, _, calls := adbLib(t, []string{"ADB_HOST=", "ADB_PORT="}, `proveo_adb_mirror_start; echo "rc=$?"`)
	if strings.TrimSpace(out) != "rc=1" || calls != "" {
		t.Errorf("out=%q calls=%q", out, calls)
	}
}
