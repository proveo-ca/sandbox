package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/proveo-ca/proveo/internal/backend/dockeregress"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/dind"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func cacheApplies(printOnly, tty bool) bool { return !printOnly && WizardEnabled() && tty }

// seedFromCache fills the axes the operator did not state explicitly from a
// remembered answer. Whether the cache applies at all is the caller's decision.
func (p *Params) promptChoices(man manifest.Manifest, lookup func(string) string, repoRoot, homeRoot string) error {
	sbxBackend, sbxWhy := false, ""
	if man.IsSbx() {
		// PROVEO_SBX is consulted HERE as well as at backend selection, or the two
		// disagree: the picker offered a ticked "docker (sandbox)" while the run took
		// the docker backend, so the prompt described a posture the run did not have.
		if !sandbox.Enabled() {
			sbxWhy = "PROVEO_SBX is off"
		} else {
			sbxBackend, sbxWhy = sbx.Available()
		}
	}
	sandboxOn := sbxBackend && p.sandboxAddonOn()
	form := &choiceui.Form{
		Banner: choiceui.Banner(),
		Title:  fmt.Sprintf("run %s — confirm or change this run", p.Target),
		Header: buildHeader(man, lookup, p.Roles, p.Bridges, repoRoot, p.Input, homeRoot),
		Rows: applicableRows(
			egressRow(man, p.Mode, sandboxOn),
			axisRow("credentials", egress.CredentialModes(), man.Capabilities.Credentials, p.credentialsOrDefault()),
		),
	}
	if auth := credentials.AvailableAuthVarsIn(man, lookup, p.Target, homeRoot); len(auth) > 1 {
		form.Rows = append(form.Rows, applicableRows(
			axisRow("auth", auth, auth, orElseFirst(p.AuthVar, auth)),
		)...)
	}
	if addons := addonOptions(man); len(addons) > 0 {
		form.Rows = append(form.Rows, applicableRows(choiceui.Row{
			Label: "add-ons", Options: addons, Multi: true, On: p.addonDefaults(addons),
		})...)
	}
	form.Rows = append(form.Rows, evidenceRow(p.evidenceOrDefault()))
	form.OnChange = func(f *choiceui.Form) {
		gateAddons(f, p.Mode, p.credentialsOrDefault(), sbxWhy)
		// Toggling the sandbox add-on moves the review tier with it: the consent
		// gate has no sbx transport, so review is reachable only off that backend.
		gateReview(f, hasAddon(f.Selections("add-ons"), addonSandbox))
		gateEvidence(f)
	}
	form.OnChange(form)

	ok, err := form.Run()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cancelled at the choice prompt")
	}
	if v := form.Selection("egress"); v != "" && !p.ModeSet {
		p.Mode = v
	}
	if v := form.Selection("credentials"); v != "" && !p.CredsSet {
		p.Credentials = v
	}
	p.Addons, p.AddonsAnswered = form.Selections("add-ons"), true
	if v := form.Selection("auth"); v != "" {
		p.AuthVar = v
	}
	p.Evidence = evidenceFrom(form.Selections(evidenceLabel))
	return nil
}

// evidenceRow offers the two levels as checkboxes with verbose ticked.
func evidenceRow(current string) choiceui.Row {
	opts := []string{EvidenceDefault, EvidenceVerbose}
	on := make([]bool, len(opts))
	for i, o := range opts {
		on[i] = o == current
	}
	return choiceui.Row{Label: evidenceLabel, Options: opts, Multi: true, On: on}
}

// gateEvidence keeps the two evidence boxes exclusive.
func gateEvidence(f *choiceui.Form) {
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Label != evidenceLabel {
			continue
		}
		if r.Selected < 0 || r.Selected >= len(r.On) || !r.On[r.Selected] {
			return
		}
		for j := range r.On {
			if j != r.Selected {
				r.On[j] = false
			}
		}
		return
	}
}

func evidenceFrom(selected []string) string {
	for _, v := range selected {
		if v == EvidenceVerbose {
			return EvidenceVerbose
		}
	}
	return EvidenceDefault
}

func orElseFirst(v string, opts []string) string {
	if v != "" {
		return v
	}
	if len(opts) > 0 {
		return opts[0]
	}
	return ""
}

func gateAddons(f *choiceui.Form, tierFallback, credsFallback, sbxWhy string) {
	tier := f.Selection("egress")
	if tier == "" {
		tier = tierFallback
	}
	creds := f.Selection("credentials")
	if creds == "" {
		creds = credsFallback
	}
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Label != "add-ons" {
			continue
		}
		r.Off = make([]bool, len(r.Options))
		if len(r.On) != len(r.Options) {
			r.On = make([]bool, len(r.Options))
		}
		r.Reason = ""
		for j, opt := range r.Options {
			if opt == addonSandbox {
				// Offered, but only checkable on a host that can actually run it.
				if sbxWhy != "" {
					r.Off[j] = true
					// Greyed AND unticked: a ticked box that cannot be honoured is
					// worse than an absent one, because the operator reads it as the
					// posture of the run rather than as a thing they cannot have.
					r.On[j] = false
					r.Reason = "docker sandbox: " + sbxWhy
				}
				continue
			}
			if opt != addonDind {
				continue
			}
			if !dind.ModeSupported(tier) || !dind.CredentialsSupported(creds) {
				r.Off[j] = true
				r.On[j] = false
				r.Reason = addonDind + " needs egress open + credentials forward"
			}
		}
	}
}

// gateReview re-greys the review tier whenever the sandbox add-on is toggled,
// so the egress row keeps telling the truth about what this run can reach.
func gateReview(f *choiceui.Form, sandboxOn bool) {
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Label != "egress" {
			continue
		}
		if len(r.Off) != len(r.Options) {
			r.Off = make([]bool, len(r.Options))
		}
		for j, opt := range r.Options {
			if opt != "review" {
				continue
			}
			reason := ""
			switch {
			case sandboxOn:
				reason = "review: not supported on the docker sandbox backend"
			default:
				if ok, why := dockeregress.ReviewSupported(os.Getenv); !ok {
					reason = "review: " + why
				}
			}
			r.Off[j] = reason != ""
			switch {
			case reason != "":
				r.Reason = reason
			case strings.HasPrefix(r.Reason, "review:"):
				r.Reason = ""
			}
			if r.Off[j] && r.Selected == j {
				r.Selected = firstSelectableIn(r)
			}
		}
	}
}

// firstSelectableIn is the fallback selection when the current one is greyed out.
func firstSelectableIn(r *choiceui.Row) int {
	for i := range r.Options {
		if i >= len(r.Off) || !r.Off[i] {
			return i
		}
	}
	return 0
}

func axisRow(label string, all, allowed []string, preselect string) choiceui.Row {
	opts := all
	if len(allowed) > 0 {
		opts = allowed
	}
	r := choiceui.Row{Label: label, Options: opts}
	for i, o := range opts {
		if strings.EqualFold(o, preselect) {
			r.Selected = i
		}
	}
	return r
}

// reviewAvailability greys the review option out on hosts whose transport
// cannot carry the gate.
func reviewAvailability(r choiceui.Row, sandboxBackend bool) choiceui.Row {
	if sandboxBackend {
		return comingSoon(r, "review", "review: not supported on the docker sandbox backend")
	}
	if ok, why := dockeregress.ReviewSupported(os.Getenv); !ok {
		return comingSoon(r, "review", "review: "+why)
	}
	return r
}

// comingSoon greys an option out and moves the selection off it.
func comingSoon(r choiceui.Row, option, reason string) choiceui.Row {
	r.Off = make([]bool, len(r.Options))
	for i, o := range r.Options {
		if o != option {
			continue
		}
		r.Off[i] = true
		r.Reason = reason
		if r.Selected == i {
			r.Selected = firstEnabled(r)
		}
	}
	return r
}

func firstEnabled(r choiceui.Row) int {
	for i := range r.Options {
		if i >= len(r.Off) || !r.Off[i] {
			return i
		}
	}
	return 0
}

// applicableRows drops axes with nothing to decide.
// egressRow shows the axis that actually governs THIS backend.
//
// On docker that is proveo's own tier: open|allowlist|review, which the egress
// sidecars enforce and the in-container gate parses.
//
// On sbx it is not. sandbox.Spec derives the Kit allowlist from the harness
// capabilities and the detected providers and never consults the tier, so "open"
// and "allowlist" produce an identical sandbox and "review" falls back to docker
// — a three-option risk axis on which nothing moves. What governs there is sbx's
// GLOBAL baseline, because a Kit only adds allow rules ON TOP of it and a
// per-sandbox deny cannot express "only the allowlist" (deny beats allow). So the
// row is replaced by the real thing, LOCKED: proveo reports the baseline and does
// not set it, since it is host-wide, applies to every sandbox including ones
// proveo never started, and changing it needs `sbx policy reset` — which clears
// every policy on the host. See _spec/internal/sbx/policy-baseline.puml.
// changeBaselineHint is deliberately identical for every baseline. The row is
// DISPLAY ONLY: the baseline is the host's, shared by every sandbox on it, and
// offering it as a per-run choice would teach exactly the wrong intuition — that
// this is a property of the container in front of you. It is not.
const changeBaselineHint = "host-wide, not per-run — to change, run on the host: " +
	"`sbx policy reset && sbx policy init allow-all|balanced|deny-all`"

// policyBaseline is a seam: reading it shells out to sbx, which a unit test has no
// business doing.
var policyBaseline = sbx.PolicyBaseline

func egressRow(man manifest.Manifest, mode string, sandboxOn bool) choiceui.Row {
	if !sandboxOn {
		return reviewAvailability(axisRow("egress", egress.Modes(), man.Capabilities.Egress, mode), false)
	}
	name, known := policyBaseline()
	if !known {
		// Naming a baseline we could not read would put a boundary in front of the
		// operator that may not exist.
		return choiceui.Row{
			Label: "egress", Options: []string{"unreadable"}, Locked: true,
			Reason: "proveo could not read the host baseline (`sbx policy inspect local-policy`). " + changeBaselineHint,
		}
	}
	r := choiceui.Row{Label: "egress", Options: sbx.Baselines(), Locked: true}
	for i, b := range r.Options {
		if b == name {
			r.Selected = i
		}
	}
	// ONE sample, the same whichever baseline is in effect. `init` alone is
	// rejected once the host is initialized ("use sbx policy reset first"), so a
	// sample without the reset would be a command that fails.
	r.Reason = changeBaselineHint
	return r
}

func applicableRows(rows ...choiceui.Row) []choiceui.Row {
	out := make([]choiceui.Row, 0, len(rows))
	for _, r := range rows {
		if r.Multi || len(r.Options) > 1 {
			out = append(out, r)
		}
	}
	return out
}

// The docker add-ons: one row entry per way a harness can hand the agent a
// Docker daemon. Both are CHECKED by default wherever the manifest declares
// them — the picker shows what the run is about to do, and unchecking is how an
// operator opts out (sandbox → docker+egress; dind → no sidecar). Each is still
// subject to its own gate, so an entry can be checked and greyed at once, with
// the reason on the row.
const (
	addonSandbox = "docker (sandbox)"
	addonDind    = "docker (dind)"
)

func addonOptions(man manifest.Manifest) []string {
	var opts []string
	for target := range man.Images {
		if strings.HasSuffix(target, "-browser") {
			opts = append(opts, "browser")
			break
		}
	}
	// One entry, never two: the manifest's docker mode IS the choice, so the
	// picker cannot offer a harness both daemons.
	switch man.Docker {
	case manifest.DockerSbx:
		opts = append(opts, addonSandbox)
	case manifest.DockerDind:
		opts = append(opts, addonDind)
	}
	return opts
}

// sandboxAddonOn reports whether this run takes the sandbox backend. It is
// default-ON: only a remembered or prompted answer can turn it off, so a first
// run — and every non-interactive one — still gets the sandbox.
func normalizeAddons(addons []string) []string {
	out := make([]string, 0, len(addons))
	for _, a := range addons {
		if a == "dind" {
			a = addonDind
		}
		out = append(out, a)
	}
	return out
}

func hasAddon(addons []string, name string) bool {
	for _, a := range addons {
		if a == name {
			return true
		}
	}
	return false
}

// sbxStoredAuth names the harness credentials sbx's own store already holds, and
// is empty on every run that will not use the sbx backend.
// See _spec/_paradigms/credential-boundary.puml.
func buildHeader(man manifest.Manifest, lookup func(string) string, roles provider.Roles, bridges provider.BridgeTable, repoRoot, inputDir, homeRoot string) []string {
	if inputDir == "" {
		inputDir = repoRoot
	}
	h := gitHeader(repoRoot)
	h = append(h, choiceui.EnvHeader(credentials.LoadedSecretNames(man, lookup), loadedSettings(man, lookup))...)
	h = append(h, posture.WorkspaceHeader(man, inputDir, repoRoot, homeRoot, posture.GlyphModeFrom(lookup))...)
	if line := posture.RolesLine(bridges, man.Name, roles); line != "" {
		h = append(h, "llms:     "+line)
	}
	return h
}

func loadedSettings(man manifest.Manifest, lookup func(string) string) map[string]string {
	out := map[string]string{}
	for _, k := range credentials.ConfigVarsFor(man) {
		if v := strings.TrimSpace(lookup(k)); v != "" {
			out[k] = v
		}
	}
	return out
}

func gitHeader(repoRoot string) []string {
	if repoRoot == "" {
		return []string{"git:      (not a repository)"}
	}
	branch := "detached"
	if out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		if b := strings.TrimSpace(string(out)); b != "" {
			branch = b
		}
	}
	dirty := ""
	if out, err := exec.Command("git", "-C", repoRoot, "status", "--porcelain").Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		dirty = " (uncommitted changes)"
	}
	return []string{fmt.Sprintf("git:      %s on %s%s", filepath.Base(repoRoot), branch, dirty)}
}
