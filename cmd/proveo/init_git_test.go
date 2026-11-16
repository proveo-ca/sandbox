package main

import (
	"os/exec"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/gitaccess"
)

func TestInitGitIdentityOkWhenComplete(t *testing.T) {
	t.Parallel()
	var wrote bool
	applyGitIdentity(gitStage{
		resolve: func() gitaccess.Identity {
			return gitaccess.Identity{Name: "Ada", Email: "ada@ex"}
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
	mark, text := applyGitIdentity(gitStage{
		resolve:  func() gitaccess.Identity { return gitaccess.Identity{} },
		lookPath: func(string) (string, error) { return "/usr/bin/git", nil },
		setGlobal: func(string, string) error {
			t.Error("must not write git config without a terminal")
			return nil
		},
		typed: func() (string, string) {
			t.Error("must not read the fields without a terminal")
			return "x", "y"
		},
	})
	if mark != choiceui.MarkNeeds || text != "not set" {
		t.Errorf("a headless missing identity = %v %q, want ! not set", mark, text)
	}
}

// Page 2's user.name and user.email fields replace the line prompts: they open under git identity
// while it reads now, prefilled from what git resolves, and the step writes what they hold; an
// empty one writes nothing and says both are needed.
func TestGitIdentityFieldsReplaceTheLinePrompts(t *testing.T) {
	t.Parallel()
	partial := gitaccess.Identity{Name: "Ada"}
	f := gitForm(gitPageIn{id: partial, gitOK: true}, initStepAt(2), initDecisions{Git: true}, initOptions{skipPrompt: true}, nil, nil,
		func(initDecisions) int { return 1 })
	var name, email *choiceui.Row
	for i := range f.Rows {
		switch f.Rows[i].Label {
		case rowName:
			name = &f.Rows[i]
		case rowEmail:
			email = &f.Rows[i]
		}
	}
	if name == nil || email == nil || !name.Field || !email.Field || name.When == nil || name.When.Row != rowGit || name.When.Option != "now" {
		t.Fatalf("git identity's fields do not open under now: %+v %+v", name, email)
	}
	if name.Value != "Ada" || email.Value != "" {
		t.Errorf("the fields are not prefilled from git: %q %q", name.Value, email.Value)
	}
	for _, c := range []struct {
		name, email string
		mark        choiceui.Mark
		text        string
		writes      bool
	}{
		{"Ada", "ada@ex", choiceui.MarkDone, "Ada <ada@ex> · git's global config", true},
		{"Ada", "", choiceui.MarkNeeds, "not set · both fields needed", false},
	} {
		var wrote []string
		mark, text := applyGitIdentity(gitStage{
			resolve:   func() gitaccess.Identity { return partial },
			lookPath:  func(string) (string, error) { return "/usr/bin/git", nil },
			typed:     func() (string, string) { return c.name, c.email },
			setGlobal: func(n, e string) error { wrote = append(wrote, n, e); return nil },
			tty:       true,
		})
		if mark != c.mark || text != c.text || (len(wrote) > 0) != c.writes {
			t.Errorf("fields %q %q: %v %q, wrote %q", c.name, c.email, mark, text, wrote)
		}
	}
}

func TestInitGitIdentityMissingGit(t *testing.T) {
	t.Parallel()
	applyGitIdentity(gitStage{
		resolve:  func() gitaccess.Identity { return gitaccess.Identity{Name: "Ada", Email: "ada@ex"} },
		lookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		setGlobal: func(string, string) error {
			t.Error("must not write config when git is absent")
			return nil
		},
		tty: true,
	})
}
