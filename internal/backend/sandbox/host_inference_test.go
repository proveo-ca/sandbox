// SPEC: _spec/internal/sbx/host-inference.puml
package sandbox

import (
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/hostdev"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestHostModelAddsTheOllamaPortToTheKit(t *testing.T) {
	in := specInput("hermes")
	in.AgentEnv = sbx.LocalModelEnv("qwen3.8:latest")
	_, kit, _ := Spec(in)
	if !has(kit.Allow(), sbx.HostOllamaPolicyHost) {
		t.Errorf("network.allow = %v, missing %q — sbx matches host.docker.internal as localhost:<port>",
			kit.Allow(), sbx.HostOllamaPolicyHost)
	}

	in.AgentEnv = nil
	_, kit, _ = Spec(in)
	if has(kit.Allow(), sbx.HostOllamaPolicyHost) {
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

func TestHostCDPAddsItsPortToTheKit(t *testing.T) {
	for _, target := range []string{"hermes", "claudecode", "codex", "cursor", "opencode"} {
		in := specInput(target)
		env := hostdev.EnvCDPPort + "=9333"
		in.AgentEnv = []string{env}
		cfg, kit, _ := Spec(in)
		if !has(kit.Allow(), "localhost:9333") || !slices.Contains(cfg.Env, env) {
			t.Errorf("%s: allow %v, env %v; want Chrome port in both", target, kit.Allow(), cfg.Env)
		}
		if len(cfg.Publish) != 0 {
			t.Errorf("%s: host Chrome added a guest port publication: %v", target, cfg.Publish)
		}
	}
	in := specInput("hermes")
	in.AgentEnv = []string{hostdev.EnvCDPPort + "=nope"}
	_, kit, _ := Spec(in)
	for _, h := range kit.Allow() {
		if strings.HasPrefix(h, "localhost:") {
			t.Errorf("an unparsable port must add nothing, got %q", h)
		}
	}
}
