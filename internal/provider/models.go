// SPEC: _spec/internal/provider/provider-registry.puml
//
// SPEC: _spec/internal/provider/provider-registry.puml
package provider

import (
	"regexp"
	"strings"
)

var knownModels = map[string][]string{
	"anthropic": {
		"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5",
		"claude-fable-5", "claude-mythos-5",
		"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-opus-4-5",
		"claude-sonnet-4-6",
		"claude-opus-4-1", "claude-opus-4-0", "claude-sonnet-4-5", "claude-sonnet-4-0",
	},
}

func ModelProvider(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if i := strings.Index(model, "/"); i > 0 {
		switch p := strings.ToLower(model[:i]); p {
		case "ollama", "ollama_chat", "openai-compatible":
			return "" // local / shim endpoints serve arbitrary ids
		default:
			if canonical, ok := providerAliases[p]; ok {
				return canonical
			}
			return p
		}
	}
	return bareModelProvider(strings.ToLower(model))
}

var providerAliases = map[string]string{
	"opencode-go": "opencode",
}

var bareIDPrefixes = []struct{ prefix, provider string }{
	{"claude-", "anthropic"},
	{"gpt-", "openai"}, {"o1-", "openai"}, {"o3-", "openai"}, {"o4-", "openai"}, {"chatgpt-", "openai"},
	{"grok-", "xai"},
	{"gemini-", "google"},
	{"kimi-", "moonshot"}, {"moonshot-", "moonshot"},
	{"glm-", "zai"},
	{"deepseek-", "deepseek"},
	{"minimax-", "minimax"}, {"abab", "minimax"},
	{"sonar", "perplexity"},
	{"mistral-", "mistral"}, {"magistral-", "mistral"}, {"codestral-", "mistral"}, {"devstral-", "mistral"},
	{"command-", "cohere"},
}

var ambiguousBareIDs = []string{"gpt-oss", "llama-", "qwen", "mixtral", "deepseek-r1-distill"}

func bareModelProvider(model string) string {
	for _, a := range ambiguousBareIDs {
		if strings.HasPrefix(model, a) {
			return ""
		}
	}
	for _, e := range bareIDPrefixes {
		if strings.HasPrefix(model, e.prefix) {
			return e.provider
		}
	}
	return ""
}

func CheckModel(model string) (known, ok bool) {
	list, have := knownModels[ModelProvider(model)]
	bare := normalizeModelID(model)
	if !have || bare == "" {
		return false, false
	}
	for _, m := range list {
		if strings.EqualFold(normalizeModelID(m), bare) {
			return true, true
		}
	}
	return false, true
}

var datedSuffix = regexp.MustCompile(`[-@]\d{8}$`)

func normalizeModelID(model string) string {
	s := strings.TrimSpace(model)
	if i := strings.LastIndex(s, "/"); i >= 0 { // last segment: handles openrouter/vendor/model
		s = s[i+1:]
	}
	s = datedSuffix.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, ".", "-")
}

// Billing is which side of the auth row a model id lands on, when the id itself
// is enough to say.
type Billing int

const (
	// BillUnknown — the id does not settle it. Either the prefix carries no
	// billing meaning, or the vendor splits plan from overage on ITS side where
	// proveo cannot see: cursor bills a Cursor plan's included usage and then
	// usage-based overage against one CURSOR_API_KEY, and nothing in the request
	// says which one this is.
	BillUnknown Billing = iota
	// BillPlan — a fixed-cost subscription.
	BillPlan
	// BillMetered — pay per token, against a balance or an API key.
	BillMetered
)

// ModelBilling reports how a model id is billed.
//
// It reads the RAW prefix, before providerAliases folds it. That fold is right
// for routing — Zen and Go are one host on one credential, so they are one
// broker route — and wrong for this question, which is the only thing the two
// prefixes exist to distinguish:
//
//	opencode-go/<m>   the $10/mo Go plan          BillPlan
//	opencode/<m>      the Zen pay-as-you-go balance   BillMetered
//
// Both read OPENCODE_API_KEY, so the CREDENTIAL cannot answer this and the
// operator cannot be asked to pick by naming a variable. Worse, Go falls back
// to the Zen balance once its limits are spent when 'Use balance' is enabled,
// so a plan run can become a metered one without the id changing at all —
// which is why proveo warns about the mismatch rather than claiming to prevent
// the spend. SPEC: _spec/internal/credentials/credential-decisions.puml
func ModelBilling(model string) Billing {
	model = strings.TrimSpace(strings.ToLower(model))
	i := strings.Index(model, "/")
	if i <= 0 {
		return BillUnknown
	}
	switch model[:i] {
	case "opencode-go":
		return BillPlan
	case "opencode":
		return BillMetered
	}
	// Any other provider prefix is the operator's own key: metered by definition.
	if p := ModelProvider(model); p != "" && !vendorPlanProviders[p] {
		return BillMetered
	}
	return BillUnknown
}

// vendorPlanProviders are the gateways whose one credential covers a plan AND
// metered usage, with the split invisible from here.
var vendorPlanProviders = map[string]bool{"cursor": true, "opencode": true}

// SplitsBilling reports a gateway whose ONE credential covers a plan and metered
// usage both, so no credential check can tell an operator which they are
// spending. SPEC: _spec/internal/credentials/credential-decisions.puml
func SplitsBilling(name string) bool {
	return vendorPlanProviders[strings.ToLower(strings.TrimSpace(name))]
}

// IsFreeTier reports a gateway model served at no cost. opencode names every one
// of them with a `-free` suffix — 31 of them on Zen at the time of writing —
// which is a convention rather than a contract, so this is a heuristic like the
// alpha/preview filter in scripts/rank-plan-fallbacks.py and is used only where
// being wrong is safe.
//
// It exists so a fallback can be entitlement-safe. A free model bills NOTHING,
// so it cannot spend the wrong side of a choice the operator made — which is
// the property that matters when proveo cannot see which plan a key entitles.
// SPEC: _spec/internal/credentials/credential-decisions.puml
func IsFreeTier(model string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), "-free")
}
