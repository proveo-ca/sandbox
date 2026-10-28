package sandbox

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// declaredCredential is one credential@1 service a workload declares, with the domains a binding must approve.
type declaredCredential struct {
	Service     string
	APIKeyHosts []string
	OAuthHosts  []string
}

// declaredCredentials reads every credential@1 entry from caps, upstream maps and proveo structs alike.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R9)
func declaredCredentials(caps []sbx.CapabilityV3) []declaredCredential {
	var out []declaredCredential
	for _, c := range caps {
		if c.Type != sbx.CapCredential {
			continue
		}
		raw, err := yaml.Marshal(c.Config)
		if err != nil {
			continue
		}
		var cfg struct {
			Service string `yaml:"service"`
			APIKey  *struct {
				Inject []struct {
					Domain string `yaml:"domain"`
				} `yaml:"inject"`
			} `yaml:"apiKey"`
			OAuth *struct {
				TokenEndpoint struct {
					Host string `yaml:"host"`
				} `yaml:"tokenEndpoint"`
				ResourceHosts []string `yaml:"resourceHosts"`
			} `yaml:"oauth"`
		}
		if yaml.Unmarshal(raw, &cfg) != nil || cfg.Service == "" {
			continue
		}
		d := declaredCredential{Service: cfg.Service}
		if cfg.APIKey != nil {
			for _, i := range cfg.APIKey.Inject {
				d.APIKeyHosts = append(d.APIKeyHosts, i.Domain)
			}
		}
		if cfg.OAuth != nil {
			if h := cfg.OAuth.TokenEndpoint.Host; h != "" {
				d.OAuthHosts = append(d.OAuthHosts, h)
			}
			d.OAuthHosts = append(d.OAuthHosts, cfg.OAuth.ResourceHosts...)
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// chosenService is the service the run's own credential lives under: the auth var's provider, else the built-in agent's subscription service.
func chosenService(target, authVar string, subscription bool) string {
	if v := strings.TrimSpace(authVar); v != "" {
		if p := credentials.ProviderOfKeyVar(v); p != v {
			return p
		}
	}
	if subscription {
		return sbx.SubscriptionService(sbx.BuiltinAgent(target))
	}
	return ""
}

// credentialState is one declared service resolved against the store and the bindings.
type credentialState struct {
	declaredCredential
	Use      string
	Withheld []string
}

func resolveCredentials(decl []declaredCredential, store map[string]sbx.StoreEntry, binds map[string]sbx.Binding) []credentialState {
	var out []credentialState
	for _, d := range decl {
		use, withheld := sbx.Mechanism(store[d.Service], binds[d.Service])
		if use == "" && len(withheld) == 0 {
			continue
		}
		out = append(out, credentialState{declaredCredential: d, Use: use, Withheld: withheld})
	}
	return out
}

var mechanismLabel = map[string]string{"apikey": "API key", "oauth": "OAuth login"}

// credentialLines renders each resolved service as one line; warn marks a line that withholds something.
func credentialLines(states []credentialState, chosen string) (lines []string, warn []bool) {
	for _, s := range states {
		mark := ""
		if s.Service == chosen {
			mark = " (this run's credential)"
		}
		var held []string
		for _, w := range s.Withheld {
			held = append(held, mechanismLabel[w])
		}
		switch {
		case s.Use != "" && len(held) == 0:
			lines = append(lines, fmt.Sprintf("%s%s: sbx presents the %s", s.Service, mark, mechanismLabel[s.Use]))
			warn = append(warn, false)
		case s.Use != "":
			lines = append(lines, fmt.Sprintf("%s%s: sbx presents the %s; the stored %s has no approval in %s",
				s.Service, mark, mechanismLabel[s.Use], strings.Join(held, " and "), "credentials.yaml"))
			warn = append(warn, false)
		default:
			lines = append(lines, fmt.Sprintf("%s%s: stored %s withheld — no approval in credentials.yaml",
				s.Service, mark, strings.Join(held, " and ")))
			warn = append(warn, true)
		}
	}
	return lines, warn
}

// unboundRefusal refuses a new, unattended sandbox whose own credential sbx would withhold; on a terminal sbx asks instead.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R9)
func unboundRefusal(states []credentialState, chosen string, created, interactive bool) error {
	if !created || interactive || chosen == "" {
		return nil
	}
	i := slices.IndexFunc(states, func(s credentialState) bool { return s.Service == chosen })
	if i < 0 || states[i].Use != "" {
		return nil
	}
	s := states[i]
	return fmt.Errorf("%s's stored %s has no approval in %s, and sbx withholds it from an unattended run. "+
		"Run once in a terminal so sbx asks, or approve it in that file:\n%s",
		s.Service, mechanismLabel[s.Withheld[0]], "~/.config/sbx/credentials.yaml", bindingSnippet(s))
}

// bindingSnippet is the credentials.yaml block that approves each withheld mechanism of s.
func bindingSnippet(s credentialState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  bindings:\n    %s:\n", s.Service)
	for _, w := range s.Withheld {
		hosts := s.APIKeyHosts
		key := "apiKey"
		if w == "oauth" {
			hosts, key = s.OAuthHosts, "oauth"
		}
		fmt.Fprintf(&b, "      %s:\n        domains: [%s]\n", key, strings.Join(hosts, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// inUseProbe is the read-only `sbx exec` script that classifies the credential an agent holds, by target.
var inUseProbe = map[string]string{
	"opencode":   `opencode auth export --standalone 2>/dev/null`,
	"codex":      `cat "$HOME/.codex/auth.json" 2>/dev/null`,
	"claudecode": `cat "$HOME/.claude/.credentials.json" 2>/dev/null`,
}

// credentialInUse reads the agent's own credential state and names whether sbx manages it; "" when it cannot tell.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R10)
func credentialInUse(target, out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	switch target {
	case "opencode":
		var recs []struct {
			ID            string `json:"id"`
			IntegrationID string `json:"integrationID"`
			Active        bool   `json:"active"`
			Value         struct {
				MethodID string `json:"methodID"`
			} `json:"value"`
		}
		if json.Unmarshal([]byte(out), &recs) != nil {
			return ""
		}
		var parts []string
		for _, r := range recs {
			if !r.Active {
				continue
			}
			who := "an agent-owned login (" + cmp.Or(r.Value.MethodID, r.ID) + ") — it outranks sbx's credential"
			if strings.HasPrefix(r.ID, "sbx-") {
				who = "sbx's credential (" + r.ID + ")"
			}
			parts = append(parts, r.IntegrationID+": "+who)
		}
		if len(parts) == 0 {
			return "opencode holds no active credential"
		}
		return "opencode uses " + strings.Join(parts, "; ")
	case "codex", "claudecode":
		if strings.Contains(out, "proxy-managed") {
			return target + " uses sbx's credential (sentinel in its credential file)"
		}
		return target + " uses a credential it holds itself, not sbx's sentinel"
	}
	return ""
}

// consentInteractive reports whether `sbx create` may ask for credential approval on this terminal.
func consentInteractive() bool {
	return agentio.IsStdinTTY() && os.Getenv("PROVEO_SCHEDULE") == ""
}

// reportCredentials prints each declared service sbx holds something for, and refuses an unattended create that would withhold the run's own credential.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R9)
func reportCredentials(in Input, caps []sbx.CapabilityV3, created bool) error {
	binds, err := sbx.LoadBindings(sbx.BindingsPath(os.Getenv))
	if err != nil {
		ui.Warnf("credentials.yaml unreadable (%v); sbx decides approvals on its own", err)
		return nil
	}
	states := resolveCredentials(declaredCredentials(caps), sbx.StoreEntries(), binds)
	chosen := chosenService(in.Target, in.AuthVar, in.Man.Subscription)
	lines, warn := credentialLines(states, chosen)
	if len(lines) > 0 {
		ui.Section(ui.SectionCredentials)
	}
	for i, l := range lines {
		if warn[i] {
			ui.Warnf("%s", l)
			continue
		}
		ui.Hostf("%s", l)
	}
	interactive := consentInteractive()
	if created && interactive && slices.ContainsFunc(states, func(s credentialState) bool { return len(s.Withheld) > 0 }) {
		ui.Notef("sbx asks next whether to approve the withheld ones; its answer is saved in credentials.yaml")
	}
	return unboundRefusal(states, chosen, created, interactive)
}

// reportCredentialInUse prints, read-only, which credential the agent held; it skips a sandbox that is not running.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R10)
func reportCredentialInUse(target, name string) {
	script, ok := inUseProbe[target]
	if !ok || !sbx.Running(name) {
		return
	}
	out, err := sbx.ExecOutput(name, script, 20*time.Second)
	if err != nil {
		return
	}
	if line := credentialInUse(target, out); line != "" {
		ui.Section(ui.SectionCredentials)
		if strings.Contains(line, "outranks") || strings.Contains(line, "itself") {
			ui.Warnf("%s", line)
			return
		}
		ui.Hostf("%s", line)
	}
}
