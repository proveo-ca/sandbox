package sbx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
