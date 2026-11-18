// SPEC: _spec/internal/cdn/distribution-update.puml
// Package release cuts, publishes and pushes a CLI release; `mise run deploy --cli` reaches it through the fleet main.
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
	question   = "Cut a CLI release, deploy apps/cli to Cloudflare, then push main and its tag?"
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
	cmd := exec.Command("git", append([]string{"-C", r.Root}, args...)...)
	cmd.Env = r.Env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// Steps are the side effects Deploy orders around its git work.
type Steps struct {
	Ledger  func() error
	Confirm func(question string) bool
	Build   func(tag string) error
	Stage   func() error
	Publish func() error
}

// DefaultSteps runs the retirement ledger, goreleaser, the CDN stage and wrangler from root.
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

// Plan is what Deploy runs, one line per step, for --print.
func Plan(version string) []string {
	tag := "<" + version + ">"
	if semverTag.MatchString(version) {
		tag = version
	}
	return []string{
		"PROVEO_RELEASE_GATE=1 go test -count=1 " + ledgerRun + " ./internal/contract/",
		"ask: " + question,
		"git fetch --quiet --tags " + Remote + " " + Branch,
		"GORELEASER_CURRENT_TAG=" + tag + " goreleaser release --clean --skip=publish,validate",
		"go run ./internal/cdn/stage",
		"git commit -m 'release: " + tag + "' -- " + LatestJSON,
		"git tag " + tag,
		"wrangler deploy --cwd apps/cli",
		"git push --atomic " + Remote + " HEAD:refs/heads/" + Branch + " refs/tags/" + tag,
	}
}

// Deploy gates, asks, cuts (or resumes) a release, publishes apps/cli, then pushes main and the tag.
func Deploy(r Repo, version string, st Steps, pr *ui.Printer) error {
	if err := st.Ledger(); err != nil {
		return fmt.Errorf("retirement ledger: %w", err)
	}
	if !st.Confirm(question) {
		return errors.New("not confirmed: nothing was cut, deployed or pushed (--yes answers the question)")
	}
	tag, err := Cut(r, version, st, pr)
	if err != nil {
		return err
	}
	if why := StagedRefusal(r); why != "" {
		return errors.New(why)
	}
	pr.Cloudf("deploying apps/cli as %s", tag)
	if err := st.Publish(); err != nil {
		return err
	}
	return Push(r, pr)
}

// Cut builds and stages the next release, commits latest.json and tags that commit; a cut HEAD resumes.
func Cut(r Repo, version string, st Steps, pr *ui.Printer) (string, error) {
	if b, err := r.git("symbolic-ref", "--quiet", "--short", "HEAD"); err != nil || b != Branch {
		return "", fmt.Errorf("a release cuts %s; HEAD is on %q", Branch, b)
	}
	if _, err := r.git("fetch", "--quiet", "--tags", Remote, Branch); err != nil {
		return "", err
	}
	if tag, err := r.headTag(); err != nil {
		return "", err
	} else if tag != "" {
		return tag, r.resume(tag, version, pr)
	}
	head, err := r.git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	upstream, err := r.git("rev-parse", "refs/remotes/"+Remote+"/"+Branch)
	if err != nil {
		return "", err
	}
	if head != upstream {
		return "", fmt.Errorf("HEAD %.12s is not %s/%s %.12s; a release cuts what %s/%s holds: pull or push first",
			head, Remote, Branch, upstream, Remote, Branch)
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
	if _, err := r.git("rev-parse", "--quiet", "--verify", "refs/tags/"+tag); err == nil {
		return "", fmt.Errorf("%s already exists; pass a newer version", tag)
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
	quiet := r.with("SKIP_GIT_HOOKS=1")
	for _, args := range [][]string{
		{"add", "--", LatestJSON},
		{"commit", "--quiet", "-m", "release: " + tag, "--", LatestJSON},
		{"tag", tag},
	} {
		if _, err := quiet.git(args...); err != nil {
			return "", err
		}
	}
	pr.Okf("cut %s: its commit carries %s", tag, LatestJSON)
	return tag, nil
}

// resume accepts a HEAD that Cut already made: tagged, latest.json alone, clean, and its binaries still staged.
func (r Repo) resume(tag, version string, pr *ui.Printer) error {
	if semverTag.MatchString(version) && version != tag {
		return fmt.Errorf("HEAD is cut as %s, not %s", tag, version)
	}
	m, err := r.manifestAt("HEAD")
	changed, derr := r.git("diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if err != nil || derr != nil || "v"+m.Version != tag || changed != LatestJSON {
		return fmt.Errorf("HEAD carries %s but is no release commit; `git tag -d %s` if it never left this machine, then rerun", tag, tag)
	}
	if dirty, err := r.dirty(); err != nil {
		return err
	} else if len(dirty) > 0 {
		return fmt.Errorf("HEAD is cut as %s, but the tree has changes: %s", tag, strings.Join(dirty, ", "))
	}
	if err := stagedMatches(r.Root, m); err != nil {
		return fmt.Errorf("HEAD is cut as %s, but %w; if neither left this machine, drop the cut with "+
			"`git tag -d %s && git reset --hard HEAD~1` and rerun", tag, err, tag)
	}
	pr.Notef("HEAD is cut as %s; resuming at the deploy", tag)
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

// Push sends main and HEAD's release tag to origin in one atomic push.
func Push(r Repo, pr *ui.Printer) error {
	tag, err := r.headTag()
	if err != nil {
		return err
	}
	if tag == "" {
		return errors.New("HEAD carries no vX.Y.Z tag; `mise run deploy --cli` cuts one")
	}
	if _, err := r.git("push", "--atomic", "--quiet", Remote, "HEAD:refs/heads/"+Branch, "refs/tags/"+tag+":refs/tags/"+tag); err != nil {
		return err
	}
	pr.Okf("pushed %s and %s to %s", Branch, tag, Remote)
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

// StagedRefusal is "" when HEAD carries a vX.Y.Z tag and the staged latest.json names its version, else one line why not.
func StagedRefusal(r Repo) string {
	tag, err := r.headTag()
	if err != nil {
		return err.Error()
	}
	if tag == "" {
		sha, _ := r.git("rev-parse", "--short", "HEAD")
		return "HEAD " + sha + " carries no vX.Y.Z tag; `mise run deploy --cli` cuts and tags one"
	}
	m, err := readManifest(filepath.Join(r.Root, filepath.FromSlash(LatestJSON)))
	if err != nil {
		return fmt.Sprintf("%v; `mise run deploy --cli` stages it", err)
	}
	if "v"+m.Version != tag {
		return fmt.Sprintf("%s names %q, not HEAD's %s; `git checkout -- %s` restores the cut one",
			LatestJSON, m.Version, tag, LatestJSON)
	}
	return ""
}
