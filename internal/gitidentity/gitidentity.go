// SPEC: _spec/_paradigms/git-identity.puml,
// _spec/_paradigms/harness-paradigms.puml, _spec/components.puml
//
// SPEC: _spec/_paradigms/git-identity.puml, _spec/_paradigms/harness-paradigms.puml, _spec/components.puml
package gitidentity

import (
	"os"
	"os/exec"
	"strings"
)

// Identity is the resolved author/committer name and email.
type Identity struct {
	Name  string
	Email string
}

func Resolve(getenv func(string) string, gitConfig func(key string) string) Identity {
	if getenv == nil {
		getenv = os.Getenv
	}
	if gitConfig == nil {
		gitConfig = hostGitConfig
	}
	name := strings.TrimSpace(getenv("GIT_AUTHOR_NAME"))
	if name == "" {
		name = strings.TrimSpace(getenv("GIT_COMMITTER_NAME"))
	}
	if name == "" {
		name = strings.TrimSpace(gitConfig("user.name"))
	}
	email := strings.TrimSpace(getenv("GIT_AUTHOR_EMAIL"))
	if email == "" {
		email = strings.TrimSpace(getenv("GIT_COMMITTER_EMAIL"))
	}
	if email == "" {
		email = strings.TrimSpace(gitConfig("user.email"))
	}
	return Identity{Name: name, Email: email}
}

// EnvPairs returns docker-style KEY=VALUE strings for non-empty identity fields
// (author + committer both set to the same resolved values).
func (id Identity) EnvPairs() []string {
	var out []string
	if id.Name != "" {
		out = append(out,
			"GIT_AUTHOR_NAME="+id.Name,
			"GIT_COMMITTER_NAME="+id.Name,
		)
	}
	if id.Email != "" {
		out = append(out,
			"GIT_AUTHOR_EMAIL="+id.Email,
			"GIT_COMMITTER_EMAIL="+id.Email,
		)
	}
	return out
}

func hostGitConfig(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
