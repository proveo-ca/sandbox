// SPEC: _spec/defs/claudecode/chrome-bridge.puml, _spec/internal/choiceui/wireframe.puml
package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/backend/dockeregress"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/chromebridge"
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
			sbxWhy = "PROVEO_SBX=0 is set"
		} else {
			sbxBackend, sbxWhy = sbx.Available()
		}
	}
	// The add-on does not vote any more: a harness that declares sbx runs there
	// whenever the host can, so availability alone decides which egress axis the
	// operator is shown. Consulting the remembered answer here offered the docker
	// tiers — open/allowlist/review — for a run that was going to sbx and read the
	// host baseline instead, and hid changeBaselineHint, the one true statement
	// about what governs egress there.
	sandboxOn := sbxBackend
	chromeWhy := ""
	if man.Capabilities.HasHostBrowser() {
		chromeWhy = chromeUnavailable(lookup, p.Target, homeRoot)
	}
	form := &choiceui.Form{
		Banner: choiceui.Banner(),
		Title:  fmt.Sprintf("run %s — confirm or change this run", p.Target),
		Header: buildHeader(man, lookup, p.Roles, p.Bridges, repoRoot, p.Input, homeRoot),
		// The same lookup buildHeader uses, so one project .env governs the
		// header's devicons and the strip's together.
		Glyphs:   stripGlyphs(posture.GlyphModeFrom(lookup)),
		Topology: topologyOf(man, p.Target, sbxBackend, p.Mode, p.credentialsOrDefault()),
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
	for _, label := range addonRows {
		opts := addonOptions(man, label)
		if len(opts) == 0 {
			continue
		}
		form.Rows = append(form.Rows, applicableRows(choiceui.Row{
			Label: label, Options: opts, Multi: true, Divider: true,
			On: p.addonDefaults(opts), Help: addonHelp,
		})...)
	}
	form.Rows = append(form.Rows, evidenceRow(p.evidenceOrDefault()))
	form.OnChange = func(f *choiceui.Form) {
		gateAddons(f, p.Mode, p.credentialsOrDefault(), sbxWhy, chromeWhy)
		// Toggling the sandbox add-on moves the review tier with it: the consent
		// gate has no sbx transport, so review is reachable only off that backend.
		gateReview(f, hasAddon(selectedAddons(f), addonSandbox))
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
	p.Addons, p.AddonsAnswered = selectedAddons(form), true
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

func gateAddons(f *choiceui.Form, tierFallback, credsFallback, sbxWhy, chromeWhy string) {
	tier := f.Selection("egress")
	if tier == "" {
		tier = tierFallback
	}
	creds := f.Selection("credentials")
	if creds == "" {
		creds = credsFallback
	}
	// Read from the form, not from the row in hand: the sandbox box lives in the
	// execution group and the Chrome box it excludes lives in the interface one,
	// so the two are no longer neighbours in the same slice.
	// Read from the row's OPTIONS, not from On: the loop below is what ticks the
	// compulsory box, so asking On here answers from the previous pass and leaves
	// the host browser drawn live for one whole paint — long enough to accept.
	sandboxTicked := sbxWhy == "" && rowOffers(f, rowExecution, addonSandbox)
	for i := range f.Rows {
		r := &f.Rows[i]
		if !isAddonRow(r.Label) {
			continue
		}
		r.Off = make([]bool, len(r.Options))
		if len(r.On) != len(r.Options) {
			r.On = make([]bool, len(r.Options))
		}
		// Rebuilt from scratch on every toggle, like Off: these constraints move
		// with the other rows, and a stale entry would explain a box that is
		// available again.
		r.OffWhy = map[string]string{}
		var reasons []string
		for j, opt := range r.Options {
			// A fact, not a choice: greyed, and left in the state it states.
			if fixed, ok := addonFixed[opt]; ok {
				r.Off[j] = true
				r.On[j] = fixed.on
				r.OffWhy[opt] = fixed.why
				// Deliberately NOT in reasons: these are greyed on every run, and an
				// inline note that never changes teaches the operator to skip the
				// line that sometimes matters.
				continue
			}
			switch opt {
			case addonSandbox:
				// A fact in both directions, greyed either way.
				r.Off[j] = true
				if sbxWhy != "" {
					// Greyed AND unticked: a ticked box that cannot be honoured is
					// worse than an absent one, because the operator reads it as the
					// posture of the run rather than as a thing they cannot have.
					r.On[j] = false
					reasons = append(reasons, "docker sandbox: "+sbxWhy)
					r.OffWhy[opt] = sbxWhy
					break
				}
				// Greyed AND ticked: compulsory, and out of `reasons`.
				r.On[j] = true
				r.OffWhy[opt] = "this harness runs in the sandbox and nowhere else; PROVEO_SBX=0 or --egress-mode review fall back to docker + egress sidecars"
			case addonDind:
				if !dind.ModeSupported(tier) || !dind.CredentialsSupported(creds) {
					r.Off[j] = true
					r.On[j] = false
					reasons = append(reasons, addonDind+" needs egress open + credentials forward")
					r.OffWhy[opt] = "needs egress open + credentials forward"
				}
			case addonChrome:
				// The bridge is a TCP relay on the host that the agent reaches as
				// host.docker.internal — which only the docker backend's bridge network
				// resolves to the host, and only on the open+forward tier (every other
				// tier parks the agent behind a sidecar with no route out but the proxy).
				// A sandbox VM cannot name the host at all, so the sbx box and this one
				// are exclusive, and re-gated on every toggle.
				why := chromeWhy
				switch {
				case why != "":
				case sandboxTicked:
					why = "docker backend only — set PROVEO_SBX=0"
				case !dind.ModeSupported(tier) || !dind.CredentialsSupported(creds):
					why = "needs egress open + credentials forward"
				}
				if why != "" {
					r.Off[j] = true
					r.On[j] = false
					reasons = append(reasons, addonChrome+": "+why)
					r.OffWhy[opt] = why
				}
			}
		}
		r.Reason = strings.Join(reasons, " · ")
	}
}

// chromeUnavailable is the host-side preflight for the Claude in Chrome add-on:
// what, at this moment, would stop the bridge from connecting. Empty means go.
//
// The credential check is Claude Code's rule, not proveo's, and it is a rule about
// SCOPES rather than about variables: see chromebridge.ScopeGate. Reading it as
// "env-var sessions are refused" greyed the box for a correctly-scoped token that
// would have connected, and for an ANTHROPIC_API_KEY set beside a login the key
// does not displace. Offering the box would sell a bridge to a client that has
// already decided not to use it; withholding it hides one that works.
func chromeUnavailable(lookup func(string) string, target, homeRoot string) string {
	if why := chromebridge.ScopeGate(lookup, credentials.HasPersistedLogin(target, homeRoot)); why != "" {
		return why
	}
	if ok, why := chromebridge.Available(chromebridge.HostSocketDir()); !ok {
		return why
	}
	return ""
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
	// The two planes the checkboxes are grouped into: WHERE the agent runs, and
	// WHAT it can drive. One undifferentiated "add-ons" row put a Docker daemon
	// beside a browser as though they answered the same question.
	rowExecution = "execution"
	rowInterface = "interface"

	addonHost    = "host"
	addonTUI     = "tui (this session)"
	addonBrowser = "browser"
	addonSandbox = "docker (sandbox)"
	addonDind    = "docker (dind)"
	addonChrome  = chromebridge.Addon
)

// addonRows is the order the two groups are drawn and read back in.
var addonRows = []string{rowExecution, rowInterface}

func isAddonRow(label string) bool { return label == rowExecution || label == rowInterface }

// addonFixed are the boxes that state a FACT rather than offer a choice: their
// state is never the operator's to change, and drawing them is how the plane
// says what it excludes. "host" is the alternative an operator might otherwise
// assume unticking the daemon selects — it never is, because proveo containerises
// every run — and the TUI is the interface for the whole session whether or not
// a browser joins it.
//
// They are greyed but keep their true state: a ticked greyed box reads as
// compulsory, which is exactly what it is, and the help line below says "always
// on" rather than "off" for one.
var addonFixed = map[string]struct {
	on  bool
	why string
}{
	addonHost: {why: "the agent can't touch your whole machine"},
	addonTUI:  {on: true, why: "there is no headless mode to pick instead — the boxes beside it ADD to this terminal"},
}

// addonHelp is what each add-on DOES, in one line, shown under the row while the
// cursor is on that box.
//
// Naming the alternative is half the job and the half that was missing: an
// unticked box is a posture too, and "docker (sandbox)" said nothing about what
// unticking it selects. It is not the host — proveo never runs an agent there —
// it is the docker backend behind egress sidecars, and an operator reading only
// the checkbox had no way to know that.
var addonHelp = map[string]string{
	addonHost:    "your own machine, with your files and your credentials — not a place proveo will run an agent",
	addonTUI:     "this terminal — the agent's transcript and your prompts, for the whole run",
	addonBrowser: "Chromium inside the sandbox (Playwright + agent-browser) — the agent's own browser",
	addonChrome:  "your own Chrome, with your profile and logins, driven through proveo's bridge",
	addonSandbox: "a microVM with its own Docker daemon (sbx) — the boundary every run on this harness gets",
	addonDind:    "a privileged sibling Docker daemon; unticked: no daemon reaches the agent",
}

// executionOptions is WHERE the agent runs: the excluded host, then the daemon
// this harness declares. One entry, never two — the manifest's docker mode IS
// the choice, so the picker cannot offer a harness both daemons.
func executionOptions(man manifest.Manifest) []string {
	opts := []string{addonHost}
	switch man.Docker {
	case manifest.DockerSbx:
		opts = append(opts, addonSandbox)
	case manifest.DockerDind:
		opts = append(opts, addonDind)
	}
	return opts
}

// interfaceOptions is WHAT the agent can drive. "browser" is a Chromium INSIDE
// the sandbox (the -browser image variant); "chrome (host browser)" is the
// operator's own Chrome, reached through the Claude in Chrome bridge. Different
// things, so both can be offered at once.
func interfaceOptions(man manifest.Manifest) []string {
	opts := []string{addonTUI}
	for target := range man.Images {
		if strings.HasSuffix(target, "-browser") {
			opts = append(opts, addonBrowser)
			break
		}
	}
	if man.Capabilities.HasHostBrowser() {
		opts = append(opts, addonChrome)
	}
	return opts
}

func addonOptions(man manifest.Manifest, label string) []string {
	if label == rowExecution {
		return executionOptions(man)
	}
	return interfaceOptions(man)
}

// selectedAddons reads both groups back as the one list the rest of the run
// consumes, so splitting the row changed the picker and nothing downstream.
func selectedAddons(f *choiceui.Form) []string {
	var out []string
	for _, label := range addonRows {
		out = append(out, f.Selections(label)...)
		out = append(out, compulsory(f, label)...)
	}
	return out
}

// compulsory are the ticked boxes of one group that "Selections" drops because
// they are greyed. See _spec/internal/choiceui/choice-prompt-render.puml.
func compulsory(f *choiceui.Form, label string) []string {
	var out []string
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Label != label {
			continue
		}
		for j, opt := range r.Options {
			if _, declared := addonFixed[opt]; declared {
				continue
			}
			if j < len(r.On) && r.On[j] && j < len(r.Off) && r.Off[j] {
				out = append(out, opt)
			}
		}
		break
	}
	return out
}

// rowOffers reports whether a group lists an option at all, which is a different
// question from whether it is ticked: the execution row carries the sandbox only
// on a harness that declares it, and a dind harness must not be read as one.
func rowOffers(f *choiceui.Form, label, option string) bool {
	for i := range f.Rows {
		if f.Rows[i].Label != label {
			continue
		}
		return slices.Contains(f.Rows[i].Options, option)
	}
	return false
}

// rowTicked reports whether one option of one group is checked, reading On
// directly: gating runs before Off is recomputed, so Selections would answer
// from a stale gate on the first pass.
func rowTicked(f *choiceui.Form, label, option string) bool {
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Label != label {
			continue
		}
		for j, opt := range r.Options {
			if opt == option && j < len(r.On) && r.On[j] {
				return true
			}
		}
	}
	return false
}

// normalizeAddons upgrades the names a previous version remembered, so a cached
// choice keeps meaning what the operator picked.
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
