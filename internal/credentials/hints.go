// SPEC: _spec/_paradigms/credential-boundary.puml
package credentials

import (
	"fmt"
	"strings"

	"github.com/proveo-ca/proveo/internal/manifest"
)

type subscriptionAuthHint struct {
	HowTo string // one-line obtain instructions
	Login string // optional in-sandbox login command
}

// SubscriptionAuthHints maps harness name → env var → obtain/login guidance.
var SubscriptionAuthHints = map[string]map[string]subscriptionAuthHint{
	"claudecode": {
		"CLAUDE_CODE_OAUTH_TOKEN": {
			HowTo: "generate a Claude Code OAuth token with `claude setup-token`",
			Login: "claude setup-token",
		},
	},
	"codex": {
		"OPENAI_API_KEY": {
			HowTo: "create an OpenAI API key at platform.openai.com/api-keys",
			Login: "codex login",
		},
	},
	"cursor": {
		"CURSOR_API_KEY": {
			HowTo: "create a Cursor API key at cursor.com/dashboard → API Keys",
			Login: "agent login",
		},
	},
	"opencode": {
		"OPENCODE_API_KEY": {
			HowTo: "sign in at opencode.ai/auth and copy the key — one key for both plans, " +
				"Zen (`opencode/<model>`) and Go (`opencode-go/<model>`)",
		},
	},
}

func CredentialReachedAgent(man manifest.Manifest, target, homeRoot string, env []string, secrets [][2]string, lookup func(string) string) bool {
	if HasPersistedLogin(target, homeRoot) {
		return true
	}
	for _, kv := range secrets {
		if strings.TrimSpace(kv[1]) != "" {
			return true // host-side injection put a real value in the store
		}
	}
	for _, e := range env {
		name, value, valued := strings.Cut(e, "=")
		if !IsAuthVarOf(man, name) {
			continue
		}
		if !valued {
			if lookup != nil && strings.TrimSpace(lookup(name)) != "" {
				return true
			}
			continue
		}
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func StoreHolds(man manifest.Manifest, stored []string) []string {
	var held []string
	for _, n := range stored {
		if IsAuthVarOf(man, n) {
			held = append(held, n)
		}
	}
	return held
}

func IsAuthVarOf(man manifest.Manifest, name string) bool {
	for _, e := range man.Env {
		if e.Secret && e.Name == name {
			return true
		}
	}
	return false
}

func NoCredentialHint(man manifest.Manifest, target, homeRoot string, env []string, secrets [][2]string, stored []string, lookup func(string) string) []string {
	if CredentialReachedAgent(man, target, homeRoot, env, secrets, lookup) {
		return nil
	}
	byHarness := SubscriptionAuthHints[HarnessFamily(man.Name)]
	lines := []string{
		"no credential reached the agent — the likeliest reason it exited before saying anything",
	}
	if held := StoreHolds(man, stored); len(held) > 0 {
		lines = []string{fmt.Sprintf(
			"this run sent no credential of its own — the agent had only sbx's stored %s, "+
				"whose value proveo cannot read", strings.Join(held, ", ")),
		}
	}
	for _, e := range man.Env {
		if !e.Secret {
			continue
		}
		howTo := byHarness[e.Name].HowTo
		if howTo == "" {
			howTo = e.Description
		}
		if howTo != "" {
			lines = append(lines, fmt.Sprintf("  %s — %s", e.Name, howTo))
		} else {
			lines = append(lines, "  "+e.Name)
		}
		lines = append(lines, fmt.Sprintf("      sbx secret set %s", e.Name))
	}
	lines = append(lines,
		"  a store entry can already exist while holding an EMPTY value: `sbx secret ls` shows",
		"  the name either way, and `sbx secret set` overwrites it")
	return lines
}

func HarnessFamily(name string) string {
	name = strings.TrimSpace(name)
	for _, base := range []string{"claudecode", "codex", "cursor", "opencode", "cecli"} {
		if name == base || strings.HasPrefix(name, base+"-") {
			return base
		}
	}
	return name
}
