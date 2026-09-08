package sandbox

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// gatedInput is a run where the host holds keys for BOTH providers but the
// manifest permits only one. Both halves matter: an ungated run declares a
// credential the agent cannot spend AND forwards its key as an env var, which
// is what makes a harness probe a provider nobody asked it to use.
func gatedInput(t *testing.T, permitted ...string) Input {
	t.Helper()
	in := specInput("cecli")
	in.Man.Capabilities.Providers = permitted
	in.Detected = []string{"anthropic", "xai"}
	in.Lookup = func(k string) string {
		switch k {
		case "ANTHROPIC_API_KEY":
			return "sk-ant-real-key"
		case "XAI_API_KEY":
			return "xai-real-key"
		}
		return ""
	}
	return in
}

func TestForbiddenProviderIsNeitherDeclaredNorForwarded(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	withStoredSecrets(t, "anthropic", "xai")
	cfg, kit, secrets := Spec(gatedInput(t, "anthropic"))

	for _, c := range kit.Credentials {
		if c.Service == "xai" {
			t.Error("the kit declares an xai credential the manifest forbids — sbx then " +
				"sets XAI_API_KEY to a sentinel, and a harness that reads os.environ to " +
				"decide which providers to talk to will talk to that one")
		}
	}
	for _, a := range kit.Permissions.Network.Allow {
		if strings.HasSuffix(a, "x.ai") {
			t.Errorf("network.allow carries %q for a forbidden provider", a)
		}
	}
	for _, kv := range secrets {
		if kv[0] == "XAI_API_KEY" {
			t.Error("XAI_API_KEY was stored for a provider the manifest forbids")
		}
	}
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, "XAI_API_KEY") || e == "XAI_API_KEY" {
			t.Errorf("cfg.Env forwards %q past the provider gate", e)
		}
	}

	// The control: the permitted provider must still come through, or this test
	// would pass just as well against a Spec that declared nothing at all.
	var sawAnthropic bool
	for _, c := range kit.Credentials {
		if c.Service == "anthropic" {
			sawAnthropic = true
		}
	}
	if !sawAnthropic {
		t.Fatal("no anthropic credential either — the gate is not selecting, it is emptying")
	}
	if !has(kit.Permissions.Network.Allow, "anthropic.com") {
		t.Errorf("network.allow = %v, missing the permitted provider", kit.Permissions.Network.Allow)
	}
}

// The pre-fix behaviour, kept as a named contrast: an empty list is not a wide
// capability statement, it is no statement, and it allows everything.
func TestEmptyProviderListStillAllowsEverything(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	withStoredSecrets(t, "anthropic", "xai")
	_, kit, _ := Spec(gatedInput(t))

	var sawXai bool
	for _, c := range kit.Credentials {
		if c.Service == "xai" {
			sawXai = true
		}
	}
	if !sawXai {
		t.Fatal("an empty providers list no longer allows everything — manifest.listAllows " +
			"changed, and every def relying on the permissive default needs re-reading")
	}
}
