// SPEC: _spec/internal/provider/provider-registry.puml
package provider

import (
	"regexp"
	"strings"
)

var knownModels = map[string][]string{
	"anthropic": {
		// current
		"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5",
		"claude-fable-5", "claude-mythos-5",
		"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-opus-4-5",
		"claude-sonnet-4-6",
		// legacy but still routable
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

// providerAliases folds upstream provider ids that share ONE registry entry, so
// a model id spelled the catalog's way still pins the same route and names the
// same credential.
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
