// SPEC: _spec/defs/claudecode/chrome-bridge.puml,
// _spec/internal/choiceui/wireframe.puml
//
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
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func cacheApplies(printOnly, tty bool) bool { return !printOnly && WizardEnabled() && tty }

func (p *Params) promptChoices(man manifest.Manifest, lookup func(string) string, repoRoot, homeRoot string) error {
	sbxBackend, sbxWhy := false, ""
	if man.IsSbx() {
		if !sandbox.Enabled() {
			sbxWhy = "PROVEO_SBX=0 is set"
		} else {
			sbxBackend, sbxWhy = sbx.Available()
		}
	}
	sandboxOn := sbxBackend
	chromeWhy := ""
	if man.Capabilities.HasHostBrowser() {
		chromeWhy = chromeUnavailable(man, lookup, p.AuthVar, p.Target, homeRoot)
	}
	form := &choiceui.Form{
		Banner:   choiceui.Banner(),
		Title:    fmt.Sprintf("run %s — confirm or change this run", p.Target),
		Header:   buildHeader(man, lookup, p.Roles, p.Bridges, repoRoot, p.Input, homeRoot),
		Glyphs:   posture.GlyphModeFrom(lookup),
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
			Radio: label == rowExecution,
			On:    p.addonDefaults(opts), Help: addonHelp,
		})...)
	}
	form.Rows = append(form.Rows, evidenceRow(p.evidenceOrDefault()))
	form.OnChange = func(f *choiceui.Form) {
		gateAddons(f, p.Mode, p.credentialsOrDefault(), sbxWhy, chromeWhy)
		gateReview(f, hasAddon(selectedAddons(f), addonSandbox))
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
	if v := form.Selection(evidenceLabel); v != "" {
		p.Evidence = v
	}
	return nil
}

func evidenceRow(current string) choiceui.Row {
	opts := []string{EvidenceDefault, EvidenceVerbose}
	sel := 0
	if current == EvidenceVerbose {
		sel = 1
	}
	return choiceui.Row{Label: evidenceLabel, Options: opts, Selected: sel}
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
		r.OffWhy = map[string]string{}
		var reasons []string
		for j, opt := range r.Options {
			if fixed, ok := addonFixed[opt]; ok {
				r.Off[j] = true
				r.On[j] = fixed.on
				r.OffWhy[opt] = fixed.why
				continue
			}
			switch opt {
			case addonSandbox:
				r.Off[j] = true
				if sbxWhy != "" {
					r.On[j] = false
					reasons = append(reasons, "docker sandbox: "+sbxWhy)
					r.OffWhy[opt] = sbxWhy
					break
				}
				r.On[j] = true
				r.OffWhy[opt] = "this harness runs in the sandbox and nowhere else; PROVEO_SBX=0 or --egress-mode review fall back to docker + egress sidecars"
			case addonChrome:
				why := chromeWhy
				switch {
				case why != "":
				case sandboxTicked: // sbx: no tier gate, the tier is inert there
				case !chromebridge.TierSupported(tier, creds):
					why = chromebridge.TierWhy
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

func chromeUnavailable(man manifest.Manifest, lookup func(string) string, chosen, target, homeRoot string) string {
	suppressed := credentials.AuthSuppressor(man, target, chosen, homeRoot)
	effective := func(k string) string {
		if suppressed(k) {
			return ""
		}
		return lookup(k)
	}
	if effective(chromebridge.EnvOAuthToken) == "" &&
		effective(chromebridge.EnvOAuthTokenFD) == "" &&
		credentials.LoginBlanked(target, homeRoot) {
		return "the login in the proveo home is empty (macOS keeps it in the Keychain, " +
			"which the container cannot read) — /login INSIDE the run to put one there"
	}
	if why := chromebridge.ScopeGate(effective, suppressed(chromebridge.EnvOAuthToken)); why != "" {
		return why
	}
	if ok, why := chromebridge.Available(chromebridge.HostSocketDir()); !ok {
		return why
	}
	return ""
}

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

func reviewAvailability(r choiceui.Row, sandboxBackend bool) choiceui.Row {
	if sandboxBackend {
		return comingSoon(r, "review", "review: not supported on the docker sandbox backend")
	}
	if ok, why := dockeregress.ReviewSupported(os.Getenv); !ok {
		return comingSoon(r, "review", "review: "+why)
	}
	return r
}

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

const changeBaselineHint = "host-wide, not per-run — to change, run on the host: " +
	"`sbx policy reset && sbx policy init allow-all|balanced|deny-all`"

var policyBaseline = sbx.PolicyBaseline

func egressRow(man manifest.Manifest, mode string, sandboxOn bool) choiceui.Row {
	if !sandboxOn {
		return reviewAvailability(axisRow("egress", egress.Modes(), man.Capabilities.Egress, mode), false)
	}
	name, known := policyBaseline()
	if !known {
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

const (
	rowExecution = "execution"
	rowInterface = "interface"

	addonHost    = "host"
	addonTUI     = "tui (this session)"
	addonBrowser = "browser"
	addonSandbox = "docker (sandbox)"
	addonChrome  = chromebridge.Addon
)

var addonRows = []string{rowExecution, rowInterface}

func isAddonRow(label string) bool { return label == rowExecution || label == rowInterface }

var addonFixed = map[string]struct {
	on  bool
	why string
}{
	addonHost: {why: "the agent can't touch your whole machine"},
	addonTUI:  {on: true, why: "there is no headless mode to pick instead — the boxes beside it ADD to this terminal"},
}

var addonHelp = map[string]string{
	addonHost:    "your own machine, with your files and your credentials — not a place proveo will run an agent",
	addonTUI:     "this terminal — the agent's transcript and your prompts, for the whole run",
	addonBrowser: "Chromium inside the sandbox (Playwright + agent-browser) — the agent's own browser",
	addonChrome:  "Claude Code drives YOUR Chrome — your profile, your logins — over proveo's bridge",
	addonSandbox: "a microVM with its own Docker daemon (sbx) — the boundary every run on this harness gets",
}

// SPEC: _spec/_plans/retire-dind.puml
func executionOptions(man manifest.Manifest) []string {
	opts := []string{addonHost}
	if man.Docker == manifest.DockerSbx {
		opts = append(opts, addonSandbox)
	}
	return opts
}

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

func selectedAddons(f *choiceui.Form) []string {
	var out []string
	for _, label := range addonRows {
		out = append(out, f.Selections(label)...)
		out = append(out, compulsory(f, label)...)
	}
	return out
}

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

func rowOffers(f *choiceui.Form, label, option string) bool {
	for i := range f.Rows {
		if f.Rows[i].Label != label {
			continue
		}
		return slices.Contains(f.Rows[i].Options, option)
	}
	return false
}

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

// selection. SPEC: _spec/_plans/retire-dind.puml
func normalizeAddons(addons []string) []string {
	out := make([]string, 0, len(addons))
	for _, a := range addons {
		if a == "dind" || a == "docker (dind)" {
			continue
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
