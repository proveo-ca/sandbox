package run

import (
	"fmt"
	"strings"

	"github.com/proveo-ca/proveo/internal/agentsettings"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/provider"
)

type Params struct {
	Target, Image, Mode, Credentials, LocalModel, Input, Output, Scope, DataDir string
	ModeSet, CredsSet                                                           bool
	Addons                                                                      []string
	AddonsAnswered                                                              bool // a cached or prompted answer exists; default-on add-ons stop defaulting
	Roles                                                                       provider.Roles
	// RolesRemembered is this agent's own saved answer, kept APART from Roles so
	// model resolution can see the tiers separately: a remembered choice outranks
	// an ambient .env, and each is skipped independently when it cannot
	// authenticate. SPEC: _spec/internal/credentials/credential-decisions.puml
	RolesRemembered provider.Roles
	Bridges         provider.BridgeTable
	AuthVar         string
	// HostEnvFile is the host-side KEY=VALUE file the credential lookup resolved,
	// so the auth row can name where a key actually came from rather than
	// asserting "host env" over a value that lives in the project .env.
	HostEnvFile      string
	Evidence         string
	Shell, PrintOnly bool
	Extra            []string
	// SPEC: _spec/internal/egress/teardown-and-signals.puml
	ProxyImage      string
	Clone, CloneSet bool
}

func (p Params) forwards() bool { return p.Credentials == "forward" }

func (p Params) credentialsOrDefault() string {
	if p.Credentials == "" {
		return "broker"
	}
	return p.Credentials
}

func (p Params) intercepts() bool { return p.Mode != "open" || !p.forwards() }

const (
	evidenceLabel   = "agent evidence"
	EvidenceVar     = "PROVEO_AGENT_EVIDENCE"
	EvidenceDefault = "default"
	EvidenceVerbose = "verbose"
)

func (p Params) evidenceOrDefault() string {
	if p.Evidence == EvidenceDefault {
		return EvidenceDefault
	}
	return EvidenceVerbose
}

func (p *Params) applyCapabilities(c manifest.Capabilities) error {
	if !c.AllowsEgress(p.Mode) {
		if p.ModeSet {
			return fmt.Errorf("%s does not support --egress-mode %s (allowed: %s)",
				p.Target, p.Mode, strings.Join(c.Egress, "|"))
		}
		p.Mode = c.Egress[0]
	}
	if !c.AllowsCredentials(p.credentialsOrDefault()) {
		if p.CredsSet {
			return fmt.Errorf("%s does not support --credentials %s (allowed: %s)",
				p.Target, p.credentialsOrDefault(), strings.Join(c.Credentials, "|"))
		}
		p.Credentials = c.Credentials[0]
	}
	return nil
}

func (p *Params) seedFromCache(cached agentsettings.Choice, lookup func(string) string, evidenceSet bool) {
	if !p.ModeSet && cached.Egress != "" {
		p.Mode = cached.Egress
	}
	if !p.CredsSet && cached.Credentials != "" {
		p.Credentials = cached.Credentials
	}
	p.Addons, p.AddonsAnswered = normalizeAddons(cached.Addons), true
	if p.AuthVar == "" {
		p.AuthVar = cached.AuthVar
	}
	if !evidenceSet && cached.Evidence != "" {
		p.Evidence = cached.Evidence
	}
	p.RolesRemembered = provider.RolesFromCanonical(cached.Models)
	// Kept apart, not merged: resolution needs the tiers separately so a
	// remembered choice can outrank an ambient .env and each can be skipped on
	// its own when it cannot authenticate. Roles stays the merged view for every
	// reader that predates the cascade; ResolveRoles replaces it once the auth
	// answer is known. SPEC: _spec/internal/credentials/credential-decisions.puml
	p.RolesRemembered = provider.RolesFromCanonical(cached.Models)
	p.Roles = posture.MergeRoles(provider.RolesFrom(lookup), cached.Models)
}

func (p *Params) addonDefaults(opts []string) []bool {
	on := make([]bool, len(opts))
	for i, a := range opts {
		on[i] = hasAddon(p.Addons, a) || (a == addonSandbox && !p.AddonsAnswered)
	}
	return on
}

func (p *Params) willSandbox(man manifest.Manifest) bool {
	return sandbox.Selected(man)
}
