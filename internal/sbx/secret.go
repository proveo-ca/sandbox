package sbx

import "strings"

func SecretSetArgs(name string) []string {
	return []string{"secret", "set", "--force", name}
}

func SecretSet(name, value string) error {
	return sh.SecretSet(name, value)
}

// SecretSetSourceArgs stores service from source flags (--ref, --command) or stdin, scoped to sandbox when it is set.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R11, R12)
func SecretSetSourceArgs(service, sandbox string, source []string) []string {
	args := SecretSetArgs(service)
	if sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}
	return append(args, source...)
}

// SecretSetSource runs `sbx secret set` for service with source flags, or with stdin carrying the value.
func SecretSetSource(service, sandbox string, source []string, stdin string) error {
	return sh.SecretSetFrom(SecretSetSourceArgs(service, sandbox, source), stdin)
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
