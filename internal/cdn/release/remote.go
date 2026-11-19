// SPEC: _spec/internal/cdn/distribution-update.puml
package release

import (
	"crypto/rand"
	"fmt"
	"strings"
)

const lockRef = "refs/heads/cli-deploy-lock"

func (r Repo) lock(tag, head string) (func() error, error) {
	identity := r.with("GIT_AUTHOR_NAME=proveo deploy", "GIT_COMMITTER_NAME=proveo deploy",
		"GIT_AUTHOR_EMAIL=deploy@proveo.ca", "GIT_COMMITTER_EMAIL=deploy@proveo.ca")
	owner, err := identity.git("-c", "commit.gpgsign=false", "commit-tree", head+"^{tree}", "-p", head,
		"-m", "CLI deploy "+tag, "-m", rand.Text())
	if err != nil {
		return nil, err
	}
	_, err = r.git("push", "--quiet", "--no-follow-tags", "--force-with-lease="+lockRef+":", Remote, owner+":"+lockRef)
	if err != nil {
		current, lookupErr := r.git("ls-remote", "--refs", Remote, lockRef)
		oid, _, _ := strings.Cut(current, "\t")
		switch {
		case lookupErr != nil || oid == "":
			return nil, fmt.Errorf("acquire %s on %s: %w", lockRef, Remote, err)
		case oid != owner:
			return nil, fmt.Errorf("CLI deploy is locked by %s on %s (%s)", lockRef, Remote, oid)
		}
	}
	return func() error {
		if _, err := r.git("push", "--quiet", "--no-follow-tags", "--force-with-lease="+lockRef+":"+owner, Remote, ":"+lockRef); err != nil {
			return fmt.Errorf("release the deployment lock at %s: %w", owner, err)
		}
		return nil
	}, nil
}

func (r Repo) remoteTags() (map[string]string, error) {
	out, err := r.git("ls-remote", "--tags", Remote, "refs/tags/v*")
	if err != nil {
		return nil, err
	}
	tags := make(map[string]string)
	for line := range strings.SplitSeq(out, "\n") {
		sha, ref, _ := strings.Cut(line, "\t")
		tag, annotated := strings.CutSuffix(strings.TrimPrefix(ref, "refs/tags/"), "^{}")
		if semverTag.MatchString(tag) && (annotated || tags[tag] == "") {
			tags[tag] = sha
		}
	}
	return tags, nil
}

func (r Repo) checkPreviousRelease(tags map[string]string, tag, head string) error {
	var latest string
	for remoteTag := range tags {
		if latest == "" || compare(remoteTag, latest) > 0 {
			latest = remoteTag
		}
	}
	if latest == "" {
		return nil
	}
	if compare(tag, latest) <= 0 {
		return fmt.Errorf("%s must follow the latest remote tag %s", tag, latest)
	}
	if _, err := r.git("merge-base", "--is-ancestor", tags[latest], head); err != nil {
		return fmt.Errorf("HEAD %.12s does not include the source commit of %s; check out newer %s/%s", head, latest, Remote, Branch)
	}
	return nil
}
