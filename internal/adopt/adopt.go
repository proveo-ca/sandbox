// SPEC: _spec/internal/results/result-store.puml
// Package adopt turns a ref a run carried home into a branch.
package adopt

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// RefRoot is where carried refs live in the host repository.
const RefRoot = "refs/proveo/"

// Ref is one carried ref.
type Ref struct {
	Name    string // short: proveo/<session>/<branch>
	Session string
	Branch  string
	Lead    int // commits on it that no local branch holds
}

func git(repo string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Lead counts the commits on the short ref name that no local branch holds; 0 when the ref is gone.
func Lead(repo, name string) int {
	out, err := git(repo, "rev-list", "--count", "refs/"+name, "--not", "--branches")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(out)
	return n
}

// Carried lists every ref under refs/proveo/ in repo with its lead, in ref order.
func Carried(repo string) ([]Ref, error) {
	out, err := git(repo, "for-each-ref", "--format=%(refname:short)", RefRoot)
	if err != nil {
		return nil, NotListed{Out: out}
	}
	var refs []Ref
	for _, name := range strings.Fields(out) {
		session, branch, ok := strings.Cut(strings.TrimPrefix(name, "proveo/"), "/")
		if !ok || !strings.HasPrefix(name, "proveo/") {
			continue
		}
		refs = append(refs, Ref{Name: name, Session: session, Branch: branch, Lead: Lead(repo, name)})
	}
	return refs, nil
}

// Waiting keeps the refs that hold work no branch has.
func Waiting(refs []Ref) []Ref {
	var out []Ref
	for _, r := range refs {
		if r.Lead > 0 {
			out = append(out, r)
		}
	}
	return out
}

// Pick resolves what the operator named: a ref, a session, or a branch one session carried.
// A name that fits several refs is an error that lists them.
func Pick(refs []Ref, arg string) (Ref, error) {
	arg = strings.TrimPrefix(arg, "refs/")
	for _, r := range refs {
		if r.Name == arg {
			return r, nil
		}
	}
	var fits []Ref
	for _, r := range Waiting(refs) {
		if r.Session == arg || r.Branch == arg {
			fits = append(fits, r)
		}
	}
	if len(fits) == 0 {
		for _, r := range refs {
			if r.Session == arg || r.Branch == arg {
				fits = append(fits, r)
			}
		}
	}
	switch len(fits) {
	case 0:
		return Ref{}, NoMatch{Arg: arg}
	case 1:
		return fits[0], nil
	}
	names := make([]string, len(fits))
	for i, r := range fits {
		names[i] = r.Name
	}
	return Ref{}, Ambiguous{Arg: arg, Refs: names}
}

// NotListed is a repository whose carried refs git could not list, with git's own words.
type NotListed struct{ Out string }

func (e NotListed) Error() string { return "list carried refs: " + e.Out }

// NoMatch is a name no carried ref answers to.
type NoMatch struct{ Arg string }

func (e NoMatch) Error() string { return fmt.Sprintf("no carried ref matches %q", e.Arg) }

// Ambiguous is a name that fits several carried refs.
type Ambiguous struct {
	Arg  string
	Refs []string
}

func (e Ambiguous) Error() string {
	return fmt.Sprintf("%q fits %d carried refs; name one: %s", e.Arg, len(e.Refs), strings.Join(e.Refs, ", "))
}

// BadName is a branch name git refuses, with git's own words.
type BadName struct{ Name, Out string }

func (e BadName) Error() string { return fmt.Sprintf("%q is not a branch name: %s", e.Name, e.Out) }

// Exists is a branch name already taken.
type Exists struct{ Name string }

func (e Exists) Error() string {
	return fmt.Sprintf("a branch named %s exists; pick another name with --as", e.Name)
}

// NotCreated is a branch git did not create, with git's own words.
type NotCreated struct{ Name, Out string }

func (e NotCreated) Error() string { return fmt.Sprintf("create branch %s: %s", e.Name, e.Out) }

// Branch creates a branch at ref and returns its name. It moves nothing else: HEAD,
// the index and the files stay as they are. as defaults to the ref's own branch name.
func Branch(repo string, ref Ref, as string) (string, error) {
	if as == "" {
		as = ref.Branch
	}
	if out, err := git(repo, "check-ref-format", "--branch", as); err != nil {
		return "", BadName{Name: as, Out: out}
	}
	if _, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+as); err == nil {
		return "", Exists{Name: as}
	}
	if out, err := git(repo, "branch", "--", as, "refs/"+ref.Name); err != nil {
		return "", NotCreated{Name: as, Out: out}
	}
	return as, nil
}
