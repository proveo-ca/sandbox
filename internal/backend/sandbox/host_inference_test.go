// SPEC: _spec/internal/sbx/host-inference.puml
package sandbox

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestHostModelAddsTheOllamaPortToTheKit(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	in := specInput("hermes")
	in.AgentEnv = egress.LocalModelEnv("qwen3.8:latest", sbx.HostOllamaGuestBase)
	_, kit, _ := Spec(in)
	if !has(kit.Permissions.Network.Allow, sbx.HostOllamaPolicyHost) {
		t.Errorf("network.allow = %v, missing %q — sbx matches host.docker.internal as localhost:<port>",
			kit.Permissions.Network.Allow, sbx.HostOllamaPolicyHost)
	}

	in.AgentEnv = nil
	_, kit, _ = Spec(in)
	if has(kit.Permissions.Network.Allow, sbx.HostOllamaPolicyHost) {
		t.Error("no host model ⇒ no host port in the allowlist")
	}
}

func TestPlanNoProxyNeverOverridesSbxs(t *testing.T) {
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		if !proxyOnlyVar(name) {
			t.Errorf("%s must be dropped: sbx sets its own, and the plan's omits gateway.docker.internal", name)
		}
	}
}
