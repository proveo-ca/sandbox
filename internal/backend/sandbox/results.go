// SPEC: _spec/internal/results/result-store.puml
package sandbox

import (
	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/ui"
)

// recordKept settles the store's entry for sandbox name: why keeps it, with pending
// set while teardown still runs; an empty why forgets it.
func recordKept(in Input, name, why string, pending bool) {
	if in.ResultsDir == "" || name == "" {
		return
	}
	var err error
	if why == "" {
		err = results.Remove(in.ResultsDir, results.Kept, name)
	} else {
		err = results.Put(in.ResultsDir, results.Entry{
			Kind: results.Kept, Key: name, Sid: in.Sid, Repo: in.RepoRoot, Why: why, Pending: pending,
		})
	}
	if err != nil {
		ui.Logf("results: %v", err)
	}
}

// recordCarried records the session's carried refs that hold commits HEAD lacks; with none, it forgets the session.
func recordCarried(in Input, names []string, leads map[string]int, tips map[string]string) {
	if in.ResultsDir == "" || in.Sid == "" {
		return
	}
	var refs []results.Ref
	for _, n := range names {
		if leads[n] > 0 {
			refs = append(refs, results.Ref{Name: n, Lead: leads[n], Tip: tips[n]})
		}
	}
	var err error
	if len(refs) == 0 {
		err = results.Remove(in.ResultsDir, results.Carried, in.Sid)
	} else {
		err = results.Put(in.ResultsDir, results.Entry{
			Kind: results.Carried, Key: in.Sid, Sid: in.Sid, Repo: in.RepoRoot, Refs: refs,
		})
	}
	if err != nil {
		ui.Logf("results: %v", err)
	}
}
