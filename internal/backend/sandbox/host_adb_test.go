// SPEC: _spec/internal/sbx/host-android-adb.puml
package sandbox

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestHostADBAddsTheServerPortToTheKit(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	in := specInput("claudecode")
	in.AgentEnv = []string{hostadb.EnvPort + "=5037"}
	_, kit, _ := Spec(in)
	if !has(kit.Permissions.Network.Allow, "localhost:5037") {
		t.Errorf("network.allow = %v, missing localhost:5037", kit.Permissions.Network.Allow)
	}
	in.AgentEnv = nil
	_, kit, _ = Spec(in)
	if has(kit.Permissions.Network.Allow, "localhost:5037") {
		t.Error("no android add-on ⇒ no adb port in the allowlist")
	}
}
