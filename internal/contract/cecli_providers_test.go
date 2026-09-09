package contract

import (
	"sort"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
)

// cecliExcluded is every provider proveo brokers that cecli cannot route to,
// each with the reason. A provider belongs here ONLY when cecli genuinely
// cannot use it — not when an operator merely does not want it, which is a
// per-run preference and has no business in a shared def.
var cecliExcluded = map[string]string{
	"cursor": "Cursor has no bring-your-own-key path (staff-confirmed, see " +
		"internal/provider/provider.go) — the key only works inside Cursor's own CLI, " +
		"so declaring it hands cecli a credential no request of cecli's can spend",
}

func cecliCapabilities(t *testing.T) manifest.Capabilities {
	t.Helper()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "cecli" {
			return m.Capabilities
		}
	}
	t.Fatal("no cecli manifest")
	return manifest.Capabilities{}
}

// An EMPTY providers list allows everything (manifest.listAllows), which is the
// absence of a capability statement rather than a wide one. That is how a cecli
// run came to declare a cursor credential.
func TestCecliDeclaresItsProvidersExplicitly(t *testing.T) {
	c := cecliCapabilities(t)
	if len(c.Providers) == 0 {
		t.Fatal("cecli declares no providers, which allows EVERY provider proveo detects — " +
			"including ones cecli cannot spend a key on. Every host key then becomes a " +
			"forwarded env var and a declared sbx credential.")
	}
}

// The drift guard: when the registry grows, this list has to grow with it or be
// given a reason. Silence here is what let `cursor` through in the first place.
func TestEveryBrokeredProviderIsDecidedForCecli(t *testing.T) {
	c := cecliCapabilities(t)
	var undecided []string
	for _, name := range provider.Names() {
		if c.AllowsProvider(name) {
			continue
		}
		if _, excluded := cecliExcluded[name]; excluded {
			continue
		}
		undecided = append(undecided, name)
	}
	sort.Strings(undecided)
	if len(undecided) > 0 {
		t.Errorf("provider(s) %s are in proveo's registry but neither allowed by cecli's "+
			"manifest nor listed in cecliExcluded with a reason.\n"+
			"Add them to defs/cecli/harness.manifest `capabilities.providers` if cecli can "+
			"route to them, or to cecliExcluded saying why it cannot. Leaving a provider "+
			"undecided is how it ends up declared by accident.", strings.Join(undecided, ", "))
	}
}

// The other direction: an exclusion that is silently also in the allow list
// would read as a decision while doing nothing.
func TestCecliExclusionsAreActuallyExcluded(t *testing.T) {
	c := cecliCapabilities(t)
	for name, why := range cecliExcluded {
		if c.AllowsProvider(name) {
			t.Errorf("%q is listed as excluded (%s) but the manifest allows it — the "+
				"exclusion is decorative", name, why)
		}
		if _, ok := provider.Lookup(name); !ok {
			t.Errorf("%q is excluded but is not in the registry at all; drop the entry", name)
		}
	}
}
