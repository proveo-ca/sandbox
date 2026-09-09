package sandbox

import (
	"strings"
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

// SPEC: _spec/internal/sbx/kit-domain-form.puml
func TestSquidSuffixReachesTheKitAsBothHalves(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	in := specInput("cecli")
	in.Detected = []string{"anthropic"}
	_, kit, _ := Spec(in)

	allow := kit.Permissions.Network.Allow
	// The registry stores `.anthropic.com`. sbx enforces neither that nor one
	// half of it: the CLI reference says the apex and the wildcard do not cover
	// each other, so a run that wants both has to name both.
	for _, want := range []string{"anthropic.com", "*.anthropic.com"} {
		if !has(allow, want) {
			t.Errorf("network.allow = %v, missing %q — sbx enforces exact hosts and "+
				"single-label wildcards, and these two do not cover each other", allow, want)
		}
	}
	if has(allow, ".anthropic.com") {
		t.Errorf("network.allow still carries the Squid `dstdomain` form: %v", allow)
	}
}

// The defect in one assertion: a pattern sbx cannot match is a credential sbx
// cannot attach, because SPEC-v2 ties inject[].domain to network.allow.
func TestNothingHandedToSbxCarriesSquidNotation(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	withStoredSecrets(t, "anthropic", "xai")
	for _, target := range []string{"cecli", "claudecode"} {
		t.Run(target, func(t *testing.T) {
			in := specInput(target)
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
			_, kit, _ := Spec(in)

			if len(kit.Permissions.Network.Allow) == 0 {
				t.Fatal("no allowlist at all — the fixture proves nothing")
			}
			for _, a := range kit.Permissions.Network.Allow {
				if strings.HasPrefix(a, ".") {
					t.Errorf("network.allow entry %q is Squid `dstdomain` notation; sbx "+
						"enforces exact host, host:port and *.label — this matches nothing", a)
				}
			}
			for _, c := range kit.Credentials {
				if c.APIKey == nil {
					continue
				}
				for _, inj := range c.APIKey.Inject {
					if strings.HasPrefix(inj.Domain, ".") {
						t.Errorf("credential %q injects into %q — a domain sbx never matches, "+
							"so the key is never attached and the request leaves unauthenticated",
							c.Service, inj.Domain)
					}
				}
			}
		})
	}
}

// A bare host in a def's own `capabilities.hosts` must not silently acquire a
// wildcard sibling: that would widen reach the manifest never asked for.
func TestManifestHostsAreNotWidened(t *testing.T) {
	t.Setenv(sbx.EnvAgentKit, "1")
	in := specInput("cecli")
	in.Man.Capabilities.Hosts = []string{"api2.cursor.sh"}
	_, kit, _ := Spec(in)

	allow := kit.Permissions.Network.Allow
	if !has(allow, "api2.cursor.sh") {
		t.Fatalf("network.allow = %v, missing the declared host", allow)
	}
	if has(allow, "*.api2.cursor.sh") || has(allow, "*.cursor.sh") {
		t.Errorf("network.allow = %v — a bare host was widened; Squid's leading dot is "+
			"what asks for subdomains, and this manifest did not use one", allow)
	}
}
