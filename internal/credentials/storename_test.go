package credentials

import "testing"

func TestAPIKeysTakeTheRegistrysOwnID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ envVar, want string }{
		{"ANTHROPIC_API_KEY", "anthropic"},
		{"OPENAI_API_KEY", "openai"},
		// The six the pattern would get wrong. Trimming _API_KEY yields gemini,
		// zhipuai, perplexityai … and sbx knows none of those.
		{"GEMINI_API_KEY", "google"},
		{"ZHIPUAI_API_KEY", "zai"},
		{"PERPLEXITYAI_API_KEY", "perplexity"},
		{"VENICEAI_API_KEY", "venice"},
		{"GITHUB_COPILOT_API_KEY", "copilot"},
	} {
		name, kind := StoreName(tc.envVar)
		if name != tc.want || kind != StoreAPIKey {
			t.Errorf("StoreName(%q) = %q/%v, want %q as an api key", tc.envVar, name, kind, tc.want)
		}
	}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (R4)
func TestAPlanTokenNamesItsServiceButIsNeverStored(t *testing.T) {
	t.Parallel()
	name, kind := StoreName("CLAUDE_CODE_OAUTH_TOKEN")
	if name != "anthropic" || kind != StoreSubscription {
		t.Errorf("StoreName = %q/%v, want anthropic as a subscription — sbx's OAuth slot is the only writer", name, kind)
	}
	if name, kind := StoreName("CURSOR_API_KEY"); name != "cursor" || kind != StoreAPIKey {
		t.Errorf("StoreName(CURSOR_API_KEY) = %q/%v, want cursor as an api key", name, kind)
	}
}

func TestTheThreeProvidersSbxCannotInject(t *testing.T) {
	t.Parallel()
	for _, envVar := range []string{"AWS_ACCESS_KEY_ID", "AZURE_OPENAI_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS"} {
		if _, kind := StoreName(envVar); kind != StoreUninjectable {
			t.Errorf("%s reports as brokerable, but the registry gives it no host or header to attach", envVar)
		}
	}
}

func TestAVariableNoProviderClaims(t *testing.T) {
	t.Parallel()
	if name, kind := StoreName("NOT_A_PROVIDER_KEY"); name != "" || kind != StoreUninjectable {
		t.Errorf("StoreName = %q/%v, want nothing claimed", name, kind)
	}
}
