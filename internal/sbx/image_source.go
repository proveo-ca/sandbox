// SPEC: _spec/internal/sbx/kit-image-source.puml
package sbx

import (
	"encoding/json"
	"os/exec"
	"strings"
)

// ImageSource is where a workload's base image lives and which buildx builder reaches it.
type ImageSource struct {
	Ref     string // the chosen reference, e.g. proveo/codex:local
	Host    bool   // the host engine holds Ref
	Digest  string // Ref's repo digest in the host store; "" when the store keeps none
	Builder string // buildx builder that reads the host store; "" leaves the active one
}

// From is the recipe's FROM: Ref pinned to Digest when the host store has one.
func (s ImageSource) From() string {
	if s.Digest == "" || strings.Contains(s.Ref, "@") {
		return s.Ref
	}
	return s.Ref + "@" + s.Digest
}

// BuildEnv is the env the `sbx create` process needs to build the workload FROM this source.
func (s ImageSource) BuildEnv() []string {
	if s.Builder == "" {
		return nil
	}
	return []string{"BUILDX_BUILDER=" + s.Builder}
}

// ResolveImageSource locates ref in the host engine; hostBuilder runs only for a host image.
func ResolveImageSource(ref string, hostBuilder func() (string, bool)) ImageSource {
	src := ImageSource{Ref: ref}
	if ref == "" || sh.LocalImageID(ref) == "" {
		return src
	}
	src.Host = true
	src.Digest = RepoDigest(ref, sh.RepoDigests(ref))
	if hostBuilder != nil {
		if name, ok := hostBuilder(); ok {
			src.Builder = name
		}
	}
	return src
}

// RepoDigest is the digest in repoDigests recorded under ref's own repository.
func RepoDigest(ref string, repoDigests []string) string {
	repo := familiarRepo(refRepo(ref))
	for _, rd := range repoDigests {
		name, digest, ok := strings.Cut(rd, "@")
		if ok && familiarRepo(name) == repo {
			return digest
		}
	}
	return ""
}

// refRepo is ref without its tag or digest.
func refRepo(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[:colon]
	}
	return ref
}

func familiarRepo(repo string) string {
	repo = strings.TrimPrefix(repo, "docker.io/")
	return strings.TrimPrefix(repo, "library/")
}

func dockerRepoDigests(image string) []string {
	out, err := exec.Command("docker", "image", "inspect", image, "--format", "{{json .RepoDigests}}").Output()
	if err != nil {
		return nil
	}
	var digests []string
	if json.Unmarshal(out, &digests) != nil {
		return nil
	}
	return digests
}
