// SPEC: _spec/defs/hermes/hermes-paradigm.puml
package contract

import (
	"sort"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
)

// hermesExcluded is every registry provider hermes cannot spend a brokered key on, with the reason.
var hermesExcluded = map[string]string{
	"cursor":     "harness-plan key: only Cursor's own CLI can spend it",
	"opencode":   "harness-plan key: hermes reads OPENCODE_GO_API_KEY/OPENCODE_ZEN_API_KEY, not OPENCODE_API_KEY",
	"moonshot":   "hermes's kimi provider reads KIMI_API_KEY, not MOONSHOT_API_KEY",
	"copilot":    "hermes reads COPILOT_GITHUB_TOKEN/GH_TOKEN, not GITHUB_COPILOT_API_KEY",
	"azure":      "hermes's azure-foundry reads AZURE_FOUNDRY_API_KEY, not AZURE_API_KEY",
	"vertex":     "the credential is a host file path (GOOGLE_APPLICATION_CREDENTIALS), not a key",
	"baseten":    "no hermes provider",
	"cerebras":   "no hermes provider",
	"cohere":     "no hermes provider",
	"groq":       "no hermes provider",
	"mistral":    "no hermes provider",
	"perplexity": "no hermes provider",
	"sambanova":  "no hermes provider",
	"together":   "no hermes provider",
	"venice":     "no hermes provider",
}

func hermesManifest(t *testing.T) manifest.Manifest {
	t.Helper()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "hermes" {
			return m
		}
	}
	t.Fatal("no hermes manifest")
	return manifest.Manifest{}
}

func hermesCapabilities(t *testing.T) manifest.Capabilities { return hermesManifest(t).Capabilities }

// Like cecli, hermes has no plan of its own: a provider key is a usage key, so
// it is brokered and no auth row can withhold it.
func TestHermesProviderKeysAreUsageKeysNotAPlan(t *testing.T) {
	m := hermesManifest(t)
	if credentials.DeclaresSubscription(m) {
		t.Fatal("hermes declares an env secret, which the CLI reads as a plan credential")
	}
	keys := map[string]string{"OPENROUTER_API_KEY": "or", "ANTHROPIC_API_KEY": "sk", "CURSOR_API_KEY": "cur"}
	got := strings.Join(credentials.ProviderKeyVars(m, func(k string) string { return keys[k] }), ",")
	for _, want := range []string{"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY"} {
		if !strings.Contains(got, want) {
			t.Errorf("usage keys %q miss %s", got, want)
		}
	}
	if strings.Contains(got, "CURSOR_API_KEY") {
		t.Errorf("usage keys %q include a cursor key hermes cannot spend", got)
	}
}

func TestHermesDeclaresItsProvidersExplicitly(t *testing.T) {
	if len(hermesCapabilities(t).Providers) == 0 {
		t.Fatal("hermes declares no providers, which allows every provider proveo detects")
	}
}

func TestEveryBrokeredProviderIsDecidedForHermes(t *testing.T) {
	c := hermesCapabilities(t)
	var undecided []string
	for _, name := range provider.Names() {
		if c.AllowsProvider(name) {
			continue
		}
		if _, excluded := hermesExcluded[name]; excluded {
			continue
		}
		undecided = append(undecided, name)
	}
	sort.Strings(undecided)
	if len(undecided) > 0 {
		t.Errorf("provider(s) %s are neither allowed by defs/hermes/harness.manifest nor listed "+
			"in hermesExcluded with a reason", strings.Join(undecided, ", "))
	}
}

func TestHermesExclusionsAreActuallyExcluded(t *testing.T) {
	c := hermesCapabilities(t)
	for name, why := range hermesExcluded {
		if c.AllowsProvider(name) {
			t.Errorf("%q is excluded (%s) but the manifest allows it", name, why)
		}
		if _, ok := provider.Lookup(name); !ok {
			t.Errorf("%q is excluded but is not in the registry; drop the entry", name)
		}
	}
}
