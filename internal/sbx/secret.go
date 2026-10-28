package sbx

import "strings"

func SecretSetArgs(name string) []string {
	return []string{"secret", "set", "--force", name}
}

func SecretSet(name, value string) error {
	return sh.SecretSet(name, value)
}

// OAuthServices are the built-in services whose one entry also holds a
// subscription login; an API key stored there outranks it for every sandbox.
var OAuthServices = []string{"anthropic", "openai"}

func HasOAuthSlot(service string) bool {
	for _, s := range OAuthServices {
		if s == service {
			return true
		}
	}
	return false
}

// APIKeyServices are the service-secret names `sbx secret ls` lists as "(stored)": API keys, not OAuth slots.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R6)
func APIKeyServices() []string {
	out, err := sh.SecretList()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "CUSTOM SECRETS" {
			break
		}
		f := strings.Fields(line)
		if len(f) == 4 && f[1] == "service" && f[3] == "(stored)" {
			names = append(names, f[2])
		}
	}
	return names
}
