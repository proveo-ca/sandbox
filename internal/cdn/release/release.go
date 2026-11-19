// SPEC: _spec/internal/cdn/distribution-update.puml
// Package release prepares and publishes CLI releases.
package release

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/proveo-ca/proveo/internal/cdn"
	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	LatestJSON = "apps/cli/public/cli/latest.json"
	binDir     = "apps/cli/public/cli/bin"
	Branch     = "main"
	Remote     = "origin"
	ledgerRun  = "-run=^TestNoRetirementOutlivesOneRelease$"
	question   = "Prepare a CLI release PR, or publish and tag the release if its PR has merged?"
)

var semverTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Repo is a git checkout; a nil Env inherits the process environment.
type Repo struct {
	Root string
	Env  []string
}

// with is r with extra appended to its environment.
func (r Repo) with(extra ...string) Repo {
	base := r.Env
	if base == nil {
		base = os.Environ()
	}
	return Repo{Root: r.Root, Env: append(slices.Clone(base), extra...)}
}

func (r Repo) git(args ...string) (string, error) {
	out, err := r.raw(args...)
	return strings.TrimSpace(out), err
}

// raw is git's stdout untrimmed, for output whose leading spaces carry meaning.
func (r Repo) raw(args ...string) (string, error) {
	return r.output("git", args...)
}

func (r Repo) output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = r.Root
	cmd.Env = r.Env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// Steps configures the release commands.
type Steps struct {
	Ledger  func() error
	Confirm func(question string) bool
	Build   func(tag string) error
	Stage   func() error
	Publish func() error
	GitHub  func(args ...string) (string, error)
}

// DefaultSteps runs the release commands from root.
func DefaultSteps(root string, yes bool, pr *ui.Printer) Steps {
	return Steps{
		Ledger: func() error {
			return command(root, []string{"PROVEO_RELEASE_GATE=1"}, "go", "test", "-count=1", ledgerRun, "./internal/contract/")
		},
		Confirm: func(q string) bool { return yes || askYes(q, os.Stdin, pr) },
		Build: func(tag string) error {
			return command(root, []string{"GORELEASER_CURRENT_TAG=" + tag}, "goreleaser", "release", "--clean", "--skip=publish,validate")
		},
		Stage:   func() error { return command(root, nil, "go", "run", "./internal/cdn/stage") },
		Publish: func() error { return command(root, nil, "wrangler", "deploy", "--cwd", "apps/cli") },
		GitHub: func(args ...string) (string, error) {
			return (Repo{Root: root}).output("gh", args...)
		},
	}
}

func command(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// askYes asks q on a terminal stdin; anything else answers no.
func askYes(q string, in *os.File, pr *ui.Printer) bool {
	if !term.IsTerminal(int(in.Fd())) {
		pr.Warnf("stdin is no terminal, so nobody can answer %q", q)
		return false
	}
	pr.Askf("%s [y/N]", q)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// Plan returns the CLI release steps for --print.
func Plan(version string) []string {
	tag := "<" + version + ">"
	if semverTag.MatchString(version) {
		tag = version
	}
	branch := releaseBranch + tag
	return []string{
		"PROVEO_RELEASE_GATE=1 go test -count=1 " + ledgerRun + " ./internal/contract/",
		"ask: " + question,
		"git fetch --quiet --tags " + Remote + " " + Branch,
		"GORELEASER_CURRENT_TAG=" + tag + " goreleaser release --clean --skip=publish,validate",
		"go run ./internal/cdn/stage",
		"git switch -c " + branch,
		"git commit -m 'release: " + tag + "' -- " + LatestJSON,
		"git push " + Remote + " HEAD:refs/heads/" + branch,
		"gh pr create --base " + Branch + " --head " + branch + " (reuse an existing release PR)",
		"wait for the PR to merge, then rerun the deploy",
		"verify the merged commit on " + Remote + "/" + Branch + " and the staged assets",
		"wrangler deploy --cwd apps/cli",
		"git push " + Remote + " <merged-commit>:refs/tags/" + tag,
	}
}

// Deploy returns true once the CLI release is published and tagged.
func Deploy(r Repo, version string, st Steps, pr *ui.Printer) (bool, error) {
	if err := st.Ledger(); err != nil {
		return false, fmt.Errorf("retirement ledger: %w", err)
	}
	if !st.Confirm(question) {
		return false, errors.New("release not confirmed (--yes answers the question)")
	}
	tag, err := cut(r, version, st, pr)
	if err != nil {
		return false, err
	}
	return submit(r, tag, st, pr)
}

func cut(r Repo, version string, st Steps, pr *ui.Printer) (string, error) {
	branch, err := r.git("symbolic-ref", "--quiet", "--short", "HEAD")
	pending, isRelease := strings.CutPrefix(branch, releaseBranch)
	if err != nil || (branch != Branch && (!isRelease || !semverTag.MatchString(pending))) {
		return "", fmt.Errorf("start a release on %s or resume on %s<tag>; HEAD is on %q", Branch, releaseBranch, branch)
	}
	if _, err := r.git("fetch", "--quiet", "--no-tags", Remote, Branch); err != nil {
		return "", err
	}
	head, err := r.git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	upstream, err := r.git("rev-parse", "refs/remotes/"+Remote+"/"+Branch)
	if err != nil {
		return "", err
	}
	if isRelease {
		m, err := r.manifestAt(head)
		if err != nil {
			return "", err
		}
		if head != upstream || "v"+m.Version == pending {
			return pending, r.checkCut(pending, version)
		}
	}
	if branch == Branch && head != upstream {
		tag, err := r.headTag()
		if err != nil {
			return "", err
		}
		if tag == "" {
			return "", fmt.Errorf("HEAD %.12s is not %s/%s %.12s; sync %s through a pull request or fast-forward it",
				head, Remote, Branch, upstream, Branch)
		}
		if err := r.checkCut(tag, version); err != nil {
			return "", err
		}
		branch := releaseBranch + tag
		_, err = r.git("switch", "-c", branch)
		return tag, err
	}
	if _, err := r.git("fetch", "--quiet", "--tags", Remote, Branch); err != nil {
		return "", err
	}
	if err := r.restoreLatest(pr); err != nil {
		return "", err
	}
	newest, err := r.newestTag()
	if err != nil {
		return "", err
	}
	tag, err := NextTag(version, newest)
	if err != nil {
		return "", err
	}
	if isRelease && pending != tag {
		return "", fmt.Errorf("branch %s is for %s; requested %s", branch, pending, tag)
	}
	if _, err := r.git("rev-parse", "--quiet", "--verify", "refs/tags/"+tag); err == nil {
		return "", fmt.Errorf("%s already exists; pass a newer version", tag)
	}
	if !isRelease {
		if _, err := r.git("show-ref", "--verify", "--quiet", "refs/heads/"+releaseBranch+tag); err == nil {
			return "", fmt.Errorf("switch to %s%s to resume this release", releaseBranch, tag)
		}
	}
	pr.Appf("cutting %s (previous: %s)", tag, cmp.Or(newest, "none"))
	if err := st.Build(tag); err != nil {
		return "", err
	}
	if err := st.Stage(); err != nil {
		return "", err
	}
	m, err := readManifest(filepath.Join(r.Root, filepath.FromSlash(LatestJSON)))
	if err != nil {
		return "", err
	}
	if "v"+m.Version != tag {
		return "", fmt.Errorf("the stage wrote %s version %q, want %s", LatestJSON, m.Version, tag)
	}
	if err := stagedMatches(r.Root, m); err != nil {
		return "", err
	}
	if !isRelease {
		if _, err := r.git("switch", "-c", releaseBranch+tag); err != nil {
			return "", err
		}
	}
	quiet := r.with("SKIP_GIT_HOOKS=1")
	if _, err := quiet.git("commit", "--quiet", "-m", "release: "+tag, "--", LatestJSON); err != nil {
		return "", err
	}
	pr.Okf("cut %s on %s", tag, releaseBranch+tag)
	return tag, nil
}

func (r Repo) checkCut(tag, version string) error {
	if semverTag.MatchString(version) && version != tag {
		return fmt.Errorf("this release branch is for %s, requested %s", tag, version)
	}
	m, err := r.manifestAt("HEAD")
	changed, derr := r.git("diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	title, terr := r.git("log", "-1", "--format=%s")
	if err != nil || derr != nil || terr != nil || "v"+m.Version != tag || changed != LatestJSON || title != "release: "+tag {
		return fmt.Errorf("HEAD is not the release commit for %s", tag)
	}
	if dirty, err := r.dirty(); err != nil {
		return err
	} else if len(dirty) > 0 {
		return fmt.Errorf("the %s release tree has changes: %s", tag, strings.Join(dirty, ", "))
	}
	return nil
}

// restoreLatest refuses a dirty tree, except latest.json, which a snapshot stage rewrites.
func (r Repo) restoreLatest(pr *ui.Printer) error {
	dirty, err := r.dirty()
	if err != nil {
		return err
	}
	others := slices.DeleteFunc(slices.Clone(dirty), func(p string) bool { return p == LatestJSON })
	if len(others) > 0 {
		return fmt.Errorf("the tree has changes outside %s: %s; commit or stash them first", LatestJSON, strings.Join(others, ", "))
	}
	if len(dirty) == 0 {
		return nil
	}
	if _, err := r.git("checkout", "HEAD", "--", LatestJSON); err != nil {
		return err
	}
	pr.Notef("restored %s from HEAD; the stage rewrites it", LatestJSON)
	return nil
}

func (r Repo) dirty() ([]string, error) {
	out, err := r.raw("status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	entries := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		paths = append(paths, e[3:])
		if e[0] == 'R' || e[0] == 'C' {
			i++
		}
	}
	return paths, nil
}

// headTag is the highest vX.Y.Z tag on HEAD, or "".
func (r Repo) headTag() (string, error) {
	out, err := r.git("tag", "--points-at", "HEAD", "--list", "v*")
	if err != nil {
		return "", err
	}
	return highest(strings.Fields(out)), nil
}

// newestTag is the highest vX.Y.Z tag merged into HEAD, or "".
func (r Repo) newestTag() (string, error) {
	out, err := r.git("tag", "--merged", "HEAD", "--list", "v*")
	if err != nil {
		return "", err
	}
	return highest(strings.Fields(out)), nil
}

func (r Repo) manifestAt(rev string) (cdn.Manifest, error) {
	out, err := r.git("show", rev+":"+LatestJSON)
	if err != nil {
		return cdn.Manifest{}, err
	}
	var m cdn.Manifest
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		return cdn.Manifest{}, fmt.Errorf("%s at %s: %w", LatestJSON, rev, err)
	}
	return m, nil
}

func readManifest(path string) (cdn.Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return cdn.Manifest{}, err
	}
	var m cdn.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return cdn.Manifest{}, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// stagedMatches reports the first staged binary whose sha256 is not the one m records.
func stagedMatches(root string, m cdn.Manifest) error {
	if len(m.Checksums) == 0 {
		return errors.New("latest.json lists no binaries")
	}
	for asset, want := range m.Checksums {
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(binDir), asset))
		if err != nil {
			return fmt.Errorf("%s/%s is missing", binDir, asset)
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil || hex.EncodeToString(h.Sum(nil)) != want {
			return fmt.Errorf("%s/%s is not the binary latest.json names", binDir, asset)
		}
	}
	return nil
}

// ValidVersion reports whether version is patch, minor, major or an explicit vX.Y.Z.
func ValidVersion(version string) error {
	switch {
	case version == "patch", version == "minor", version == "major", semverTag.MatchString(version):
		return nil
	}
	return fmt.Errorf("version %q: want patch, minor, major or vX.Y.Z", version)
}

// NextTag is version when it is an explicit vX.Y.Z above newest, else newest bumped by patch, minor or major.
func NextTag(version, newest string) (string, error) {
	cur := parts(newest)
	switch version {
	case "patch":
		cur[2]++
	case "minor":
		cur = [3]int{cur[0], cur[1] + 1, 0}
	case "major":
		cur = [3]int{cur[0] + 1, 0, 0}
	default:
		if err := ValidVersion(version); err != nil {
			return "", err
		}
		if newest != "" && compare(parts(version), cur) <= 0 {
			return "", fmt.Errorf("%s does not follow %s", version, newest)
		}
		return version, nil
	}
	return fmt.Sprintf("v%d.%d.%d", cur[0], cur[1], cur[2]), nil
}

func parts(tag string) [3]int {
	var out [3]int
	m := semverTag.FindStringSubmatch(tag)
	if m == nil {
		return out
	}
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out
}

func compare(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return 0
}

// highest is the greatest vX.Y.Z among tags, or "".
func highest(tags []string) string {
	best := ""
	for _, t := range tags {
		if semverTag.MatchString(t) && (best == "" || compare(parts(t), parts(best)) > 0) {
			best = t
		}
	}
	return best
}
