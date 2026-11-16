// SPEC: _spec/internal/sbx/kit-domain-form.puml
package sandbox

import (
	"slices"
	"testing"
)

func has(list []string, want string) bool {
	return slices.Contains(list, want)
}

// sbx's own policy decides egress: the Kit carries no provider or harness hosts.
func TestTheKitWritesNoProviderOrHarnessHosts(t *testing.T) {
	in := specInput("hermes")
	in.Detected = []string{"anthropic"}
	in.Man.Capabilities.Hosts = []string{"api2.cursor.sh"}
	_, kit, _ := Spec(in)
	if allow := kit.Allow(); len(allow) != 0 {
		t.Errorf("network.allow = %v, want no tier-driven hosts", allow)
	}
}
