// SPEC: _spec/defs/claudecode/chrome-bridge.puml, _spec/internal/choiceui/wireframe.puml, _spec/internal/credentials/credential-decisions.puml, _spec/internal/agentsettings/choice-cache.puml, _spec/_paradigms/retire-dind.puml, _spec/internal/sbx/credential-path.puml
package run

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/agentio"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/chromebridge"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/devports"
	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/hostcdp"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func cacheApplies(printOnly, tty bool) bool { return !printOnly && agentio.WizardEnabled() && tty }

func (p *Params) promptChoices(man manifest.Manifest, lookup func(string) string, repoRoot, homeRoot string) error {
	chromeWhy := ""
	if man.Capabilities.HostBrowser == addonChrome {
		chromeWhy = chromeUnavailable(man, lookup, p.AuthVar, p.Target, homeRoot)
	}
	form := &choiceui.Form{
		Banner:   choiceui.Banner(),
		Title:    fmt.Sprintf("run %s — confirm or change this run", p.Target),
		Header:   buildHeader(man, lookup, p.Roles, repoRoot, p.Input, homeRoot),
		Glyphs:   posture.GlyphModeFrom(lookup),
		Topology: topologyOf(p.Target, p.credentialsOrDefault()),
		Rows: append(
			applicableRows(egressRow()),
			credentialsRow(man, p.credentialsOrDefault()),
		),
	}
	auth, hasAuth := authRow(man, lookup, p.Target, homeRoot, p.HostEnvFile, p.AuthVar)
	if r, ok := sourceRow(man, auth, hasAuth, p.LocalModel); ok {
		form.Rows = append(form.Rows, r)
	} else {
		form.NoAxis = true
	}
	found := p.discovered
	var apps []devports.AndroidApp
	if slices.Contains(interfaceOptions(man), addonAndroid) {
		apps = p.discoveredApps
	}
	for _, label := range addonRows {
		opts := addonOptions(man, label)
		if len(opts) == 0 {
			continue
		}
		row := choiceui.Row{
			Label: label, Heading: addonHeading[label], Options: opts, Multi: true, Divider: true,
			Radio: label == rowExecution,
			On:    p.addonDefaults(opts), Help: addonHelpFor(man),
		}
		switch label {
		case rowInterface:
			row = layoutInterface(row)
		case rowExecution:
			row = layoutExecution(row)
		}
		form.Rows = append(form.Rows, applicableRows(row)...)
		if label == rowExecution {
			if r, ok := portsRow(found, p.Ports); ok {
				form.Rows = append(form.Rows, r)
				p.portsAsked = true
			}
			if r, ok := appsRow(apps, p.Apps); ok {
				form.Rows = append(form.Rows, r)
				p.appsAsked = true
			}
		}
	}
	form.Rows = append(form.Rows, evidenceRow(p.evidenceOrDefault()))
	form.StartRow = rowInterface
	for i := range form.Rows {
		r := &form.Rows[i]
		if r.Label != rowInterface {
			continue
		}
		form.StartOption = firstChoice(r)
		break
	}
	form.OnChange = func(f *choiceui.Form) {
		gateAddons(f, chromeWhy)
		gateApps(f)
	}
	form.OnChange(form)

	ok, err := form.Run()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cancelled at the choice prompt")
	}
	if v := form.Selection("credentials"); v != "" && !p.CredsSet {
		p.Credentials = v
	}
	p.Addons, p.AddonsAnswered = selectedAddons(form), true
	if p.portsAsked {
		p.Ports = selectedPorts(form, found)
	}
	if p.appsAsked {
		p.Apps = selectedApps(form, apps)
	}
	if v := form.Selection(rowModel); v != "" {
		p.applySource(v, hasAuth)
	}
	if v := form.Selection(evidenceLabel); v != "" {
		p.Evidence = evidenceOf(v)
	}
	return nil
}

func authRow(man manifest.Manifest, lookup func(string) string, target, homeRoot, envFile, chosen string) (choiceui.Row, bool) {
	if !credentials.DeclaresSubscription(man) {
		return choiceui.Row{}, false
	}
	available := credentials.AvailableAuthVarsIn(man, lookup, target, homeRoot)
	if len(available) == 0 {
		return choiceui.Row{}, false
	}
	opts := []string{credentials.AuthUsage, credentials.AuthSubscription}
	r := axisRow("auth", opts, opts, orElseFirst(authAnswer(chosen, available), preferSubscription(available)))
	r.Help = authHelp(man, lookup, target, homeRoot, envFile)
	r.Off = make([]bool, len(r.Options))
	r.OffWhy = map[string]string{}
	why := credentials.AuthWhyUnavailable(man, target, homeRoot)
	var reasons []string
	for i, opt := range r.Options {
		if slices.Contains(available, opt) {
			continue
		}
		r.Off[i] = true
		r.OffWhy[opt] = why[opt]
		reasons = append(reasons, opt+": "+why[opt])
	}
	r.Reason = strings.Join(reasons, " · ")
	if r.Selected < len(r.Off) && r.Off[r.Selected] {
		r.Selected = firstSelectableIn(&r)
	}
	return r, true
}

// preferSubscription puts the plan side first, so a run bills against it whenever it can.
func preferSubscription(available []string) []string {
	if !slices.Contains(available, credentials.AuthSubscription) {
		return available
	}
	return append([]string{credentials.AuthSubscription}, slices.DeleteFunc(slices.Clone(available),
		func(s string) bool { return s == credentials.AuthSubscription })...)
}

func authAnswer(chosen string, available []string) string {
	switch {
	case chosen == "":
		return ""
	case slices.Contains(available, chosen):
		return chosen
	case chosen == credentials.AuthVarLogin:
		return credentials.AuthSubscription
	}
	return "" // a variable name from an older cache: let availability decide
}

func authHelp(man manifest.Manifest, lookup func(string) string, target, homeRoot, envFile string) map[string]string {
	backing := credentials.AuthBacking(man, lookup, target, homeRoot, envFile)
	unavailable := credentials.AuthWhyUnavailable(man, target, homeRoot)
	usageIs := "metered per token at each provider, on your own keys"
	if credentials.VendorPinnedWhy(man) != "" {
		usageIs = "metered by the vendor beyond the plan's included usage — there is no " +
			"bring-your-own-key path, so a provider key of yours authenticates nothing here"
	}
	what := map[string]string{
		credentials.AuthUsage:        usageIs,
		credentials.AuthSubscription: "billed against the plan " + man.Name + "'s vendor issues",
	}
	// What choosing it costs the other side. Only meaningful where it can be chosen.
	withholds := map[string]string{
		credentials.AuthUsage:        man.Name + "'s own credential is withheld",
		credentials.AuthSubscription: "every provider key is withheld",
	}
	help := map[string]string{}
	for opt, text := range what {
		if b := backing[opt]; b != "" {
			help[opt] = text + " · " + b
			if w := withholds[opt]; w != "" {
				help[opt] += " · " + w
			}
			continue
		}
		help[opt] = text + " · " + unavailable[opt]
	}
	if caveat := credentials.BillingCaveat(man, lookup); caveat != "" {
		for _, opt := range []string{credentials.AuthUsage, credentials.AuthSubscription} {
			if b := backing[opt]; b != "" {
				help[opt] = what[opt] + " · " + b + " · " + caveat
			}
		}
	}
	return help
}

// evidenceOf is the PROVEO_AGENT_EVIDENCE value an outputs answer stands for.
// SPEC: _spec/internal/choiceui/choice-prompt-render.puml
func evidenceOf(answer string) string {
	if answer == evidenceFilter {
		return EvidenceDefault
	}
	return answer
}

func evidenceRow(current string) choiceui.Row {
	opts := []string{evidenceFilter, EvidenceVerbose}
	sel := 0
	if current == EvidenceVerbose {
		sel = 1
	}
	return pinCols(choiceui.Row{Label: evidenceLabel, Options: opts, Selected: sel}, evidenceColumn)
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

func gateAddons(f *choiceui.Form, chromeWhy string) {
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
				r.On[j] = true
				r.OffWhy[opt] = "Mount the current git project in a sandbox for the agent"
			case addonIOS:
				r.Off[j] = true
				r.On[j] = false
				reasons = append(reasons, addonIOS+": "+hostadb.IOSWhy)
				r.OffWhy[opt] = hostadb.IOSWhy
			case addonChrome:
				why := chromeWhy
				if why == "" {
					why = chromebridge.SbxWhy
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
	suppressed := credentials.AuthSuppressor(man, target, chosen, homeRoot, lookup)
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

// firstChoice is the first option on a row the operator can actually change.
func firstChoice(r *choiceui.Row) string {
	for _, opt := range r.Options {
		if opt == addonBrowser {
			return opt
		}
	}
	for _, opt := range r.Options {
		if _, fixed := addonFixed[opt]; !fixed {
			return opt
		}
	}
	return ""
}

func firstSelectableIn(r *choiceui.Row) int {
	for i := range r.Options {
		if i >= len(r.Off) || !r.Off[i] {
			return i
		}
	}
	return 0
}

func pinCols(r choiceui.Row, col func(string) int) choiceui.Row {
	r.Cols = make([]int, len(r.Options))
	for i, opt := range r.Options {
		r.Cols[i] = col(opt)
	}
	return r
}

func egressColumn(name string) int {
	switch name {
	case sbx.BaselineBalanced:
		return 2
	case sbx.BaselineDenyAll:
		return 3
	default:
		return 0
	}
}

func credentialColumn(name string) int {
	if name == "broker" {
		return 3
	}
	return 0
}

func executionColumn(name string) int {
	if name == addonSandbox {
		return 1
	}
	return 0
}

func layoutExecution(r choiceui.Row) choiceui.Row {
	r = pinCols(r, executionColumn)
	r.Paint = map[string]string{addonSandbox: "sandbox"}
	return r
}

func evidenceColumn(name string) int {
	if name == EvidenceVerbose {
		return 1
	}
	return 0
}

func interfaceColumn(name string) int {
	switch name {
	case addonTUI:
		return 0
	case addonBrowser:
		return 1
	case addonAndroid:
		return 2
	case addonIOS:
		return 3
	default:
		return 4
	}
}

func layoutInterface(r choiceui.Row) choiceui.Row {
	type item struct {
		opt string
		on  bool
		col int
	}
	used := map[int]bool{}
	items := make([]item, len(r.Options))
	for i, opt := range r.Options {
		col := interfaceColumn(opt)
		for used[col] {
			col++
		}
		used[col] = true
		on := i < len(r.On) && r.On[i]
		items[i] = item{opt, on, col}
	}
	slices.SortStableFunc(items, func(a, b item) int { return cmp.Compare(a.col, b.col) })
	r.Paint = map[string]string{addonChrome: "chrome"}
	r.Options = make([]string, len(items))
	r.On = make([]bool, len(items))
	r.Cols = make([]int, len(items))
	for i, it := range items {
		r.Options[i] = it.opt
		r.On[i] = it.on
		r.Cols[i] = it.col
	}
	return r
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

func credentialsRow(man manifest.Manifest, mode string) choiceui.Row {
	r := pinCols(axisRow("credentials", CredentialModes(), nil, mode), credentialColumn)
	r = comingSoon(r, "forward", "sbx's proxy uses Secrets Manager. Away from the VM and the agent, attached in each request")
	r.Locked = true
	r.Hover = r.Selected
	if caps := man.Capabilities.Credentials; len(caps) > 0 && !slices.Contains(caps, "broker") {
		r.Help = map[string]string{"broker": man.Name + " always brokers credentials natively on sbx — its proxy attaches the key"}
	}
	return r
}

// egressRow shows sbx's host-wide baseline, which decides egress; proveo only reports it.
func egressRow() choiceui.Row {
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
	return pinCols(r, egressColumn)
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
	rowExecution = "OS"
	rowInterface = "interface"
	rowPorts     = "ports"
	rowApps      = "apps"

	addonHost    = "host"
	addonTUI     = "tui"
	addonBrowser = "browser"
	addonSandbox = "docker (sandbox)"
	addonChrome  = chromebridge.Addon
	addonHostCDP = hostcdp.Addon
	addonAndroid = hostadb.Addon
	addonIOS     = hostadb.AddonIOS
)

var addonRows = []string{rowExecution, rowInterface}

// addonHeading names a divider whose group is not named for its row.
var addonHeading = map[string]string{rowExecution: "execution"}

func scanRoot(input, repoRoot string) string {
	if input != "" {
		return input
	}
	return OrWD(repoRoot)
}

// portsRow offers each discovered run command's port for publishing; every box starts unticked.
func portsRow(found []devports.Candidate, chosen []devports.Candidate) (choiceui.Row, bool) {
	if len(found) == 0 {
		return choiceui.Row{}, false
	}
	r := choiceui.Row{Label: rowPorts, Multi: true, Help: map[string]string{}}
	for _, c := range found {
		opt := c.Label()
		r.Options = append(r.Options, opt)
		r.On = append(r.On, slices.ContainsFunc(chosen, func(x devports.Candidate) bool { return x.Port == c.Port }))
		how := "the tool's default port"
		if c.Explicit {
			how = "the port the command names"
		}
		r.Help[opt] = fmt.Sprintf("publish sandbox :%d to this host's loopback — %q in %s, %s", c.Port, c.Command, c.Source, how)
	}
	return pinSequence(r), true
}

// appsRow offers each Android application module for build + install + launch on the host emulator.
func appsRow(found []devports.AndroidApp, chosen []devports.AndroidApp) (choiceui.Row, bool) {
	if len(found) == 0 {
		return choiceui.Row{}, false
	}
	r := choiceui.Row{Label: rowApps, Multi: true, Help: map[string]string{}}
	for _, a := range found {
		opt := a.Label()
		r.Options = append(r.Options, opt)
		r.On = append(r.On, slices.ContainsFunc(chosen, func(x devports.AndroidApp) bool { return x.Module == a.Module }))
		r.Help[opt] = fmt.Sprintf("once the sandbox toolchain is ready: gradle %s, adb install, then launch %s on the host emulator — %s", a.Task(), a.AppID, a.Source)
	}
	return pinSequence(r), true
}

func pinSequence(r choiceui.Row) choiceui.Row {
	r.Cols = make([]int, len(r.Options))
	for i := range r.Options {
		r.Cols[i] = i
	}
	return r
}

const appsNeedAndroid = "needs [x] android under interface: the install goes to the emulator that add-on boots"

// gateApps greys the apps row while android is unticked.
func gateApps(f *choiceui.Form) {
	android := rowTicked(f, rowInterface, addonAndroid)
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Label != rowApps {
			continue
		}
		r.Off = make([]bool, len(r.Options))
		r.OffWhy = map[string]string{}
		r.Reason = ""
		if android {
			return
		}
		for j, opt := range r.Options {
			r.Off[j] = true
			r.OffWhy[opt] = appsNeedAndroid
		}
		r.Reason = appsNeedAndroid
	}
}

func selectedApps(f *choiceui.Form, found []devports.AndroidApp) []devports.AndroidApp {
	picked := f.Selections(rowApps)
	var out []devports.AndroidApp
	for _, a := range found {
		if slices.Contains(picked, a.Label()) {
			out = append(out, a)
		}
	}
	return out
}

func selectedPorts(f *choiceui.Form, found []devports.Candidate) []devports.Candidate {
	picked := f.Selections(rowPorts)
	var out []devports.Candidate
	for _, c := range found {
		if slices.Contains(picked, c.Label()) {
			out = append(out, c)
		}
	}
	return out
}

func isAddonRow(label string) bool { return label == rowExecution || label == rowInterface }

var addonFixed = map[string]struct {
	on  bool
	why string
}{
	addonHost: {why: "the agent can't touch your whole machine"},
	addonTUI:  {on: true, why: "there is no headless mode to pick instead — the boxes beside it ADD to this terminal"},
}

var addonHelp = map[string]string{
	addonHost:    "your own machine, with your files and your credentials — not a place proveo recommends for an agent",
	addonTUI:     "this session — this terminal, the agent's transcript and your prompts, for the whole run",
	addonBrowser: "Chromium inside the sandbox (Playwright + agent-browser) — the agent's own browser",
	addonChrome:  "Claude Code drives YOUR Chrome — your profile, your logins — over proveo's bridge",
	addonHostCDP: "the agent drives a dedicated Chrome profile on this host over CDP — log in there once; your everyday profile is never touched",
	addonAndroid: "the agent drives an Android emulator on this host through its adb server (mobile-mcp) — log in there once; the emulator stays open after the run",
	addonIOS:     "the agent drives an iOS Simulator on this host — coming soon",
	addonSandbox: "a microVM with its own Docker daemon (sbx) — the boundary every run on this harness gets",
}

func addonHelpFor(man manifest.Manifest) map[string]string {
	help := maps.Clone(addonHelp)
	if !man.WantsDocker() {
		help[addonSandbox] = "a microVM (sbx) with no Docker daemon for the agent — the boundary every run on this harness gets"
	}
	return help
}

func executionOptions() []string {
	return []string{addonHost, addonSandbox}
}

func interfaceOptions(man manifest.Manifest) []string {
	opts := []string{addonTUI}
	for target := range man.Images {
		if strings.HasSuffix(target, "-browser") {
			opts = append(opts, addonBrowser)
			break
		}
	}
	switch man.Capabilities.HostBrowser {
	case addonChrome:
		opts = append(opts, addonChrome)
	case manifest.HostBrowserCDP:
		opts = append(opts, addonHostCDP)
	}
	if man.Capabilities.HasHostDevice(manifest.HostDeviceAndroid) {
		opts = append(opts, addonAndroid)
	}
	if man.Capabilities.HasHostDevice(manifest.HostDeviceIOS) {
		opts = append(opts, addonIOS)
	}
	return opts
}

func addonOptions(man manifest.Manifest, label string) []string {
	if label == rowExecution {
		return executionOptions()
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

var addonLegacy = map[string]string{
	"tui (this session)":      addonTUI,
	"android (host emulator)": addonAndroid,
	"ios (host simulator)":    addonIOS,
	"host chrome (CDP)":       addonHostCDP,
}

func normalizeAddons(addons []string) []string {
	out := make([]string, 0, len(addons))
	for _, a := range addons {
		if a == "dind" || a == "docker (dind)" {
			continue
		}
		if n, ok := addonLegacy[a]; ok {
			a = n
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

func buildHeader(man manifest.Manifest, lookup func(string) string, roles provider.Roles, repoRoot, inputDir, homeRoot string) []string {
	if inputDir == "" {
		inputDir = repoRoot
	}
	h := gitHeader(repoRoot)
	h = append(h, choiceui.EnvHeader(credentials.LoadedSecretNames(man, os.Getenv), loadedSettings(man, lookup))...)
	h = append(h, posture.WorkspaceHeader(man, inputDir, repoRoot, homeRoot, posture.GlyphModeFrom(lookup))...)
	if line := posture.RolesLine(roles); line != "" {
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

// addonAliases are the short --addon spellings of the interface add-ons.
var addonAliases = map[string]string{
	"browser":          addonBrowser,
	"host-chrome":      addonHostCDP,
	"cdp":              addonHostCDP,
	"claude-in-chrome": addonChrome,
	"android":          addonAndroid,
	"ios":              addonIOS,
}

// resolveAddonFlags maps --addon values to the add-ons this harness offers, refusing any it does not.
func (p *Params) resolveAddonFlags(man manifest.Manifest) error {
	if !p.AddonsSet {
		return nil
	}
	offered := interfaceOptions(man)
	out := make([]string, 0, len(p.Addons))
	for _, a := range p.Addons {
		name := strings.TrimSpace(a)
		if full, ok := addonAliases[name]; ok {
			name = full
		}
		if !slices.Contains(offered, name) {
			return fmt.Errorf("--addon %q is not offered by %s (offered: %s)", a, p.Target, strings.Join(offered, ", "))
		}
		if name == addonIOS {
			return fmt.Errorf("--addon %q: %s", a, hostadb.IOSWhy)
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	p.Addons, p.AddonsAnswered = out, true
	return nil
}
