// SPEC: _spec/internal/results/result-store.puml
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/adopt"
	"github.com/proveo-ca/proveo/internal/backend"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

// listSandboxes is sbx.List; tests replace it.
var listSandboxes = sbx.List

// errNoListing is what a verb returns when sbx cannot say which sandboxes exist.
var errNoListing = errors.New("sbx gave no listing of its sandboxes; nothing was changed — run again once `sbx ls` answers")

// trustedListing is sbx's listing when it can prove a sandbox gone. An empty listing
// cannot: a busy daemon prints one too.
func trustedListing() ([]sbx.Listed, error) {
	rows, ok := listSandboxes()
	if !ok || len(rows) == 0 {
		return nil, errNoListing
	}
	return rows, nil
}

// RemoveKept carries home what sandbox name holds, removes it and clears its entry; a running one needs stop,
// and an untrusted listing changes nothing. One closing line; a refusal is "× <name> — not removed", Reported.
// SPEC: _spec/internal/results/result-store.puml (THE VERBS)
func RemoveKept(name, resultsDir string, report func(string, ...any), stop bool) error {
	rows, err := trustedListing()
	if err != nil {
		return notRemoved(name, err, "sbx gave no listing of its sandboxes", "`proveo rm "+name+"` tries again once `sbx ls` answers")
	}
	i := slices.IndexFunc(rows, func(r sbx.Listed) bool { return r.Name == name })
	if i < 0 {
		return forgetKept(name, resultsDir)
	}
	row := rows[i]
	if row.Running() && !stop {
		return notRemoved(name, fmt.Errorf("%s is running; `proveo rm --stop %s` stops it, carries its work home and removes it", name, name),
			"it is running", keptFix(name, true))
	}
	if row.Running() {
		report("sbx stop %s", name)
		if out, err := guestRun(sbx.StopArgs(name)...); err != nil {
			return notRemoved(name, fmt.Errorf("stop %s: %w: %s — it is kept", name, err, strings.TrimSpace(out)),
				"sbx stop: "+failure(err, out), "`proveo rm --stop "+name+"` tries again")
		}
	}
	in, cfg := keptInput(row, resultsDir)
	carried := NewBlock(func() {})
	if in.RepoRoot != "" && sbx.Gone(name) {
		return forgetKept(name, resultsDir)
	}
	if err := carryKept(in, cfg, report, carried); err != nil {
		carried.Print()
		return notRemoved(name, fmt.Errorf("%w — %s kept", err, name), "`proveo rm "+name+"` tries again")
	}
	carried.Print()
	report("sbx rm --force %s", name)
	if out, err := guestRun(sbx.RemoveArgs(name)...); err != nil && !sbx.NotFound(out) {
		return notRemoved(name, fmt.Errorf("remove %s: %w: %s", name, err, strings.TrimSpace(out)),
			"sbx rm: "+failure(err, out), "check `sbx ls`, then `proveo rm "+name+"` again")
	}
	_ = os.Remove(receiptPath(name))
	_ = os.Remove(legacyOwnerRecord(name))
	return settleKept(name, resultsDir, "removed")
}

// carryKept brings home what the kept sandbox holds into out: a clone's work, behind the clone-mode
// probe's live step, and an OpenCode engine's history. It returns an error while work stays only
// in the sandbox, with an item in out that says which.
func carryKept(in Input, cfg sbx.RunConfig, report func(string, ...any), out *Block) error {
	if in.RepoRoot != "" {
		var clones bool
		if err := shown(stepCloneMode, func() (err error) {
			clones, err = sandboxClone(in, cfg.Name)
			return err
		}); err != nil {
			out.Item(Item{Warn: true, Text: "clone mode — unknown", Detail: []string{failure(err, "")}})
			return fmt.Errorf("cannot tell whether it holds a clone, so its work may be only there: %w", err)
		}
		if clones {
			report("carrying its work home from %s", cfg.Name)
			in.Clone, in.CloneKnown = true, true
			if err := PreserveClone(in, cfg, out); err != nil {
				if !slices.ContainsFunc(out.items, func(it Item) bool { return it.Warn }) {
					out.Item(Item{Warn: true, Text: "work — not carried home", Detail: []string{failure(err, "")}})
				}
				return err
			}
		}
	}
	if !opencodeTarget(in.Target) {
		return nil
	}
	dest, size, err := exportOpenCodeHistory(in.HomeRoot, cfg.Name)
	switch {
	case err != nil:
		out.Item(Item{Warn: true, Text: "history — not carried", Detail: []string{failure(err, "")}})
		return fmt.Errorf("OpenCode history could not be carried out of %s: %w", cfg.Name, err)
	case dest != "":
		out.Fact(ui.Fact{Role: ui.RoleApp, Key: "history", Value: "carried history (" + byteSize(size) + ") to " + ui.Tilde(dest)})
	}
	return nil
}

// forgetKept closes `proveo rm` of a sandbox sbx does not list: its entry cleared when a run kept
// it, a ⚠ that exits 0 when none did.
func forgetKept(name, resultsDir string) error {
	if !slices.ContainsFunc(results.List(resultsDir), func(e results.Entry) bool { return e.Kind == results.Kept && e.Key == name }) {
		Item{Warn: true, Text: name + " — no such sandbox", Detail: []string{"sbx lists none, and no run kept one by that name"}}.print()
		return nil
	}
	return settleKept(name, resultsDir, "already gone")
}

// settleKept clears name's kept entry and closes on "✓ <name> — <done>"; an entry it cannot clear
// closes on a ⚠ that says so.
func settleKept(name, resultsDir, done string) error {
	if err := results.Remove(resultsDir, results.Kept, name); err != nil {
		return refused(Item{Warn: true, Text: name + " — " + done + ", its entry stays", Detail: []string{failure(err, "")}}, err)
	}
	ui.Okf("%s — %s", name, done)
	return nil
}

// notRemoved closes `proveo rm` on "× <name> — not removed" with detail dim under it, and returns err Reported.
func notRemoved(name string, err error, detail ...string) error {
	return refused(Item{Fail: true, Text: name + " — not removed", Detail: detail}, err)
}

// refused prints it and returns err as one main prints nothing for.
func refused(it Item, err error) error {
	it.print()
	return backend.Reported{Err: err}
}

// keptInput rebuilds what the carry needs from the sandbox's listing and its store entry.
func keptInput(row sbx.Listed, resultsDir string) (Input, sbx.RunConfig) {
	in := Input{
		Target:     strings.TrimPrefix(row.Agent, sbx.NamePrefix),
		Sid:        fmt.Sprintf("proveo-%d-%d-rm", time.Now().Unix(), os.Getpid()),
		HomeRoot:   proveohome.Root(os.Getenv),
		ResultsDir: resultsDir,
	}
	cfg := sbx.RunConfig{Name: row.Name}
	if len(row.Workspaces) > 0 {
		cfg.Mounts = []sbx.Mount{{Host: row.Workspaces[0]}}
		in.RepoRoot = gitTop(row.Workspaces[0])
	}
	if r := readReceipt(row.Name); r != nil {
		in.OutputDir = r.OutputDir
	}
	for _, e := range results.List(resultsDir) {
		if e.Kind != results.Kept || e.Key != row.Name {
			continue
		}
		if e.Repo != "" {
			in.RepoRoot = e.Repo
		}
		if e.Output != "" {
			in.OutputDir = e.Output
		}
	}
	return in, cfg
}

// gitTop is the repository dir sits in, or "" outside one.
func gitTop(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ReconcileResults drops entries that no longer describe anything: a kept sandbox sbx
// no longer lists, a carried session whose repository is gone, and, for repo, carried
// refs that were deleted or that a branch now holds.
func ReconcileResults(resultsDir, repo string) {
	entries := results.List(resultsDir)
	if len(entries) == 0 {
		return
	}
	rows, listErr := trustedListing()
	listed := map[string]bool{}
	for _, r := range rows {
		listed[r.Name] = true
	}
	repo = results.Canonical(repo)
	for _, e := range entries {
		switch e.Kind {
		case results.Kept:
			if listErr == nil && !listed[e.Key] {
				_ = results.Remove(resultsDir, results.Kept, e.Key)
			}
		case results.Carried:
			if _, err := os.Stat(e.Repo); e.Repo != "" && errors.Is(err, os.ErrNotExist) {
				_ = results.Remove(resultsDir, results.Carried, e.Key)
				continue
			}
			if repo == "" || e.Repo != repo {
				continue
			}
			var ahead []results.Ref
			for _, r := range e.Refs {
				if n := adopt.Lead(repo, r.Name); n > 0 {
					ahead = append(ahead, results.Ref{Name: r.Name, Lead: n, Tip: r.Tip})
				}
			}
			if len(ahead) == 0 {
				_ = results.Remove(resultsDir, results.Carried, e.Key)
			} else if len(ahead) != len(e.Refs) {
				e.Refs = ahead
				_ = results.Put(resultsDir, e)
			}
		}
	}
}
