package main

import (
	"os/exec"
	"testing"

	"github.com/proveo-ca/proveo/internal/gitidentity"
)

func TestInitGitIdentityOkWhenComplete(t *testing.T) {
	t.Parallel()
	var wrote bool
	applyGitIdentity(gitStage{
		resolve: func() gitidentity.Identity {
			return gitidentity.Identity{Name: "Ada", Email: "ada@ex"}
		},
		lookPath: func(string) (string, error) { return "/usr/bin/git", nil },
		setGlobal: func(string, string) error {
			wrote = true
			return nil
		},
		tty: true,
	})
	if wrote {
		t.Error("a complete identity must not be rewritten")
	}
}

func TestInitGitIdentityHeadlessDoesNotWrite(t *testing.T) {
	t.Parallel()
	applyGitIdentity(gitStage{
		resolve:  func() gitidentity.Identity { return gitidentity.Identity{} },
		lookPath: func(string) (string, error) { return "/usr/bin/git", nil },
		setGlobal: func(string, string) error {
			t.Error("must not write git config without a terminal")
			return nil
		},
		prompt: func(string) string {
			t.Error("must not prompt without a terminal")
			return "x"
		},
	})
}

func TestInitGitIdentityPromptsAndWrites(t *testing.T) {
	t.Parallel()
	var name, email string
	applyGitIdentity(gitStage{
		resolve:  func() gitidentity.Identity { return gitidentity.Identity{} },
		lookPath: func(string) (string, error) { return "/usr/bin/git", nil },
		prompt: func(label string) string {
			if label == "git user.name" {
				return "Ada"
			}
			return "ada@ex"
		},
		setGlobal: func(n, e string) error { name, email = n, e; return nil },
		tty:       true,
	})
	if name != "Ada" || email != "ada@ex" {
		t.Errorf("wrote %s <%s>", name, email)
	}
}

func TestInitGitIdentityMissingGit(t *testing.T) {
	t.Parallel()
	applyGitIdentity(gitStage{
		resolve:  func() gitidentity.Identity { return gitidentity.Identity{Name: "Ada", Email: "ada@ex"} },
		lookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		setGlobal: func(string, string) error {
			t.Error("must not write config when git is absent")
			return nil
		},
		tty: true,
	})
}
