package sbx

import "strings"

func SecretSetArgs(name string) []string {
	return []string{"secret", "set", "--force", name}
}

func SecretSet(name, value string) error {
	return sh.SecretSet(name, value)
}

// BuiltinServices are the service ids whose hosts and auth header sbx knows, per `sbx secret set --help` (v0.42.1).
// SPEC: _spec/internal/sbx/credential-resolution.puml (R2, R3)
var BuiltinServices = []string{
	"anthropic", "copilot", "cursor", "devin", "droid", "github", "google",
	"groq", "mistral", "nebius", "openai", "openrouter", "xai",
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

func IsBuiltinService(name string) bool {
	for _, s := range BuiltinServices {
		if s == name {
			return true
		}
	}
	return false
}

// SecretSetCustomArgs stores a credential for a service sbx does not know,
// naming the hosts to match and the variable the sandbox sees.
func SecretSetCustomArgs(hosts []string, envVar string) []string {
	args := []string{"secret", "set-custom"}
	for _, h := range hosts {
		args = append(args, "--host", h)
	}
	return append(args, "--env", envVar)
}

// CustomSecretPlaceholder finds the placeholder of the custom secret already
// bound to envVar, if there is one.
func CustomSecretPlaceholder(envVar string) string {
	out, err := sh.SecretList()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		var hasVar bool
		var placeholder string
		for _, f := range fields {
			if f == envVar {
				hasVar = true
			}
			if strings.HasPrefix(f, customPlaceholderPrefix) {
				placeholder = f
			}
		}
		if hasVar && placeholder != "" {
			return placeholder
		}
	}
	return ""
}

const customPlaceholderPrefix = "sbx-cs-"

func SecretRemoveCustomArgs(placeholder string) []string {
	return []string{"secret", "rm", "--placeholder", placeholder, "-f"}
}

// SecretSetCustom replaces whatever custom secret holds envVar, so the value
// this run resolved is the one that answers — the same semantics `secret set
// --force` already gives a service secret.
func SecretSetCustom(hosts []string, envVar, value string) error {
	if p := CustomSecretPlaceholder(envVar); p != "" {
		if err := sh.SecretRemoveCustom(p); err != nil {
			return err
		}
	}
	return sh.SecretSetCustom(hosts, envVar, value)
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
