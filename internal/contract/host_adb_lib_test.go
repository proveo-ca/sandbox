// SPEC: _spec/internal/sbx/host-android-adb.puml
package contract_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const artemisRef = "351ca8422f7b5b54e80a9c1ce03a222e02415b6b"

// adbLib runs script against the entrypoint lib with getent and uv stubbed on PATH.
func adbLib(t *testing.T, env []string, script string) (stdout, stderr, uvLog string) {
	t.Helper()
	bash := bashOrSkip(t)
	bin, tools := t.TempDir(), t.TempDir()
	log := filepath.Join(t.TempDir(), "uv.log")
	stub := map[string]string{
		"getent": `[ "$2" = host.docker.internal ] && printf '169.254.1.1     STREAM host.docker.internal\n'`,
		"uv":     `printf '%s\n' "$*" >>"` + log + `"`,
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
	b, _ := os.ReadFile(log)
	return out.String(), errb.String(), string(b)
}

func TestHostADBEnvIsInertWithoutTheAddon(t *testing.T) {
	out, _, _ := adbLib(t, []string{"PROVEO_HOST_ADB_PORT="}, `proveo_host_adb_env; echo "rc=$? host=${ADB_HOST:-}"`)
	if !strings.Contains(out, "rc=1 host=\n") {
		t.Errorf("no PROVEO_HOST_ADB_PORT must export nothing: %q", out)
	}
}

func TestHostADBEnvDialsTheGatewayByIP(t *testing.T) {
	out, _, _ := adbLib(t, []string{"PROVEO_HOST_ADB_PORT=5037"},
		`proveo_host_adb_env && echo "$ADB_HOST $ADB_PORT $ADB_SERVER_SOCKET"`)
	if strings.TrimSpace(out) != "169.254.1.1 5037 tcp:169.254.1.1:5037" {
		t.Errorf("env = %q", out)
	}
}

func TestArtemisConfigReusesAnInstallAtThePinnedRef(t *testing.T) {
	out, _, uv := adbLib(t, []string{"PROVEO_HOST_ADB_PORT=5037"}, `
d="$TOOLS/.local/share/uv/tools/artemis"; mkdir -p "$d/bin"; : >"$d/bin/python"; chmod +x "$d/bin/python"
printf 'requirements = [{ name = "artemis", git = "https://github.com/google/artemis?rev=`+artemisRef+`" }]\n' >"$d/uv-receipt.toml"
proveo_host_adb_env && proveo_artemis_mcp_config`)
	if uv != "" {
		t.Errorf("a matching receipt must not reinstall; uv ran: %q", uv)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("config is not JSON: %v\n%s", err, out)
	}
	a, ok := cfg.MCPServers["artemis"]
	if !ok || !strings.HasSuffix(a.Command, "/uv/tools/artemis/bin/python") || strings.Join(a.Args, " ") != "-m mcp_server" {
		t.Errorf("artemis server = %+v", a)
	}
	if a.Env["ADB_HOST"] != "169.254.1.1" || a.Env["ADB_PORT"] != "5037" {
		t.Errorf("artemis env = %v", a.Env)
	}
}

func TestArtemisConfigInstallsThePinnedRefWhenStale(t *testing.T) {
	_, _, uv := adbLib(t, []string{"PROVEO_HOST_ADB_PORT=5037"}, `proveo_host_adb_env && proveo_artemis_mcp_config`)
	if !strings.Contains(uv, "tool install") || !strings.Contains(uv, "google/artemis@"+artemisRef) {
		t.Errorf("uv = %q; want a tool install of the pinned ref", uv)
	}
}

func TestArtemisInstallSwapsOpenCVForTheHeadlessWheel(t *testing.T) {
	_, _, uv := adbLib(t, []string{"PROVEO_HOST_ADB_PORT=5037"}, `
d="$TOOLS/.local/share/uv/tools/artemis/bin"; mkdir -p "$d"
printf '#!/bin/sh\necho 5.0.0.93\n' >"$d/python"; chmod +x "$d/python"
proveo_host_adb_env && proveo_artemis_mcp_config >/dev/null`)
	lines := strings.Split(strings.TrimSpace(uv), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "tool install") ||
		!strings.Contains(lines[1], "pip uninstall") || !strings.Contains(lines[1], "opencv-python") ||
		!strings.Contains(lines[2], "opencv-python-headless==5.0.0.93") {
		t.Errorf("uv calls = %q; want install, uninstall opencv-python, install the headless wheel at the same version", lines)
	}
}

func TestArtemisConfigNeedsTheADBEnv(t *testing.T) {
	out, _, uv := adbLib(t, []string{"ADB_HOST=", "ADB_PORT="}, `proveo_artemis_mcp_config; echo "rc=$?"`)
	if strings.TrimSpace(out) != "rc=1" || uv != "" {
		t.Errorf("no adb env must emit nothing and install nothing: out=%q uv=%q", out, uv)
	}
}
