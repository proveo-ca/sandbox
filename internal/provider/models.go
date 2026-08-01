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
			return p
		}
	}
	if strings.HasPrefix(strings.ToLower(model), "claude-") {
		return "anthropic"
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
