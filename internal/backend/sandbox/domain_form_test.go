// SPEC: _spec/internal/sbx/kit-domain-form.puml
package sandbox

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

func has(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// sbx's own policy decides egress: the Kit carries no provider or harness hosts.
func TestTheKitWritesNoProviderOrHarnessHosts(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	in := specInput("cecli")
	in.Detected = []string{"anthropic"}
	in.Man.Capabilities.Hosts = []string{"api2.cursor.sh"}
	_, kit, _ := Spec(in)
	if allow := kit.Permissions.Network.Allow; len(allow) != 0 {
		t.Errorf("network.allow = %v, want no tier-driven hosts", allow)
	}
}
