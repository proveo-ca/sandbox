// SPEC: _spec/internal/clean/clean-lifecycle.puml, _spec/packages/lib/config-seeding-and-persistence.puml, _spec/_paradigms/retire-dind.puml
package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/clean"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

func cleanCmd() *cobra.Command {
	var deep, force, dryRun, homes, tools bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Reclaim leaked proveo run artifacts (--deep also removes proveo/* images)",
		Long: `Reclaim what crashed or killed runs left on this host. When it is done it
prints what went, by kind, and one closing line:

  state       a run's state dir, <state root>/runs/<run id>: its sbx kit, its
              policy log and a clone's staged .env. The state root is
              $PROVEO_STATE_ROOT, else $XDG_STATE_HOME/proveo, else
              ~/.local/state/proveo
  containers, networks, volumes
              what the retired docker backend left
  images      proveo/* images (--deep)
  tools       toolchains provisioned on demand (--tools)
  home        PROVEO_HOME, ~/.proveo: sessions and seeded config (--homes)

A run whose proveo process still answers keeps its state dir, and a running
container keeps what it uses; --tools keeps every toolchain while any
sandbox runs. --force removes those too and disrupts the run that holds
them. A removal that fails is a warning; clean exits 1 only when the proveo
home stays (--homes).`,
		Args: noArgs,
		RunE: func(*cobra.Command, []string) error {
			inv, err := gatherCleanInventory(deep)
			if err != nil {
				return err
			}
			if tools {
				inv.ToolDirs = gatherToolDirs()
				inv.Sandboxes, inv.SandboxesUnknown = gatherSandboxes()
			}
			p := clean.BuildPlan(inv, clean.Options{Deep: deep, Force: force, Tools: tools})
			home := ""
			if homes {
				home = proveohome.Root(os.Getenv)
			}
			res := runClean(p, home, dryRun)
			if !dryRun {
				sandbox.ReconcileResults(results.Dir(run.StateDir()), "")
			}
			return printClean(res, p.SkippedLive, inv.SandboxesUnknown, dryRun)
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false, "also remove proveo/* images (harness + base)")
	cmd.Flags().BoolVar(&force, "force", false, "also remove resources that look live (disrupts an in-progress run)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be removed, without removing it")
	cmd.Flags().BoolVar(&homes, "homes", false, "also remove PROVEO_HOME (~/.proveo): sessions and seeded config")
	cmd.Flags().BoolVar(&tools, "tools", false,
		"also remove toolchains provisioned on demand (Go, JDK, Node, Python) — they reinstall on next run")
	return cmd
}

var toolSubdirs = []string{
	".local/share/mise",
	".local/share/proveo",
	".local/bin",
	".go",
	"go",
}

func toolRoots(root string) []string {
	roots := []string{root}
	matches, err := filepath.Glob(filepath.Join(root, "toolchains", "*"))
	if err != nil {
		return roots
	}
	sort.Strings(matches)
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() {
			roots = append(roots, m)
		}
	}
	return roots
}

func gatherSandboxes() (names []string, unknown bool) {
	names, ok := sbx.RunningNames()
	return names, !ok
}

func gatherToolDirs() []clean.ToolDir {
	root := proveohome.Root(os.Getenv)
	var out []clean.ToolDir
	for _, base := range toolRoots(root) {
		for _, sub := range toolSubdirs {
			p := filepath.Join(base, sub)
			fi, err := os.Stat(p)
			if err != nil || !fi.IsDir() {
				continue
			}
			out = append(out, clean.ToolDir{Path: p, Bytes: dirSize(p)})
		}
	}
	return out
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr
		}
		if info, e := d.Info(); e == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func gatherCleanInventory(deep bool) (clean.Inventory, error) {
	inv := gatherLegacyDocker()

	for _, sid := range stateSessions() {
		inv.StateDirs = append(inv.StateDirs, sid)
		if sessionAlive(sid) {
			inv.LiveSessions = append(inv.LiveSessions, sid)
		}
	}

	if deep {
		seen := map[string]bool{}
		for _, ref := range dockerLines("image", "ls", "--format", "{{.Repository}}:{{.Tag}}") {
			if strings.HasPrefix(ref, "proveo/") && !strings.HasSuffix(ref, ":<none>") && !seen[ref] {
				seen[ref] = true
				inv.Images = append(inv.Images, ref)
			}
		}
	}
	return inv, nil
}

// gatherLegacyDocker lists what the retired docker backend left on this host: egress
// containers and networks, proveo-dind-* containers and proveo-squid-cache-* volumes.
func gatherLegacyDocker() clean.Inventory {
	var inv clean.Inventory

	for _, line := range dockerLines("ps", "-a", "--filter", "label=proveo.egress.session",
		"--format", "{{.Names}}\t{{.State}}\t{{.Label \"proveo.egress.session\"}}") {
		if f := strings.SplitN(line, "\t", 3); len(f) == 3 {
			inv.Egress = append(inv.Egress, clean.Container{Name: f[0], Running: f[1] == "running", Session: f[2]})
		}
	}

	for _, line := range dockerLines("ps", "-a", "--filter", "name=proveo-dind-",
		"--format", "{{.Names}}\t{{.State}}") {
		if f := strings.SplitN(line, "\t", 2); len(f) == 2 {
			inv.LegacyDind = append(inv.LegacyDind, clean.Container{Name: f[0], Running: f[1] == "running"})
		}
	}

	for _, name := range dockerLines("network", "ls", "--filter", "label=proveo.egress.session", "--format", "{{.Name}}") {
		n := clean.Net{Name: name}
		if insp := dockerLines("network", "inspect", name,
			"--format", "{{index .Labels \"proveo.egress.session\"}}\t{{len .Containers}}"); len(insp) == 1 {
			if f := strings.SplitN(insp[0], "\t", 2); len(f) == 2 {
				n.Session, n.HasEndpoints = f[0], f[1] != "0"
			}
		}
		inv.Networks = append(inv.Networks, n)
	}

	inv.Volumes = dockerLines("volume", "ls", "--filter", "name=proveo-squid-cache-", "--format", "{{.Name}}")
	return inv
}

// sessionAlive reports whether the proveo process a session id names (proveo-<unix>-<pid>) still runs.
func sessionAlive(sid string) bool {
	i := strings.LastIndexByte(sid, '-')
	if !strings.HasPrefix(sid, "proveo-") || i < 0 {
		return false
	}
	pid, err := strconv.Atoi(sid[i+1:])
	if err != nil || pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	return err == nil && proc.Signal(syscall.Signal(0)) == nil
}

// stateSessions lists the session ids under every run root, sorted and deduplicated.
func stateSessions() []string {
	var out []string
	for _, root := range run.RunRoots() {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, e.Name())
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// cleanTarget is one removal: its kind, its name as the screen writes it, a toolchain's size, and how it goes.
type cleanTarget struct {
	kind, name string
	bytes      int64
	remove     func() (string, error)
}

// cleanResult is what runClean removed, or under a dry run would remove, and each removal that failed.
type cleanResult struct {
	gone       []cleanTarget
	failed     []sandbox.Item
	homeFailed bool
	reclaimed  int64
}

// cleanTargets is the plan's removals in the block's order, and home when it exists.
func cleanTargets(p clean.Plan, home string) []cleanTarget {
	docker := func(args ...string) func() (string, error) {
		return func() (string, error) {
			out, err := exec.Command("docker", args...).CombinedOutput()
			return string(out), err
		}
	}
	removeAll := func(dir string) func() (string, error) {
		return func() (string, error) { return "", os.RemoveAll(dir) }
	}
	var ts []cleanTarget
	for _, c := range p.Containers {
		ts = append(ts, cleanTarget{kind: "container", name: c, remove: docker("rm", "-f", c)})
	}
	for _, n := range p.Networks {
		ts = append(ts, cleanTarget{kind: "network", name: n, remove: docker("network", "rm", n)})
	}
	for _, v := range p.Volumes {
		ts = append(ts, cleanTarget{kind: "volume", name: v, remove: docker("volume", "rm", v)})
	}
	for _, sid := range p.StateDirs {
		for _, root := range run.RunRoots() {
			if dir := filepath.Join(root, sid); isDir(dir) {
				ts = append(ts, cleanTarget{kind: "state", name: sid, remove: removeAll(dir)})
			}
		}
	}
	for _, img := range p.Images {
		ts = append(ts, cleanTarget{kind: "image", name: img, remove: docker("image", "rm", img)})
	}
	for _, dir := range p.ToolDirs {
		ts = append(ts, cleanTarget{kind: "tools", name: ui.Tilde(dir), bytes: dirSize(dir), remove: removeAll(dir)})
	}
	if home != "" && isDir(home) {
		ts = append(ts, cleanTarget{kind: "home", name: ui.Tilde(home), remove: removeAll(home)})
	}
	return ts
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// runClean removes each of the plan's targets behind a live line; a dry run removes nothing.
func runClean(p clean.Plan, home string, dryRun bool) cleanResult {
	var res cleanResult
	for _, t := range cleanTargets(p, home) {
		if !dryRun {
			done := ui.Running(func(ran time.Duration) string {
				return "clean · " + t.kind + " " + t.name + " · " + ran.Round(time.Second).String()
			}, "")
			out, err := t.remove()
			done()
			if err != nil {
				said := cmp.Or(strings.TrimSpace(out), err.Error())
				res.failed = append(res.failed, sandbox.Item{Warn: true, Text: t.kind + " " + t.name + " — not removed",
					Detail: errLines(errors.New(said))})
				res.homeFailed = res.homeFailed || t.kind == "home"
				continue
			}
		}
		res.gone = append(res.gone, t)
		res.reclaimed += t.bytes
	}
	return res
}

// cleanKeys is each kind's key in the block, in the block's order.
var cleanKeys = []struct{ kind, key string }{
	{"container", "containers"}, {"network", "networks"}, {"volume", "volumes"},
	{"state", "state"}, {"image", "images"}, {"tools", "tools"}, {"home", "home"},
}

// printClean prints what went as one block by kind, each failed removal and what was left live as ⚠,
// then one closing line; a home that stays comes back Reported.
func printClean(res cleanResult, left []string, sbxUnknown, dryRun bool) error {
	var facts []ui.Fact
	for _, k := range cleanKeys {
		key := k.key
		for _, t := range res.gone {
			if t.kind != k.kind {
				continue
			}
			value := t.name
			if t.kind == "tools" {
				value += " (" + humanBytes(t.bytes) + ")"
			}
			facts = append(facts, ui.Fact{Role: ui.RoleError, Key: key, Value: value})
			key = ""
		}
	}
	items := slices.Clone(res.failed)
	if len(left) > 0 {
		items = append(items, leftLive(left, sbxUnknown))
	}
	sandbox.PrintBlock(func() {}, facts, items)
	return cleanClosing(res, len(left), dryRun)
}

// leftLive is the one ⚠ for what clean held back as live: each one dim, then the --force fix.
func leftLive(left []string, sbxUnknown bool) sandbox.Item {
	it := sandbox.Item{Warn: true, Text: fmt.Sprintf("left: %d that look live", len(left))}
	fix := "`--force` removes them too and disrupts the runs that hold them"
	if len(left) == 1 {
		it.Text, fix = "left: 1 that looks live", "`--force` removes it too and disrupts the run that holds it"
	}
	tools := false
	for _, l := range left {
		if dir, ok := strings.CutPrefix(l, "tools "); ok {
			l, tools = "tools "+ui.Tilde(dir), true
		}
		it.Detail = append(it.Detail, l)
	}
	if tools && sbxUnknown {
		it.Detail = append(it.Detail, "sbx did not say whether a sandbox runs")
	}
	it.Detail = append(it.Detail, fix)
	return it
}

// cleanClosing is clean's one closing line: a failed removal wins over what went; a home that stays is ×
// and exits 1.
func cleanClosing(res cleanResult, left int, dryRun bool) error {
	reclaimed := func(what string) {
		if res.reclaimed > 0 {
			ui.Detailf("%s of toolchains %s", humanBytes(res.reclaimed), what)
		}
	}
	switch {
	case res.homeFailed:
		ui.Failf("clean — %d not removed", len(res.failed))
		reclaimed("reclaimed")
		return backend.Reported{Err: fmt.Errorf("clean: %d not removed", len(res.failed))}
	case len(res.failed) > 0:
		ui.Warnf("clean — %d not removed", len(res.failed))
		reclaimed("reclaimed")
	case len(res.gone) == 0 && left == 0:
		ui.Okf("clean — nothing to remove")
	case dryRun:
		ui.Storef("clean — nothing removed (dry run)")
		reclaimed("to reclaim")
	case len(res.gone) == 0:
		ui.Warnf("clean — nothing removed")
	default:
		ui.Okf("clean — %d removed", len(res.gone))
		reclaimed("reclaimed")
	}
	return nil
}

func dockerLines(args ...string) []string {
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimRight(l, "\r"); strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
