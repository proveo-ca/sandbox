// SPEC: _spec/internal/sbx/sbx-kit-v3.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runHermesGatewayWire runs proveo_wire_hermes_mcp_gateway against a hermes stub that logs its argv.
func runHermesGatewayWire(t *testing.T, target string, env ...string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	stub := "#!/bin/sh\nprintf 'HERMES_HOME=%s %s\\n' \"$HERMES_HOME\" \"$*\" >> '" + log + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "hermes"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bashOrSkip(t), "-c", `source "$1/packages/lib/entrypoint-lib.sh"; proveo_wire_hermes_mcp_gateway "$2"`, "bash", repoRoot(t), target)
	cmd.Env = append(os.Environ(), append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HERMES_HOME=", "MCP_GATEWAY_URL="}, env...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("proveo_wire_hermes_mcp_gateway: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(log)
	return string(b)
}

func TestHermesTakesSbxsGatewayWithItsPlaceholdersVerbatim(t *testing.T) {
	t.Parallel()
	got := runHermesGatewayWire(t, "hermes", "MCP_GATEWAY_URL=http://gw.invalid/mcp")
	for _, want := range []string{
		"HERMES_HOME=/opt/data config set mcp_servers.mcp-gateway.url ${MCP_GATEWAY_URL}",
		"config set mcp_servers.mcp-gateway.headers.Authorization Bearer ${MCP_SENTINEL_TOKEN_NAME}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("calls lack %q — Hermes expands the ${VAR}s at runtime, so they must reach config.yaml unexpanded\n%s", want, got)
		}
	}
}

func TestTheHermesGatewayStepSkipsWithoutAGatewayOrForOtherHarnesses(t *testing.T) {
	t.Parallel()
	if got := runHermesGatewayWire(t, "hermes"); got != "" {
		t.Errorf("no MCP_GATEWAY_URL, yet hermes was configured:\n%s", got)
	}
	if got := runHermesGatewayWire(t, "claudecode", "MCP_GATEWAY_URL=http://gw.invalid/mcp"); got != "" {
		t.Errorf("claudecode ran the hermes step:\n%s", got)
	}
}

func TestTheHermesSeedWiresTheGatewayAfterItRestoresConfig(t *testing.T) {
	t.Parallel()
	calls, _, _ := runSeedPhase(t, bashOrSkip(t), nil, "hermes", "release")
	restore, wire := strings.Index(calls, "CALL proveo_sync_config restore"), strings.Index(calls, "CALL proveo_wire_hermes_mcp_gateway")
	if restore < 0 || wire < restore {
		t.Errorf("restore at %d, wire at %d; the entry must land on the restored config.yaml\n%s", restore, wire, calls)
	}
}
