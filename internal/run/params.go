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
	// Clone is the REQUEST: --clone / PROVEO_CLONE. Whether the run actually
	// clones is Spec.Backend.Clone, settled by decideClone once the backend and
	// the workspace shape are known. CloneSet records an explicit flag, which
	// turns "cannot clone here" from a fallback into an error.
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

// Agent evidence: how much of its own work the harness narrates.
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

// policyProviderHosts names the endpoints the egress DLP must treat as
// on-provider: where a credential this run legitimately holds is allowed to go.
// It unions the detected providers' hosts with the ones the manifest declares
// the harness can use, because the second set is the only one a SUBSCRIPTION
// harness has — it logs in inside the sandbox, so no key is detectable
// host-side, yet the token it mints still has to reach the vendor.
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

func (p *Params) sandboxAddonOn() bool {
	return hasAddon(p.Addons, addonSandbox) || !p.AddonsAnswered
}

// addonDefaults is the picker's initial checkbox state: a remembered answer
// wins, and absent one BOTH docker add-ons start checked — the run is going to
// use them, so the box that says so is ticked before the operator is asked.
func (p *Params) addonDefaults(opts []string) []bool {
	on := make([]bool, len(opts))
	for i, a := range opts {
		on[i] = hasAddon(p.Addons, a) ||
			((a == addonSandbox || a == addonDind) && !p.AddonsAnswered)
	}
	return on
}

// normalizeAddons upgrades the names a previous version remembered, so a cached
// choice keeps meaning what the operator picked.
func (p *Params) willSandbox(man manifest.Manifest) bool {
	return sandbox.Selected(man) && p.sandboxAddonOn()
}

// reviewConsent builds the terminal half of the review tier: a pty overlay that
// asks, and the answer as a plain bool. The backend takes this as a callback, so
// it owns the gate and the socket while never drawing a prompt — and a test can
// pass deny-all without a terminal.
