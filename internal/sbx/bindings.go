package sbx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Binding is one service's approved mechanisms in sbx's credentials.yaml.
type Binding struct {
	APIKey, OAuth bool
}

// BindingsPath is sbx's credential-bindings file on Linux and macOS.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R9)
func BindingsPath(getenv func(string) string) string {
	home := getenv("HOME")
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "sbx", "credentials.yaml")
}

// LoadBindings reads the approved mechanisms per service; a missing file approves nothing.
func LoadBindings(path string) (map[string]Binding, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Binding{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Bindings map[string]struct {
			APIKey *struct{} `yaml:"apiKey"`
			OAuth  *struct{} `yaml:"oauth"`
		} `yaml:"bindings"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]Binding, len(doc.Bindings))
	for name, v := range doc.Bindings {
		out[name] = Binding{APIKey: v.APIKey != nil, OAuth: v.OAuth != nil}
	}
	return out, nil
}

// StoreEntry is what the global store holds for one service.
type StoreEntry struct {
	APIKey, OAuth bool
}

// StoreEntries reads the global service rows of `sbx secret ls`.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R9)
func StoreEntries() map[string]StoreEntry {
	out, err := sh.SecretList()
	if err != nil {
		return nil
	}
	return parseStoreEntries(string(out))
}

// StoredServices names every global service the store holds a key or an OAuth login for.
func StoredServices() []string {
	var out []string
	for name := range StoreEntries() {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func parseStoreEntries(listing string) map[string]StoreEntry {
	out := map[string]StoreEntry{}
	for _, line := range strings.Split(listing, "\n") {
		if strings.TrimSpace(line) == "CUSTOM SECRETS" {
			break
		}
		f := strings.Fields(line)
		if len(f) < 4 || f[0] != "(global)" || f[1] != "service" {
			continue
		}
		e := out[f[2]]
		switch strings.Join(f[3:], " ") {
		case "(stored)":
			e.APIKey = true
		case "(oauth configured)":
			e.OAuth = true
		default:
			continue
		}
		out[f[2]] = e
	}
	return out
}

// Mechanism is what sbx presents for a service: an approved API key outranks a stored OAuth login, which needs no approval.
// withheld names a stored API key the binding does not approve.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R9)
func Mechanism(e StoreEntry, b Binding) (use string, withheld []string) {
	switch {
	case e.APIKey && b.APIKey:
		use = "apikey"
	case e.OAuth:
		use = "oauth"
	}
	if e.APIKey && !b.APIKey {
		withheld = append(withheld, "apikey")
	}
	return use, withheld
}

var subscriptionService = map[string]string{
	"claude": "anthropic",
	"codex":  "openai",
	"cursor": "cursor",
}

// SubscriptionService is the service a built-in agent's subscription login lives under.
func SubscriptionService(agent string) string { return subscriptionService[agent] }

var signIn = map[string]string{
	"anthropic": "`/login` inside a claudecode sandbox",
	"openai":    "`sbx secret set openai --oauth` on this host",
	"cursor":    "cursor's own sign-in prompt inside a cursor sandbox",
}

// SignIn is the sbx-native way a service's OAuth login reaches the store; "" for a service without one.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R4)
func SignIn(service string) string { return signIn[service] }

// CustomSecret is one `sbx secret set-custom` row: the variable it fills and its placeholder.
type CustomSecret struct {
	Env, Placeholder string
}

// CustomSecrets reads the custom-secret rows of `sbx secret ls`, never their values.
func CustomSecrets() []CustomSecret {
	out, err := sh.SecretList()
	if err != nil {
		return nil
	}
	return parseCustomSecrets(string(out))
}

func parseCustomSecrets(listing string) []CustomSecret {
	var out []CustomSecret
	custom := false
	for _, line := range strings.Split(listing, "\n") {
		if strings.TrimSpace(line) == "CUSTOM SECRETS" {
			custom = true
			continue
		}
		if !custom {
			continue
		}
		f := strings.Fields(line)
		for i := 1; i < len(f); i++ {
			if strings.HasPrefix(f[i], "sbx-cs-") {
				out = append(out, CustomSecret{Env: f[i-1], Placeholder: f[i]})
				break
			}
		}
	}
	return out
}

// RemoveSecretArgs is the `sbx secret` argv that deletes a global service entry.
func RemoveSecretArgs(name string) []string { return []string{"secret", "rm", name, "-f"} }

// RemoveCustomArgs is the `sbx secret` argv that deletes a custom secret by its placeholder.
func RemoveCustomArgs(placeholder string) []string {
	return []string{"secret", "rm", "--placeholder", placeholder, "-f"}
}
