// SPEC: _spec/internal/results/result-store.puml
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/adopt"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/sbx"
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

// RemoveKept carries what the kept sandbox name still holds home, removes it, and clears its
// entry. A running sandbox is refused unless stop asks to stop it first; nothing is cleared on
// a listing it cannot trust.
func RemoveKept(name, resultsDir string, report func(string, ...any), stop bool) error {
	rows, err := trustedListing()
	if err != nil {
		return err
	}
	var row *sbx.Listed
	for i := range rows {
		if rows[i].Name == name {
			row = &rows[i]
		}
	}
	if row == nil {
		if err := results.Remove(resultsDir, results.Kept, name); err != nil {
			return err
		}
		report("%s is already gone; its entry is cleared", name)
		return nil
	}
	if row.Running() && !stop {
		return fmt.Errorf("%s is running; `proveo rm --stop %s` stops it, carries its work home and removes it", name, name)
	}
	if row.Running() {
		report("sbx stop %s", name)
		if out, err := guestRun(sbx.StopArgs(name)...); err != nil {
			return fmt.Errorf("stop %s: %w: %s — it is kept", name, err, strings.TrimSpace(out))
		}
	}
	in, cfg := keptInput(*row, resultsDir)
	report("carrying its work home from %s", name)
	carried := NewBlock(ResultsHeading)
	err = PreserveClone(in, cfg, carried)
	carried.Print()
	if err != nil {
		return fmt.Errorf("%w — %s kept; `sbx rm --force %s` deletes it and whatever is only in it", err, name, name)
	}
	if opencodeTarget(in.Target) {
		if err := carryOpenCodeHistory(in.HomeRoot, name); err != nil {
			return fmt.Errorf("OpenCode history could not be carried out of %s (%v) — it is kept", name, err)
		}
	}
	report("sbx rm --force %s", name)
	if out, err := guestRun(sbx.RemoveArgs(name)...); err != nil && !sbx.NotFound(out) {
		return fmt.Errorf("remove %s: %w: %s — check `sbx ls`, then run `proveo rm %s` again", name, err, strings.TrimSpace(out), name)
	}
	_ = os.Remove(receiptPath(name))
	_ = os.Remove(legacyOwnerRecord(name))
	return results.Remove(resultsDir, results.Kept, name)
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
