// Package provider is the single source of truth about inference providers:
// how each is auto-detected (which env vars imply it), its Squid write-pin ACL,
// and — for the broker-injectable ones — the auth header/host used to inject a
// credential. It replaces provider knowledge that was duplicated between
// defs/lib/egress.sh (Bash) and the broker.
//
// Not every provider is broker-injectable: signed-request providers
// (Bedrock/Azure/Vertex) are detectable and get a Squid ACL, but have no static
// auth header to inject, so Resolve reports them as non-injectable.
// SPEC: _spec/internal/provider/provider-registry.puml
package provider

import "strings"

// AuthOption is one way to authenticate to a provider. The first option whose
// EnvVar is present wins, so list the preferred scheme first.
type AuthOption struct {
	EnvVar string // env var holding the secret, e.g. "ANTHROPIC_API_KEY"
	Header string // header to set, e.g. "x-api-key" or "authorization"
	Query  string // query param to set instead of a header (e.g. Gemini "key")
	Bearer bool   // prefix the value with "Bearer "
}

// Entry is a provider's full policy: detection, Squid ACL, and (optional) broker
// injection. Entries are held in an ordered slice; detection order is preserved.
type Entry struct {
	Name   string
	Detect []string     // env vars that imply this provider (any present => detected)
	ACL    string       // Squid `provider_allow` ACL body (after "acl provider_allow ")
	Hosts  []string     // broker inject/strip hosts (nil => not broker-injectable)
	Auth   []AuthOption // broker auth options (nil => not broker-injectable)
}

// Resolved is the concrete broker inputs for a run.
type Resolved struct {
	Hosts  []string
	Header string
	Query  string
	Value  string // empty => no injectable key present; strip + pass-through only
	EnvVar string // which credential was chosen, for reporting
}

func bearer(envVar string) []AuthOption {
	return []AuthOption{{EnvVar: envVar, Header: "authorization", Bearer: true}}
}

// entries is ordered to match defs/lib/egress.sh `proveo_egress_detect_providers`.
var entries = []Entry{
	{Name: "anthropic", Detect: []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"},
		ACL: "dstdomain .anthropic.com", Hosts: []string{".anthropic.com"}, Auth: []AuthOption{
			{EnvVar: "ANTHROPIC_API_KEY", Header: "x-api-key"},
			{EnvVar: "CLAUDE_CODE_OAUTH_TOKEN", Header: "authorization", Bearer: true},
		}},
	{Name: "cursor", Detect: []string{"CURSOR_API_KEY"},
		ACL: "dstdomain .cursor.sh .cursor.com", Hosts: []string{".cursor.sh", ".cursor.com"}, Auth: bearer("CURSOR_API_KEY")},
	{Name: "openai", Detect: []string{"OPENAI_API_KEY"},
		ACL: "dstdomain .openai.com .api.openai.com", Hosts: []string{".openai.com"}, Auth: bearer("OPENAI_API_KEY")},
	{Name: "moonshot", Detect: []string{"MOONSHOT_API_KEY"},
		ACL: "dstdomain .moonshot.ai", Hosts: []string{".moonshot.ai"}, Auth: bearer("MOONSHOT_API_KEY")},
	{Name: "cerebras", Detect: []string{"CEREBRAS_API_KEY"},
		ACL: "dstdomain .cerebras.ai", Hosts: []string{".cerebras.ai"}, Auth: bearer("CEREBRAS_API_KEY")},
	{Name: "deepinfra", Detect: []string{"DEEPINFRA_API_KEY"},
		ACL: "dstdomain .deepinfra.com", Hosts: []string{".deepinfra.com"}, Auth: bearer("DEEPINFRA_API_KEY")},
	{Name: "baseten", Detect: []string{"BASETEN_API_KEY"},
		ACL: "dstdomain .baseten.co", Hosts: []string{".baseten.co"}, Auth: bearer("BASETEN_API_KEY")},
	{Name: "perplexity", Detect: []string{"PERPLEXITYAI_API_KEY", "PERPLEXITY_API_KEY"},
		ACL: "dstdomain .perplexity.ai", Hosts: []string{".perplexity.ai"}, Auth: bearer("PERPLEXITYAI_API_KEY")},
	{Name: "sambanova", Detect: []string{"SAMBANOVA_API_KEY"},
		ACL: "dstdomain .sambanova.ai", Hosts: []string{".sambanova.ai"}, Auth: bearer("SAMBANOVA_API_KEY")},
	{Name: "nebius", Detect: []string{"NEBIUS_API_KEY"},
		ACL: "dstdomain .nebius.com .nebius.ai", Hosts: []string{".nebius.com", ".nebius.ai"}, Auth: bearer("NEBIUS_API_KEY")},
	{Name: "novita", Detect: []string{"NOVITA_API_KEY"},
		ACL: "dstdomain .novita.ai", Hosts: []string{".novita.ai"}, Auth: bearer("NOVITA_API_KEY")},
	{Name: "venice", Detect: []string{"VENICEAI_API_KEY", "VENICE_API_KEY"},
		ACL: "dstdomain .venice.ai", Hosts: []string{".venice.ai"}, Auth: bearer("VENICEAI_API_KEY")},
	{Name: "minimax", Detect: []string{"MINIMAX_API_KEY"},
		ACL: "dstdomain .minimax.io .minimaxi.com", Hosts: []string{".minimax.io", ".minimaxi.com"}, Auth: bearer("MINIMAX_API_KEY")},
	{Name: "copilot", Detect: []string{"GITHUB_COPILOT_API_KEY", "GH_COPILOT_TOKEN"},
		ACL: "dstdomain .githubcopilot.com .github.com", Hosts: []string{".githubcopilot.com", ".github.com"}, Auth: bearer("GITHUB_COPILOT_API_KEY")},
	{Name: "deepseek", Detect: []string{"DEEPSEEK_API_KEY"},
		ACL: "dstdomain .deepseek.com", Hosts: []string{".deepseek.com"}, Auth: bearer("DEEPSEEK_API_KEY")},
	{Name: "huggingface", Detect: []string{"HUGGINGFACE_API_KEY", "HF_TOKEN"},
		ACL: "dstdomain .huggingface.co .hf.co", Hosts: []string{".huggingface.co", ".hf.co"},
		Auth: []AuthOption{
			{EnvVar: "HUGGINGFACE_API_KEY", Header: "authorization", Bearer: true},
			{EnvVar: "HF_TOKEN", Header: "authorization", Bearer: true},
		}},
	{Name: "zai", Detect: []string{"ZAI_API_KEY", "ZHIPUAI_API_KEY"},
		ACL: "dstdomain .z.ai .bigmodel.cn", Hosts: []string{".z.ai", ".bigmodel.cn"}, Auth: bearer("ZAI_API_KEY")},
	{Name: "xai", Detect: []string{"XAI_API_KEY"},
		ACL: "dstdomain .x.ai", Hosts: []string{".x.ai"}, Auth: bearer("XAI_API_KEY")},
	{Name: "google", Detect: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		ACL: "dstdomain generativelanguage.googleapis.com", Hosts: []string{"generativelanguage.googleapis.com"}, Auth: []AuthOption{
			{EnvVar: "GEMINI_API_KEY", Header: "x-goog-api-key"},
			{EnvVar: "GOOGLE_API_KEY", Header: "x-goog-api-key"},
		}},
	{Name: "groq", Detect: []string{"GROQ_API_KEY"},
		ACL: "dstdomain .groq.com", Hosts: []string{".groq.com"}, Auth: bearer("GROQ_API_KEY")},
	{Name: "mistral", Detect: []string{"MISTRAL_API_KEY"},
		ACL: "dstdomain .mistral.ai", Hosts: []string{".mistral.ai"}, Auth: bearer("MISTRAL_API_KEY")},
	{Name: "cohere", Detect: []string{"COHERE_API_KEY"},
		ACL: "dstdomain .cohere.com .cohere.ai", Hosts: []string{".cohere.com", ".cohere.ai"}, Auth: bearer("COHERE_API_KEY")},
	{Name: "together", Detect: []string{"TOGETHER_API_KEY"},
		ACL: "dstdomain .together.xyz .together.ai", Hosts: []string{".together.xyz", ".together.ai"}, Auth: bearer("TOGETHER_API_KEY")},
	{Name: "fireworks", Detect: []string{"FIREWORKS_API_KEY"},
		ACL: "dstdomain .fireworks.ai", Hosts: []string{".fireworks.ai"}, Auth: bearer("FIREWORKS_API_KEY")},
	{Name: "gmi", Detect: []string{"GMI_API_KEY"},
		ACL: "dstdomain .gmi-serving.com", Hosts: []string{".gmi-serving.com"}, Auth: bearer("GMI_API_KEY")},
	{Name: "openrouter", Detect: []string{"OPENROUTER_API_KEY"},
		ACL: "dstdomain openrouter.ai .openrouter.ai", Hosts: []string{"openrouter.ai", ".openrouter.ai"}, Auth: bearer("OPENROUTER_API_KEY")},
	// Signed-request providers: detectable + Squid-pinned, but NOT broker-injectable.
	{Name: "bedrock", Detect: []string{"AWS_BEARER_TOKEN_BEDROCK", "AWS_ACCESS_KEY_ID"},
		ACL: `dstdom_regex (^|\.)bedrock-runtime\.[a-z0-9-]+\.amazonaws\.com$`},
	{Name: "azure", Detect: []string{"AZURE_API_KEY", "AZURE_OPENAI_API_KEY"},
		ACL: "dstdomain .inference.ai.azure.com .services.ai.azure.com .openai.azure.com .cognitiveservices.azure.com"},
	{Name: "vertex", Detect: []string{"GOOGLE_APPLICATION_CREDENTIALS"},
		ACL: `dstdom_regex (^|\.)([a-z0-9-]+-)?aiplatform\.googleapis\.com$`},
}

var byName = func() map[string]*Entry {
	m := make(map[string]*Entry, len(entries))
	for i := range entries {
		m[entries[i].Name] = &entries[i]
	}
	return m
}()

// Names returns all provider names in registry (detection) order.
func Names() []string {
	out := make([]string, len(entries))
	for i := range entries {
		out[i] = entries[i].Name
	}
	return out
}

// Lookup returns the entry for a provider name.
func Lookup(name string) (Entry, bool) {
	e, ok := byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Entry{}, false
	}
	return *e, true
}

// Detect returns the providers implied by the present env vars, in registry
// order. Mirrors defs/lib/egress.sh `proveo_egress_detect_providers`.
func Detect(getenv func(string) string) []string {
	var out []string
	for i := range entries {
		for _, v := range entries[i].Detect {
			if strings.TrimSpace(getenv(v)) != "" {
				out = append(out, entries[i].Name)
				break
			}
		}
	}
	return out
}

// ACLBody returns the Squid `provider_allow` ACL body for a provider.
func ACLBody(name string) (string, bool) {
	e, ok := byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return "", false
	}
	return e.ACL, true
}

func DetectVars() []string {
	seen := map[string]bool{}
	var out []string
	for i := range entries {
		for _, v := range entries[i].Detect {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

// KeyVars returns every broker secret env-var name (injectable providers only),
// so the host side can dump exactly those into the broker's secret env-file.
func KeyVars() []string {
	seen := map[string]bool{}
	var out []string
	for i := range entries {
		for _, a := range entries[i].Auth {
			if !seen[a.EnvVar] {
				seen[a.EnvVar] = true
				out = append(out, a.EnvVar)
			}
		}
	}
	return out
}

// Resolve produces broker inputs for name using getenv. ok is false when the
// provider is unknown OR not broker-injectable (no static auth header). When
// known-injectable but no key is present, Hosts is still populated (for
// strip-exclusion) and Value is empty (pass-through on the provider host).
func Resolve(name string, getenv func(string) string) (Resolved, bool) {
	return ResolveWith(name, "", getenv)
}

// ResolveWith is Resolve with an explicit credential choice. A provider may accept
// more than one — anthropic takes an API key OR a subscription OAuth token — and
// the ordering of Auth would otherwise decide silently for an operator holding
// both. preferVar names the env var to use; unset or unavailable falls back to the
// declared order.
func ResolveWith(name, preferVar string, getenv func(string) string) (Resolved, bool) {
	e, ok := byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok || len(e.Auth) == 0 {
		return Resolved{}, false
	}
	opts := e.Auth
	if preferVar = strings.TrimSpace(preferVar); preferVar != "" {
		for _, a := range e.Auth {
			if strings.EqualFold(a.EnvVar, preferVar) && strings.TrimSpace(getenv(a.EnvVar)) != "" {
				opts = append([]AuthOption{a}, e.Auth...)
				break
			}
		}
	}
	r := Resolved{Hosts: e.Hosts}
	for _, a := range opts {
		v := strings.TrimSpace(getenv(a.EnvVar))
		if v == "" {
			continue
		}
		r.Header = a.Header
		r.Query = a.Query
		r.EnvVar = a.EnvVar
		if a.Bearer {
			r.Value = "Bearer " + v
		} else {
			r.Value = v
		}
		break
	}
	return r, true
}

// AuthVars lists the credential env vars a provider accepts, in declared order.
// More than one means the operator has a choice to make.
func AuthVars(name string) []string {
	e, ok := byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(e.Auth))
	for _, a := range e.Auth {
		out = append(out, a.EnvVar)
	}
	return out
}
