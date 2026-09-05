// SPEC: _spec/internal/clean/clean-lifecycle.puml
//
// SPEC: _spec/internal/clean/clean-lifecycle.puml
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/clean"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

func cleanCmd() *cobra.Command {
	var deep, force, dryRun, homes, tools bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Reclaim leaked proveo run artifacts (--deep also removes proveo/* images)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			inv, err := gatherCleanInventory(deep)
			if err != nil {
				return err
			}
			if tools {
				inv.ToolDirs = gatherToolDirs()
				inv.Sandboxes, inv.SandboxesUnknown = gatherSandboxes()
			}
			if err := runClean(clean.BuildPlan(inv, clean.Options{Deep: deep, Force: force, Tools: tools}), dryRun); err != nil {
				return err
			}
			if homes {
				return cleanProveoHomes(dryRun)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false, "also remove proveo/* images (harness + base + sidecars)")
	cmd.Flags().BoolVar(&force, "force", false, "also remove resources that look live (disrupts an in-progress run)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be removed, without removing it")
	cmd.Flags().BoolVar(&homes, "homes", false, "also remove PROVEO_HOME (~/.proveo) durable session/config cache")
	cmd.Flags().BoolVar(&tools, "tools", false,
		"also remove toolchains provisioned on demand (language servers, Go, JDK) — they reinstall on next run")
	return cmd
}

// SPEC: _spec/internal/clean/clean-lifecycle.puml
var toolSubdirs = []string{
	".local/share/mise",
	".local/share/proveo",
	".local/bin",
	".go",
	"go",
}

// SPEC: _spec/_plans/config-seeding-and-persistence.puml
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

func cleanProveoHomes(dryRun bool) error {
	root := proveohome.Root(os.Getenv)
	verb := "removing"
	if dryRun {
		verb = "would remove"
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		ui.Okf("no proveo home at %s", root)
		return nil
	}
	ui.Dangerf("%s proveo home %s (sessions + seeded config)", verb, root)
	if dryRun {
		return nil
	}
	return os.RemoveAll(root)
}

func gatherCleanInventory(deep bool) (clean.Inventory, error) {
	var inv clean.Inventory

	for _, line := range dockerLines("ps", "-a", "--filter", "label=proveo.egress.session",
		"--format", "{{.Names}}\t{{.State}}\t{{.Label \"proveo.egress.session\"}}") {
		if f := strings.SplitN(line, "\t", 3); len(f) == 3 {
			inv.Egress = append(inv.Egress, clean.Container{Name: f[0], Running: f[1] == "running", Session: f[2]})
		}
	}

	// that made it. SPEC: _spec/_plans/retire-dind.puml
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

	if entries, err := os.ReadDir(filepath.Join(run.StateDir(), "egress")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				inv.StateDirs = append(inv.StateDirs, e.Name())
			}
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

func runClean(p clean.Plan, dryRun bool) error {
	if len(p.Containers)+len(p.Networks)+len(p.StateDirs)+len(p.Images)+len(p.ToolDirs) == 0 {
		if len(p.SkippedLive) == 0 {
			ui.Okf("nothing to clean")
			return nil
		}
	}
	verb := "removing"
	if dryRun {
		verb = "would remove"
	}
	for _, c := range p.Containers {
		ui.Dangerf("%s container %s", verb, c)
		if !dryRun {
			_ = exec.Command("docker", "rm", "-f", c).Run()
		}
	}
	for _, n := range p.Networks {
		ui.Dangerf("%s network %s", verb, n)
		if !dryRun {
			_ = exec.Command("docker", "network", "rm", n).Run()
		}
	}
	for _, sid := range p.StateDirs {
		dir := filepath.Join(run.StateDir(), "egress", sid)
		ui.Dangerf("%s state %s (incl. any injected broker secret)", verb, dir)
		if !dryRun {
			_ = os.RemoveAll(dir)
		}
	}
	for _, img := range p.Images {
		ui.Dangerf("%s image %s", verb, img)
		if !dryRun {
			_ = exec.Command("docker", "image", "rm", img).Run()
		}
	}
	var reclaimed int64
	for _, dir := range p.ToolDirs {
		size := dirSize(dir)
		reclaimed += size
		ui.Dangerf("%s toolchain %s (%s)", verb, dir, humanBytes(size))
		if !dryRun {
			_ = os.RemoveAll(dir)
		}
	}
	if len(p.ToolDirs) > 0 {
		ui.Okf("%s %s of provisioned toolchains — they reinstall on the next run that needs them",
			map[bool]string{true: "would reclaim", false: "reclaimed"}[dryRun], humanBytes(reclaimed))
	}

	if len(p.SkippedLive) > 0 {
		ui.Warnf("left %d resource(s) that look live (in-progress run?): %s",
			len(p.SkippedLive), strings.Join(p.SkippedLive, ", "))
		ui.Notef("re-run with --force to remove those too (disrupts an in-progress run)")
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
