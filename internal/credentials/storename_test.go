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
		name, kind := StoreName(tc.envVar, "")
		if name != tc.want || kind != StoreAPIKey {
			t.Errorf("StoreName(%q) = %q/%v, want %q as an api key", tc.envVar, name, kind, tc.want)
		}
	}
}

func TestAPlanTokenNeverLandsOnTheAPIKeysID(t *testing.T) {
	t.Parallel()
	// Measured: a subscription stored as `anthropic` makes the proxy attach an
	// OAuth token into an x-api-key header, and the provider answers 401 to a
	// credential that is good.
	name, kind := StoreName("CLAUDE_CODE_OAUTH_TOKEN", "claudecode")
	if name == "anthropic" {
		t.Fatal("a plan token took the api key's id; the proxy would attach it as x-api-key")
	}
	if name != "claudecode" || kind != StoreSubscription {
		t.Errorf("StoreName = %q/%v, want claudecode as a subscription", name, kind)
	}
}

func TestADefNamedForItsProviderTakesTheSubSuffix(t *testing.T) {
	t.Parallel()
	if name, _ := StoreName("CURSOR_API_KEY", "cursor"); name != "cursor" {
		t.Errorf("an api key still takes the provider id, got %q", name)
	}
	// No def in the registry is named for its provider AND carries a plan token
	// today, so the rule is exercised with a def name that would collide: the
	// suffix is what keeps the two kinds off one id when one ever does.
	if name, kind := StoreName("CLAUDE_CODE_OAUTH_TOKEN", "anthropic"); name != "anthropic-sub" || kind != StoreSubscription {
		t.Errorf("StoreName = %q/%v, want anthropic-sub so the api key keeps `anthropic`", name, kind)
	}
}

func TestTheThreeProvidersSbxCannotInject(t *testing.T) {
	t.Parallel()
	for _, envVar := range []string{"AWS_ACCESS_KEY_ID", "AZURE_OPENAI_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS"} {
		if _, kind := StoreName(envVar, ""); kind != StoreUninjectable {
			t.Errorf("%s reports as brokerable, but the registry gives it no host or header to attach", envVar)
		}
	}
}

func TestAVariableNoProviderClaims(t *testing.T) {
	t.Parallel()
	if name, kind := StoreName("NOT_A_PROVIDER_KEY", "x"); name != "" || kind != StoreUninjectable {
		t.Errorf("StoreName = %q/%v, want nothing claimed", name, kind)
	}
}
