// SPEC: _spec/internal/cdn/distribution-update.puml
// Package release prepares and publishes CLI releases.
package release

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/proveo-ca/proveo/internal/cdn"
	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	Branch = "main"
	Remote = "origin"

	mainRef   = "refs/remotes/" + Remote + "/" + Branch
	ledgerRun = "-run=^TestNoRetirementOutlivesOneRelease$"
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
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Root
	cmd.Env = r.Env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// Steps configures the release commands.
type Steps struct {
	Ledger  func() error
	Confirm func(question string) bool
	Build   func(tag string) error
	Stage   func(path string) error
	Publish func(path string) error
	Live    func() (cdn.Manifest, error)
	Verify  func(cdn.Manifest) error
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
		Stage: func(path string) error { return command(root, nil, "go", "run", "./internal/cdn/stage", path) },
		Publish: func(path string) error {
			return command(root, nil, "wrangler", "deploy", "--cwd", "apps/cli", "--assets", path)
		},
		Live:   liveManifest,
		Verify: verifyLive,
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
	return []string{
		"PROVEO_RELEASE_GATE=1 go test -count=1 " + ledgerRun + " ./internal/contract/",
		"git fetch --tags " + Remote + " " + Branch + " and verify the pinned HEAD is on " + Remote + "/" + Branch,
		"confirm " + version + " for the pinned commit (unless --yes)",
		"acquire the shared Git lock " + lockRef,
		"check the remote tags and live CLI version",
		"reuse the staged artifact or run GORELEASER_CURRENT_TAG=" + version + " goreleaser release --clean --skip=publish,validate",
		"go run ./internal/cdn/stage <artifact>/cli",
		"wrangler deploy --cwd apps/cli --assets <artifact> (unless the exact manifest is already live)",
		"verify the live manifest version and checksums",
		"git push " + Remote + " <commit>:refs/tags/" + version,
		"release the shared Git lock",
	}
}

// Deploy publishes the CLI built from the pinned commit.
func Deploy(r Repo, tag string, st Steps, pr *ui.Printer) (err error) {
	if err := ValidVersion(tag); err != nil {
		return err
	}
	if err := st.Ledger(); err != nil {
		return fmt.Errorf("retirement ledger: %w", err)
	}
	head, err := r.sourceCommit()
	if err != nil {
		return err
	}
	if !st.Confirm(fmt.Sprintf("Publish %s from %.12s to Cloudflare and tag that commit?", tag, head)) {
		return errors.New("release not confirmed (--yes answers the question)")
	}
	unlock, err := r.lock(tag, head)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	return deployLocked(r, tag, head, st, pr)
}

func (r Repo) sourceCommit() (string, error) {
	dirty, err := r.git("status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if dirty != "" {
		return "", fmt.Errorf("a CLI release needs a clean checkout:\n%s", dirty)
	}
	if _, err := r.git("fetch", "--quiet", "--tags", Remote, "refs/heads/"+Branch+":"+mainRef); err != nil {
		return "", err
	}
	head, err := r.git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := r.git("merge-base", "--is-ancestor", head, mainRef); err != nil {
		return "", fmt.Errorf("HEAD %.12s is outside %s/%s; check out the approved commit from %s/%s", head, Remote, Branch, Remote, Branch)
	}
	return head, nil
}

func deployLocked(r Repo, tag, head string, st Steps, pr *ui.Printer) error {
	tags, err := r.remoteTags()
	if err != nil {
		return err
	}
	if commit := tags[tag]; commit != "" {
		if commit != head {
			return fmt.Errorf("%s already points to %.12s on %s, requested %.12s", tag, commit, Remote, head)
		}
		pr.Okf("%s already tagged at %.12s on %s", tag, head, Remote)
		return nil
	}
	if err := r.checkPreviousRelease(tags, tag, head); err != nil {
		return err
	}

	live, err := st.Live()
	if err != nil {
		return fmt.Errorf("read the live release before publishing: %w", err)
	}
	if liveTag := "v" + live.Version; semverTag.MatchString(liveTag) && compare(liveTag, tag) > 0 {
		return fmt.Errorf("%s is already live; choose a version newer than %s", liveTag, liveTag)
	}
	assets, manifest, err := r.artifact(tag, head, st, pr)
	if err != nil {
		return err
	}
	if live.Version == manifest.Version {
		if !sameManifest(live, manifest) {
			return fmt.Errorf("%s is already live with different checksums; restore the original artifact or choose a new version", tag)
		}
		pr.Notef("%s is already live with the staged checksums", tag)
	} else {
		current, err := r.git("rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if current != head {
			return fmt.Errorf("HEAD moved during the release; retry from %.12s", head)
		}
		pr.Cloudf("deploying apps/cli as %s", tag)
		if err := st.Publish(assets); err != nil {
			return err
		}
		if err := st.Verify(manifest); err != nil {
			return err
		}
	}
	if _, err := r.git("push", "--quiet", "--no-follow-tags", Remote, head+":refs/tags/"+tag); err != nil {
		return err
	}
	pr.Okf("published %s and tagged %.12s on %s", tag, head, Remote)
	return nil
}

func sameManifest(a, b cdn.Manifest) bool {
	return a.Version == b.Version && maps.Equal(a.Checksums, b.Checksums)
}

func liveManifest() (cdn.Manifest, error) {
	url := cdn.DefaultBaseURL + "?release-check=" + strconv.FormatInt(time.Now().UnixNano(), 10)
	return cdn.FetchManifest(&http.Client{Timeout: 5 * time.Second}, url)
}

func verifyLive(want cdn.Manifest) error {
	var err error
	for attempt := range 6 {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
		}
		var live cdn.Manifest
		live, err = liveManifest()
		if err == nil {
			if sameManifest(live, want) {
				return nil
			}
			err = fmt.Errorf("live version %s or its checksums differ from the staged manifest", live.Version)
		}
	}
	return fmt.Errorf("verify published v%s: %w", want.Version, err)
}

// ValidVersion accepts an explicit vX.Y.Z.
func ValidVersion(version string) error {
	if !semverTag.MatchString(version) {
		return fmt.Errorf("version %q: pass an explicit vX.Y.Z", version)
	}
	return nil
}

func compare(a, b string) int {
	return slices.CompareFunc(strings.Split(a, "."), strings.Split(b, "."), func(x, y string) int {
		if len(x) != len(y) {
			return len(x) - len(y)
		}
		return strings.Compare(x, y)
	})
}
