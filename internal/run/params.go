// SPEC: _spec/internal/credentials/credential-decisions.puml, _spec/internal/sbx/policy-baseline.puml, _spec/internal/sbx/credential-path.puml
package run

import (
	"fmt"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/devports"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
)

type Params struct {
	Target, Image, Credentials, LocalModel, Input, Output, Scope, DataDir string
	CredsSet                                                              bool
	Addons                                                                []string
	AddonsAnswered                                                        bool // a cached or prompted answer exists; default-on add-ons stop defaulting
	AddonsSet                                                             bool // --addon was passed; the cache does not override it
	Yes                                                                   bool // --yes: accept the cached and flagged choices without drawing the form
	Roles                                                                 provider.Roles
	RolesRemembered                                                       provider.Roles
	AuthVar                                                               string
	HostEnvFile                                                           string
	Evidence                                                              string
	MCP                                                                   string // mcpOn or mcpOff; "" until chosen
	MCPSet                                                                bool   // PROVEO_SBX_MCP was set; the cache and the row do not override it
	PrintOnly                                                             bool
	Extra                                                                 []string
	Clone, CloneSet                                                       bool
	Ports                                                                 []devports.Candidate
	discovered                                                            []devports.Candidate
	portsAsked                                                            bool // the form drew the ports row
	Apps                                                                  []devports.AndroidApp
	discoveredApps                                                        []devports.AndroidApp
	appsAsked                                                             bool // the form drew the apps row
}

// CredentialModes are the --credentials values: forward puts the key in the sandbox env, broker leaves it with sbx's proxy.
func CredentialModes() []string { return []string{"forward", "broker"} }

// ValidCredentials reports whether mode is a --credentials value.
func ValidCredentials(mode string) bool { return slices.Contains(CredentialModes(), mode) }

func (p Params) forwards() bool { return p.Credentials == "forward" }

func (p Params) credentialsOrDefault() string {
	if p.Credentials == "" {
		return "broker"
	}
	return p.Credentials
}

const (
	evidenceLabel   = "outputs"
	evidenceFilter  = "filtered"
	EvidenceVar     = "PROVEO_AGENT_EVIDENCE"
	EvidenceDefault = "default"
	EvidenceVerbose = "verbose"
)

const (
	mcpLabel = "mcp"
	mcpOn    = "on"
	mcpOff   = "off"
)

func (p Params) mcpAllowed() bool { return p.MCP == mcpOn }

func (p Params) evidenceOrDefault() string {
	if p.Evidence == EvidenceDefault {
		return EvidenceDefault
	}
	return EvidenceVerbose
}

func (p *Params) applyCapabilities(man manifest.Manifest) error {
	c := man.Capabilities
	mode := p.credentialsOrDefault()
	if !c.AllowsCredentials(mode) {
		if mode == "broker" {
			return nil // sbx's proxy brokers natively
		}
		if p.CredsSet {
			return fmt.Errorf("%s does not support --credentials %s (allowed: %s)",
				p.Target, mode, strings.Join(c.Credentials, "|"))
		}
		p.Credentials = c.Credentials[0]
	}
	return nil
}

func (p *Params) seedFromCache(cached agentsettings.Choice, lookup func(string) string, evidenceSet bool) {
	if !p.CredsSet && cached.Credentials != "" {
		p.Credentials = cached.Credentials
	}
	if !p.AddonsSet {
		p.Addons = normalizeAddons(cached.Addons)
	}
	p.AddonsAnswered = true
	if p.AuthVar == "" {
		p.AuthVar = cached.AuthVar
	}
	if !evidenceSet && cached.Evidence != "" {
		p.Evidence = cached.Evidence
	}
	if !p.MCPSet && cached.MCP != "" {
		p.MCP = cached.MCP
	}
	if p.LocalModel == "" {
		p.LocalModel = hostTagFor(cached.LocalModel)
		if p.LocalModel == "" {
			p.LocalModel = legacyVariantTag(cached.ModelVariant)
		}
	}
	p.RolesRemembered = provider.RolesFromCanonical(cached.Models)
	p.Roles = provider.RolesFromCanonical(cached.Models)
}

func (p *Params) addonDefaults(opts []string) []bool {
	on := make([]bool, len(opts))
	for i, a := range opts {
		on[i] = hasAddon(p.Addons, a) || (a == addonSandbox && !p.AddonsAnswered)
	}
	return on
}
