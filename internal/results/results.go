// SPEC: _spec/internal/results/result-store.puml
// Package results records what a run left behind, one file per entry, so that a
// shell prompt can read it without starting a process.
package results

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Kind says what an entry is about.
type Kind string

const (
	Kept    Kind = "kept"    // a sandbox that outlived its run
	Carried Kind = "carried" // a session's refs, home and waiting to be adopted
)

// Ref is one carried ref: the commit it points at, and how many of its commits no
// branch held when it came home.
type Ref struct {
	Name string `json:"name"`
	Lead int    `json:"lead"`
	Tip  string `json:"tip,omitempty"`
}

// Entry is one thing a run left behind.
type Entry struct {
	Kind    Kind      `json:"kind"`
	Key     string    `json:"key"` // the sandbox name, or the session id
	Repo    string    `json:"repo,omitempty"`
	Sid     string    `json:"sid,omitempty"`
	Why     string    `json:"why,omitempty"`
	Output  string    `json:"output,omitempty"` // a kept clone's host output dir: what `proveo rm` lifts into
	Refs    []Ref     `json:"refs,omitempty"`
	Pending bool      `json:"pending,omitempty"` // written ahead: the teardown that would settle it has not finished
	Exit    *int      `json:"exit,omitempty"`
	At      time.Time `json:"at"`
}

// Dir is the store's directory under proveo's state directory.
func Dir(state string) string { return filepath.Join(state, "results") }

var safeKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func path(dir string, kind Kind, key string) (string, error) {
	if dir == "" {
		return "", errors.New("results: no store directory")
	}
	if (kind != Kept && kind != Carried) || !safeKey.MatchString(key) {
		return "", fmt.Errorf("results: no entry can be named %s %q", kind, key)
	}
	return filepath.Join(dir, string(kind)+"-"+key+".json"), nil
}

// Put writes e, replacing an entry of the same kind and key. A reader sees the old file or the new one, never half of one.
func Put(dir string, e Entry) error {
	p, err := path(dir, e.Kind, e.Key)
	if err != nil {
		return err
	}
	e.Repo = Canonical(e.Repo)
	if e.At.IsZero() {
		e.At = time.Now()
	}
	body, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".entry-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Remove deletes the entry; a missing one is not an error.
func Remove(dir string, kind Kind, key string) error {
	p, err := path(dir, kind, key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// List reads every entry, oldest first. A file that cannot be read or parsed is skipped.
func List(dir string) []Entry {
	names, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []Entry
	for _, n := range names {
		body, err := os.ReadFile(n)
		if err != nil {
			continue
		}
		var e Entry
		if json.Unmarshal(body, &e) != nil || e.Key == "" {
			continue
		}
		if p, err := path(dir, e.Kind, e.Key); err != nil || p != n {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// SetExit records a run's exit status on the entries its session left.
func SetExit(dir, sid string, code int) {
	for _, e := range List(dir) {
		if e.Sid == sid && sid != "" {
			e.Exit = &code
			_ = Put(dir, e)
		}
	}
}

// Canonical is path with its symlinks resolved, so two spellings of one directory compare equal.
func Canonical(path string) string {
	if path == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// RepoOf is the nearest directory at or above dir that holds a .git, or "".
// It reads the filesystem only: a prompt calls it on every draw.
func RepoOf(dir string) string {
	dir = Canonical(dir)
	for dir != "" {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return ""
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Summary is the prompt segment for repo: how many sandboxes were kept and how many
// carried commits wait to be adopted; two refs at one commit count once. It is ""
// when there is nothing to say. An empty repo counts every entry.
func Summary(entries []Entry, repo string) string {
	repo = Canonical(repo)
	kept := 0
	waiting := map[string]bool{}
	for _, e := range entries {
		if repo != "" && e.Repo != repo {
			continue
		}
		switch e.Kind {
		case Kept:
			kept++
		case Carried:
			for _, r := range e.Refs {
				if r.Lead > 0 {
					waiting[cmpOr(r.Tip, r.Name)] = true
				}
			}
		}
	}
	adopt := len(waiting)
	var parts []string
	if kept > 0 {
		parts = append(parts, fmt.Sprintf("%d kept", kept))
	}
	if adopt > 0 {
		parts = append(parts, fmt.Sprintf("%d to adopt", adopt))
	}
	return strings.Join(parts, " · ")
}
