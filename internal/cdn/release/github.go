// SPEC: _spec/internal/cdn/distribution-update.puml
package release

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/proveo-ca/proveo/internal/ui"
)

const releaseBranch = "release/"

type pullRequest struct {
	State             string
	URL               string
	HeadRefOID        string
	IsCrossRepository bool
	MergeCommit       struct{ OID string }
}

func submit(r Repo, tag string, st Steps, pr *ui.Printer) (bool, error) {
	pushURL, err := r.git("remote", "get-url", "--push", Remote)
	if err != nil {
		return false, err
	}
	repository, err := st.GitHub("repo", "view", pushURL, "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return false, fmt.Errorf("resolve GitHub repository for %s: %w", Remote, err)
	}
	repository = strings.TrimSpace(repository)
	head, err := r.git("rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	branch := releaseBranch + tag
	pull, err := findPR(st, repository, branch, head)
	if err != nil {
		return false, err
	}
	switch pull.State {
	case "":
		if err := r.checkReleaseBase(head); err != nil {
			return false, err
		}
		if _, err := r.git("push", "--quiet", "--no-follow-tags", Remote, head+":refs/heads/"+branch); err != nil {
			return false, err
		}
		url, err := st.GitHub("pr", "create", "--repo", repository, "--base", Branch, "--head", branch,
			"--title", "release: "+tag, "--body", "Update the CLI distribution manifest for "+tag+".")
		if err != nil {
			return false, err
		}
		pull.URL = strings.TrimSpace(url)
	case "OPEN":
	case "MERGED":
		return finish(r, tag, head, pull, st, pr)
	default:
		return false, fmt.Errorf("release PR %s is %s; reopen and merge it to continue", pull.URL, pull.State)
	}
	pr.Notef("merge %s, then rerun this deploy from %s", pull.URL, branch)
	return false, nil
}

func (r Repo) checkReleaseBase(head string) error {
	if _, err := r.git("fetch", "--quiet", "--no-tags", Remote, Branch); err != nil {
		return err
	}
	base, err := r.git("rev-parse", head+"^", "refs/remotes/"+Remote+"/"+Branch)
	if err != nil {
		return err
	}
	parent, upstream, _ := strings.Cut(base, "\n")
	if parent != upstream {
		return fmt.Errorf("the release was built from %.12s, but %s/%s is %.12s", parent, Remote, Branch, upstream)
	}
	return nil
}

func findPR(st Steps, repository, branch, head string) (pullRequest, error) {
	out, err := st.GitHub("pr", "list", "--repo", repository, "--base", Branch, "--head", branch, "--state", "all",
		"--json", "state,url,headRefOid,isCrossRepository,mergeCommit")
	if err != nil {
		return pullRequest{}, err
	}
	var pulls []pullRequest
	if err := json.Unmarshal([]byte(out), &pulls); err != nil {
		return pullRequest{}, fmt.Errorf("read release PRs for %s: %w", branch, err)
	}
	for _, pull := range pulls {
		if pull.IsCrossRepository {
			continue
		}
		if pull.HeadRefOID != head {
			return pullRequest{}, fmt.Errorf("release PR %s is for a different commit on %s", pull.URL, branch)
		}
		return pull, nil
	}
	return pullRequest{}, nil
}

func finish(r Repo, tag, head string, pull pullRequest, st Steps, pr *ui.Printer) (bool, error) {
	if _, err := r.git("fetch", "--quiet", "--no-tags", Remote, Branch); err != nil {
		return false, err
	}
	main := "refs/remotes/" + Remote + "/" + Branch
	merged, err := r.git("rev-parse", "--verify", pull.MergeCommit.OID+"^{commit}")
	if err != nil {
		return false, fmt.Errorf("cannot resolve the merge commit of %s: %w", pull.URL, err)
	}
	if _, err := r.git("merge-base", "--is-ancestor", merged, main); err != nil {
		return false, fmt.Errorf("merge commit %.12s of %s is not on %s/%s", merged, pull.URL, Remote, Branch)
	}
	ref := "refs/tags/" + tag
	published, err := r.remoteTag(ref)
	if err != nil {
		return false, err
	}
	if published != "" && published != merged {
		return false, fmt.Errorf("%s already points to %.12s on %s", tag, published, Remote)
	}
	local, _ := r.git("rev-parse", "--quiet", "--verify", ref)
	if local != "" && local != head && local != merged {
		return false, fmt.Errorf("local %s points to a different release", tag)
	}
	if published == "" {
		if err := verifyMerged(r, head, merged, main); err != nil {
			return false, err
		}
		pr.Cloudf("deploying apps/cli as %s", tag)
		if err := st.Publish(); err != nil {
			return false, err
		}
		if _, err := r.git("push", "--quiet", "--no-follow-tags", Remote, merged+":"+ref); err != nil {
			return false, err
		}
	}
	if local != merged {
		if _, err := r.git("update-ref", ref, merged, local); err != nil {
			return false, err
		}
	}
	pr.Okf("published %s and tagged %.12s on %s/%s", tag, merged, Remote, Branch)
	return true, nil
}

func verifyMerged(r Repo, head, merged, main string) error {
	trees, err := r.git("rev-parse", head+"^{tree}", merged+"^{tree}")
	if err != nil {
		return err
	}
	cutTree, mergedTree, _ := strings.Cut(trees, "\n")
	if cutTree != mergedTree {
		return fmt.Errorf("merged release tree differs from the built tree; cut a new explicit version from updated %s", Branch)
	}
	manifests, err := r.git("rev-parse", merged+":"+LatestJSON, main+":"+LatestJSON)
	if err != nil {
		return err
	}
	cutManifest, currentManifest, _ := strings.Cut(manifests, "\n")
	if cutManifest != currentManifest {
		return fmt.Errorf("%s/%s now carries a different CLI manifest", Remote, Branch)
	}
	m, err := r.manifestAt(merged)
	if err != nil {
		return err
	}
	if err := stagedMatches(r.Root, m); err != nil {
		return fmt.Errorf("restore the staged release assets from the original build: %w", err)
	}
	return nil
}

func (r Repo) remoteTag(ref string) (string, error) {
	out, err := r.git("ls-remote", "--tags", Remote, ref, ref+"^{}")
	if err != nil {
		return "", err
	}
	var oid string
	for line := range strings.SplitSeq(out, "\n") {
		sha, name, _ := strings.Cut(line, "\t")
		switch name {
		case ref + "^{}":
			return sha, nil
		case ref:
			oid = sha
		}
	}
	return oid, nil
}
