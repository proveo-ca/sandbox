// SPEC: _spec/_plans/output-readability.puml, _spec/internal/ui/output-vocabulary.puml, _spec/internal/workspace/mount-symlink-escape.puml, _spec/internal/sbx/ide-attach.puml
package sandbox

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/proveo-ca/proveo/internal/devports"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/posture"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
	"github.com/proveo-ca/proveo/internal/workspace"
)

// Item is one marked line a block prints, with its dim detail lines and the run-log lines that explain it.
type Item struct {
	Warn   bool
	Host   bool // a host fact: ● in the host colour
	Text   string
	Detail []string
	Log    []string // the run log alone: the why and the how-to-change
}

func (it Item) print() {
	switch {
	case it.Warn:
		ui.Warnf("%s", it.Text)
	case it.Host:
		ui.Hostf("%s", it.Text)
	default:
		ui.Notef("%s", it.Text)
	}
	for _, d := range it.Detail {
		ui.Detailf("%s", d)
	}
	for _, l := range it.Log {
		ui.Logf("%s", l)
	}
}

// Report is what Spec decided that its caller prints.
type Report struct {
	OutputInClone string   // the output dir, repository-relative, that teardown lifts out of the clone
	NotMounted    []string // host paths a clone cannot mount
	Unbound       []string // mounted links' targets that no bind sbx gets holds
	Unmounted     []Item   // one ⚠ item per top-level entry that holds NotMounted paths
	Notes         []Item   // what the kit, the image and the secret store said
}

// Absolute leaves a path as it is: the --print form.
func Absolute(path string) string { return path }

// ReportWorkspace prints the workspace block: lead, the facts sbx cannot know, what was
// not mounted, then the outcome. path writes each host path, in a value or inside a line.
func ReportWorkspace(in Input, r Report, path func(string) string, lead ...ui.Fact) {
	ui.Section(ui.SectionWorkspace)
	facts := slices.Clone(lead)
	if worktreeClone(in) {
		facts = append(facts, ui.Fact{Role: ui.RoleStore, Key: "checkout", Value: path(in.RepoRoot) + " (linked worktree)",
			Detail: []string{"cloned from " + path(in.CloneSource.Main) + " at " + in.CloneSource.Ref, "uncommitted edits stay on the host"}})
	}
	if in.Clone && in.CloneEnvHost != "" {
		detail := []string{"edits merge back at teardown"}
		if len(in.CloneEnvDropped) > 0 {
			detail = append(detail, "leaves out "+strings.Join(in.CloneEnvDropped, ", ")+", which sbx's proxy attaches")
		}
		facts = append(facts, ui.Fact{Role: ui.RoleStore, Key: ".env", Value: path(in.CloneEnvHost) + " (copy, rw)", Detail: detail})
	}
	if r.OutputInClone != "" {
		facts = append(facts, ui.Fact{Role: ui.RoleStore, Key: "output", Value: r.OutputInClone + "/ (in the clone)",
			Detail: []string{"lifted back at teardown"}})
	}
	items := append([]Item(nil), in.WorkspaceItems...)
	var logged []string
	key := "links"
	for _, ln := range linkLines(in.Links, r.NotMounted, r.Unbound, path) {
		switch ln.to {
		case lineScreen:
			facts = append(facts, ui.Fact{Role: ui.RoleStore, Key: key, Value: ln.text})
			key = ""
		case lineWarn:
			items = append(items, Item{Warn: true, Text: ln.text, Detail: ln.detail})
		default:
			logged = append(logged, ln.text)
		}
	}
	ui.Facts(facts...)
	for _, l := range logged {
		ui.Logf("%s", l)
	}
	items = append(items, r.Unmounted...)
	items = append(items, skippedLinkItems(in.HomeAccess, path)...)
	for _, it := range WithPaths(items, path) {
		it.print()
	}
	switch {
	case in.Clone:
		ui.Okf("private clone — your checkout is not written")
		ui.Detailf("the agent's commits come back at teardown")
	case in.CloneOff != "":
		ui.Okf("mounted checkout — the agent edits it directly")
		ui.Detailf("%s", path(in.CloneOff))
	default:
		ui.Okf("mounted checkout — the agent edits it directly")
	}
}

// skippedLinkItems names each home link whose target the linked-dirs list does not hold.
func skippedLinkItems(a HomeAccess, path func(string) string) []Item {
	var out []Item
	for _, l := range a.SkippedLinks {
		out = append(out, Item{Warn: true, Text: "home link " + path(l.Link) + " → " + path(l.Target) + " — not followed",
			Detail: []string{"list its target in " + path(filepath.Join(a.Root, manifest.LinkedDirsFile)) + " to mount it read-only"}})
	}
	return out
}

// PrintNotes prints what Spec said about the kit, the image and the secret store, each host path written by path.
func PrintNotes(r Report, path func(string) string) {
	for _, n := range WithPaths(r.Notes, path) {
		n.print()
	}
}

// WithPaths is items with each host path inside their lines written by path.
func WithPaths(items []Item, path func(string) string) []Item {
	out := make([]Item, len(items))
	for i, it := range items {
		it.Text, it.Detail = path(it.Text), slices.Clone(it.Detail)
		for j := range it.Detail {
			it.Detail[j] = path(it.Detail[j])
		}
		out[i] = it
	}
	return out
}

// PrintBlock declares one subject's heading, then prints its facts and its items; nothing, not even the heading, when neither prints.
func PrintBlock(heading func(), facts []ui.Fact, items []Item) {
	if len(items) == 0 && !slices.ContainsFunc(facts, func(f ui.Fact) bool { return f.Value != "" || len(f.Detail) > 0 }) {
		return
	}
	heading()
	ui.Facts(facts...)
	for _, it := range items {
		it.print()
	}
}

// PrintInterfacePlan prints, for a --print run, the interface facts and items the run decided before Spec.
func PrintInterfacePlan(in Input) { PrintBlock(InterfaceHeading, in.InterfaceFacts, in.InterfaceItems) }

// WorkspaceHeading declares the workspace heading, for PrintBlock.
func WorkspaceHeading() { ui.Section(ui.SectionWorkspace) }

// InterfaceHeading declares the interface heading, for PrintBlock.
func InterfaceHeading() { ui.Section(ui.SectionInterface) }

// linkLine is one link decision: a value of the links fact, a ⚠ item, or a run-log row.
type linkLine struct {
	text   string
	detail []string
	to     lineTo
}

type lineTo int

const (
	lineScreen lineTo = iota
	lineWarn
	lineLog // the run log only
)

// linkLines words each link decision in walk order. Links that share a directory
// and a target directory, each keeping its target's name, take one value or one
// ⚠ item together; each still gets its own run-log row.
func linkLines(links []workspace.Link, notMounted, unbound []string, path func(string) string) []linkLine {
	dropped := func(l workspace.Link) bool {
		for _, h := range notMounted {
			if filepath.Clean(h) == filepath.Clean(l.Target) {
				return true
			}
			if _, in := nestedRel(h, l.Target); in {
				return true
			}
		}
		return false
	}
	outside := func(l workspace.Link) bool {
		return slices.ContainsFunc(unbound, func(h string) bool { return filepath.Clean(h) == filepath.Clean(l.Target) })
	}
	groups := map[linkGroup]int{}
	for _, l := range links {
		if k, ok := linkDirs(l); ok && !dropped(l) {
			groups[linkGroup{k, outside(l)}]++
		}
	}
	said := map[linkGroup]bool{}
	var out []linkLine
	for _, l := range links {
		switch l.Action {
		case workspace.LinkMounted:
			if dropped(l) {
				out = append(out, linkLine{text: l.Rel + " → " + l.Target + " (symlink leaves the workspace; target not mounted in a clone)", to: lineLog})
				continue
			}
			dirs, ok := linkDirs(l)
			g := linkGroup{dirs, outside(l)}
			row := linkLine{text: l.Rel + " → " + l.Target + " (symlink leaves the workspace; target mounted)", to: lineLog}
			if g.unbound {
				row.text = l.Rel + " → " + l.Target + " (symlink leaves the workspace; target not mounted: sbx binds directories only)"
			}
			switch {
			case !ok || groups[g] < 2:
				out = append(out, g.line(l.Rel+" → "+path(l.Target)))
				if g.unbound {
					out = append(out, row)
				}
				continue
			case !said[g]:
				said[g] = true
				out = append(out, g.line(dirs[0]+"/ → "+path(dirs[1])+"/ ("+strconv.Itoa(groups[g])+" links)"))
			}
			out = append(out, row)
		case workspace.LinkRefused:
			target := l.Target
			if target == "" {
				target = "(unresolved)"
			} else {
				target = path(target)
			}
			out = append(out, linkLine{text: l.Rel + " → " + target + " — not in the sandbox", detail: []string{l.Reason}, to: lineWarn})
		default:
			out = append(out, linkLine{text: l.Rel + ": " + l.Reason, to: lineLog})
		}
	}
	return out
}

// linkGroup is the links that share a directory and a target directory, apart by whether sbx binds their targets.
type linkGroup struct {
	dirs    [2]string
	unbound bool
}

// line is the group's value of the links fact, or its ⚠ item when sbx binds none of its targets.
func (g linkGroup) line(text string) linkLine {
	if g.unbound {
		return linkLine{text: text + " — not in the sandbox", to: lineWarn}
	}
	return linkLine{text: text, to: lineScreen}
}

// unboundLinks lists the mounted links' targets no bind in binds holds, equal or nested.
func unboundLinks(links []workspace.Link, binds []sbx.Mount) []string {
	var out []string
	for _, l := range links {
		if l.Action != workspace.LinkMounted {
			continue
		}
		held := slices.ContainsFunc(binds, func(b sbx.Mount) bool {
			_, nested := nestedRel(b.Host, l.Target)
			return nested || filepath.Clean(b.Host) == filepath.Clean(l.Target)
		})
		if !held {
			out = append(out, l.Target)
		}
	}
	return out
}

// linkDirs is a mounted link's directory and its target's, when the link sits below the workspace root and keeps its target's name.
func linkDirs(l workspace.Link) ([2]string, bool) {
	dir := filepath.Dir(l.Rel)
	if l.Action != workspace.LinkMounted || dir == "." || filepath.Base(l.Rel) != filepath.Base(l.Target) {
		return [2]string{}, false
	}
	return [2]string{dir, filepath.Dir(l.Target)}, true
}

// unmountable is one ⚠ item per top-level entry of root that holds dropped host paths.
func unmountable(root, where string, hosts []string) []Item {
	count := map[string]int{}
	var tops []string
	for _, h := range hosts {
		rel, _ := nestedRel(root, h)
		top, _, _ := strings.Cut(rel, "/")
		if count[top] == 0 {
			tops = append(tops, top)
		}
		count[top]++
	}
	var out []Item
	for _, top := range tops {
		what := top
		if count[top] > 1 {
			what = fmt.Sprintf("%s/ — %d entries inside the %s, not mounted", top, count[top], where)
		} else {
			what += " — inside the " + where + ", not mounted"
		}
		out = append(out, Item{Warn: true, Text: what, Detail: []string{"read it from the clone"}})
	}
	return out
}

// sandboxFacts are the rows sbx's create block prints, written whole for the run log.
func sandboxFacts(in Input, cfg sbx.RunConfig) []string {
	out := []string{"sandbox: " + cfg.Name, "image: " + in.Image, "kit: " + cfg.KitDir}
	for _, m := range cfg.Mounts {
		out = append(out, "mount: "+m.Host+mountMode(m))
	}
	if cfg.CPUs > 0 {
		out = append(out, "cpus: "+strconv.Itoa(cfg.CPUs))
	}
	if cfg.Memory != "" {
		out = append(out, "memory: "+cfg.Memory)
	}
	for _, p := range cfg.Publish {
		out = append(out, "publish: "+p)
	}
	return out
}

func mountMode(m sbx.Mount) string {
	if m.ReadOnly {
		return " (ro)"
	}
	return " (rw)"
}

// envFact is the project .env's values in the sandbox environment.
func envFact(in Input, values int, created bool) ui.Fact {
	if !created {
		return ui.Fact{Role: ui.RoleStore, Key: "env", Value: "the values it was created with"}
	}
	unit := "values"
	if values == 1 {
		unit = "value"
	}
	return ui.Fact{Role: ui.RoleStore, Key: "env", Value: fmt.Sprintf("%d %s from %s (--env-file)", values, unit, ui.Tilde(in.ProjectEnv))}
}

// secretFact is one secret the secrets step stored for sandbox: the variable, sbx's entry, and where
// sbx reads the value; first keys the row, and each later one stacks under it.
func secretFact(variable, service, sandbox string, flags []string, first bool) ui.Fact {
	f := ui.Fact{Role: ui.RoleHost, Value: fmt.Sprintf("%s → sbx's %q entry, for %s only", variable, service, sandbox),
		Detail: []string{"from its value"}}
	if len(flags) > 0 {
		f.Detail = []string{"sbx resolves " + flags[0] + " on the host"}
	}
	if first {
		f.Key = "secret"
	}
	return f
}

// imageFact is proveo's image and its source, as the create run's sbx block names it.
func imageFact(image string) ui.Fact {
	return ui.Fact{Role: ui.RoleApp, Key: "image", Value: posture.Image(image)}
}

// reportCreate writes what sbx's create block prints to the run log alone, and prints one block of
// what that block does not name: the stored secrets, the env seed, then proveo's image.
func reportCreate(in Input, cfg sbx.RunConfig, envValues int, secrets []ui.Fact) {
	for _, l := range sandboxFacts(in, cfg) {
		ui.Logf("%s", l)
	}
	facts := slices.Clone(secrets)
	if envValues > 0 {
		facts = append(facts, envFact(in, envValues, true))
	}
	ui.Facts(append(facts, imageFact(in.Image))...)
}

// keptSandbox is what proveo knows of a sandbox it re-attaches to: sbx's own row, the
// receipt written when it was created, and this run's changes it keeps out.
type keptSandbox struct {
	name    string
	listed  *sbx.Listed    // nil when sbx lists no row for it
	receipt *launchReceipt // nil when proveo holds no record of its create
	kept    []string       // the stale step's changes that do not apply to it
}

// listedSandbox is sbx's row for the sandbox name; nil when sbx lists none.
func listedSandbox(name string) *sbx.Listed {
	rows, ok := listSandboxes()
	if !ok {
		return nil
	}
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

// keptPorts maps each guest port a kept sandbox publishes to its host port: sbx's row while it
// runs, else the receipt's record of its create; nil when neither names them.
func keptPorts(l *sbx.Listed, r *launchReceipt) map[int]int {
	out := map[int]int{}
	switch {
	case l != nil && l.Running():
		for _, p := range l.Ports {
			out[p.SandboxPort] = p.HostPort
		}
	case r != nil && r.Publish != nil:
		for _, spec := range r.Publish {
			host, guest, _ := strings.Cut(spec, ":")
			h, herr := strconv.Atoi(host)
			g, gerr := strconv.Atoi(guest)
			if herr == nil && gerr == nil {
				out[g] = h
			}
		}
	default:
		return nil
	}
	return out
}

// reportReattach prints, after the stored secrets, what sbx's create block would have printed for a
// sandbox this run did not create, from the sandbox's own record: sbx's row and the receipt of its create.
func reportReattach(in Input, k keptSandbox, envValues int, secrets []ui.Fact) {
	facts := slices.Clone(secrets)
	if k.receipt != nil && k.receipt.Image != "" {
		facts = append(facts, ui.Fact{Role: ui.RoleApp, Key: "image", Value: k.receipt.Image})
	}
	if k.receipt != nil && k.receipt.KitDir != "" {
		kit := ui.Fact{Role: ui.RoleStore, Key: "kit", Value: ui.Tilde(k.receipt.KitDir)}
		if !exists(k.receipt.KitDir) {
			kit.Detail = []string{"no longer on disk"}
		}
		facts = append(facts, kit)
	}
	if k.listed != nil {
		key := "mounts"
		for _, w := range k.listed.Workspaces {
			host, ro := strings.CutSuffix(w, ":ro")
			value := ui.Tilde(host)
			if ro {
				value += " (ro)"
			}
			facts = append(facts, ui.Fact{Role: ui.RoleStore, Key: key, Value: value})
			key = ""
		}
	} else {
		ui.Logf("sbx ls --json lists no %s: its mounts and ports are unknown", k.name)
	}
	if k.receipt != nil && k.receipt.CPUs > 0 {
		facts = append(facts, ui.Fact{Role: ui.RoleHost, Key: "cpus", Value: strconv.Itoa(k.receipt.CPUs)})
	}
	if k.receipt != nil && k.receipt.Memory != "" {
		facts = append(facts, ui.Fact{Role: ui.RoleHost, Key: "memory", Value: k.receipt.Memory})
	}
	if envValues > 0 {
		facts = append(facts, envFact(in, envValues, false))
	}
	ui.Facts(facts...)
	var keeps []string
	if slices.Contains(k.kept, "clone") {
		keeps = append(keeps, "mode")
	}
	if slices.Contains(k.kept, "mounts") {
		keeps = append(keeps, "mounts")
	}
	plan := []string{"the workspace block is this run's plan, not " + k.name + "'s"}
	switch {
	case slices.Contains(k.kept, noReceipt):
		Item{Warn: true, Text: "workspace — " + k.name + "'s mode and mounts are unrecorded", Log: plan}.print()
	case len(keeps) > 0:
		Item{Warn: true, Text: "workspace — " + k.name + " keeps the " + strings.Join(keeps, " and ") + " it was created with", Log: plan}.print()
	}
}

// relayRuns reports whether this run publishes and runs the browser relay: the browser is on and launch carries its -p.
func relayRuns(in Input, launch sbx.RunConfig) bool {
	return len(cdpPublish(in)) > 0 && len(launch.Publish) > 0
}

// publishing is where the sandbox's ports answer on the host.
type publishing struct {
	created bool        // this run created the sandbox: sbx's create block names each host port
	relay   bool        // this run published the browser relay and runs it
	hosts   map[int]int // a kept sandbox's guest ports to host ports (keptPorts); nil when unknown
}

// reportInterface prints what the operator reaches once the sandbox runs: what each
// port serves, the browser, the Android apps and the IDE host; the how-to goes to the run log.
func reportInterface(in Input, cfg sbx.RunConfig, pub publishing) {
	var facts []ui.Fact
	var items []Item
	var logged []string
	for _, p := range in.Ports {
		fact, at := portFact(p, pub)
		switch {
		case at == 0 && pub.hosts != nil:
			items = append(items, Item{Warn: true, Text: fmt.Sprintf("port %d — not published by %s", p.Guest, cfg.Name),
				Detail: []string{fact.Value + " · log " + DevLog(p.Guest)}})
			logged = append(logged, fmt.Sprintf("port %d: not published by %s; runs `%s` in %s — log %s", p.Guest, cfg.Name, p.Cmd, p.Dir, DevLog(p.Guest)))
			continue
		case at == 0:
			logged = append(logged, fmt.Sprintf("port %d: sbx listed no host port for %s before it ran; runs `%s` in %s — log %s",
				p.Guest, cfg.Name, p.Cmd, p.Dir, DevLog(p.Guest)))
		default:
			logged = append(logged, fmt.Sprintf("port %d: http://127.0.0.1:%d → sandbox :%d (%s) runs `%s` in %s — log %s",
				p.Guest, at, p.Guest, p.What, p.Cmd, p.Dir, DevLog(p.Guest)))
		}
		facts = append(facts, fact)
	}
	if pub.relay {
		url := fmt.Sprintf("http://127.0.0.1:%d", in.CDPHostPort)
		facts = append(facts, ui.Fact{Role: ui.RoleHost, Key: "browser", Value: "the agent's Chromium over CDP"})
		logged = append(logged,
			"browser viewport: "+url+" — attach Chrome DevTools or Playwright (connectOverCDP) to the agent's Chromium",
			url+"/json/list once the agent opens a page; nothing is exposed beyond this machine's loopback")
	}
	facts = append(facts, in.InterfaceFacts...)
	key := "android app"
	for _, a := range in.AndroidApps {
		facts = append(facts, ui.Fact{Role: ui.RoleHost, Key: key, Value: a.AppID, Detail: []string{"gradle " + a.Task() + " · log " + AndroidLog}})
		key = ""
	}
	if lines := IDEAttachLines(in, cfg, true); len(lines) > 0 {
		detail := "JetBrains, Toolbox and VS Code links in the run log"
		if in.Clone {
			detail = "IDE links in the run log; a local Open edits the host checkout"
		}
		facts = append(facts, ui.Fact{Role: ui.RoleHost, Key: "ide", Value: "ssh " + sbx.SSHHost(cfg.Name), Detail: []string{detail}})
		logged = append(logged, lines...)
	}
	PrintBlock(InterfaceHeading, facts, WithPaths(append(items, in.InterfaceItems...), ui.TildeIn))
	for _, l := range logged {
		ui.Logf("%s", l)
	}
}

// portFact is one port: what serves it, and where it answers on the host when sbx's
// create block did not say; at is that host port, 0 when no listing names one.
func portFact(p PublishedPort, pub publishing) (fact ui.Fact, at int) {
	what, _, _ := strings.Cut(p.What, " · ")
	if p.Dir != "" && p.Dir != "." {
		what += " in " + p.Dir
	}
	fact = ui.Fact{Role: ui.RoleHost, Key: fmt.Sprintf("port %d", p.Guest), Value: what, Detail: []string{"log " + DevLog(p.Guest)}}
	if pub.created {
		if first := devports.HostPorts(p.Guest); len(first) > 0 && p.Host != first[0] {
			fact.Detail = append(fact.Detail, fmt.Sprintf("host :%d was taken", first[0]))
		}
		return fact, p.Host
	}
	if at = pub.hosts[p.Guest]; at != 0 {
		fact.Detail = []string{fmt.Sprintf("http://127.0.0.1:%d · log %s", at, DevLog(p.Guest))}
	}
	return fact, at
}

// agentStartLine is the line that hands the terminal to the agent.
func agentStartLine(in Input) string {
	return strings.TrimSpace(in.Target+" "+in.AgentVersion) + " — starting"
}
