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
	Bridges                                                                     provider.BridgeTable
	AuthVar                                                                     string
	Evidence                                                                    string
	Shell, PrintOnly                                                            bool
	Extra                                                                       []string
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
