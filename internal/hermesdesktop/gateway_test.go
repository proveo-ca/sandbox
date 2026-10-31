// SPEC: _spec/_plans/hermes-desktop-over-ssh.puml (GATEWAY)
package hermesdesktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readRegistry(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestRegisterGatewayMakesTheSandboxThePrimaryConnection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "desktop", "hermes")
	if err := RegisterGateway(dir, "hermes", "proveo-hermes-ab.sbx", "/opt/proveo/shims/hermes"); err != nil {
		t.Fatal(err)
	}
	reg := readRegistry(t, dir)
	if reg["version"] != float64(2) || reg["primary"] != gatewayID || reg["lastUsed"] != gatewayID || reg["launchMode"] != "primary" {
		t.Errorf("Desktop boots its primary; the sandbox must be it: %v", reg)
	}
	conns := reg["connections"].([]any)
	got := conns[len(conns)-1].(map[string]any)
	if got["kind"] != "ssh" || got["host"] != "proveo-hermes-ab.sbx" || got["user"] != "hermes" ||
		got["remoteHermesPath"] != "/opt/proveo/shims/hermes" {
		t.Errorf("gateway entry = %v", got)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "connections.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("connections.json mode = %v; Desktop keeps it 0600", fi.Mode().Perm())
	}
}

func TestRegisterGatewayReplacesItsOwnEntryAndKeepsOthers(t *testing.T) {
	dir := t.TempDir()
	seed := `{"version":2,"primary":"box","connections":[{"id":"local","kind":"local","label":"This Mac"},` +
		`{"id":"box","kind":"ssh","label":"box","host":"box"},{"id":"proveo-sandbox","kind":"ssh","label":"proveo sandbox","host":"old.sbx"}]}`
	if err := os.WriteFile(filepath.Join(dir, "connections.json"), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RegisterGateway(dir, "hermes", "new.sbx", "/opt/proveo/shims/hermes"); err != nil {
		t.Fatal(err)
	}
	conns := readRegistry(t, dir)["connections"].([]any)
	hosts := map[string]int{}
	for _, c := range conns {
		hosts[c.(map[string]any)["id"].(string)]++
	}
	if len(conns) != 3 || hosts["local"] != 1 || hosts["box"] != 1 || hosts[gatewayID] != 1 {
		t.Errorf("connections = %v; one proveo entry, the others kept", conns)
	}
	if conns[2].(map[string]any)["host"] != "new.sbx" {
		t.Errorf("the proveo entry must follow this run's sandbox: %v", conns[2])
	}
}

func TestRegisterGatewayRecoversFromACorruptRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "connections.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RegisterGateway(dir, "hermes", "a.sbx", "/opt/proveo/shims/hermes"); err != nil {
		t.Fatal(err)
	}
	if readRegistry(t, dir)["primary"] != gatewayID {
		t.Error("a corrupt proveo-owned registry must be rewritten with the gateway")
	}
}
