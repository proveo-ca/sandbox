// SPEC: _spec/_devops/git-hooks.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/ui"
)

const (
	hookSrc = "scripts/githooks/post-commit"
	hookDst = ".git/hooks/post-commit"
)

func init() {
	hooks := &cobra.Command{Use: "hooks", Short: "Git hooks: install the post-commit shim, run its body"}
	hooks.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Copy scripts/githooks/post-commit into .git/hooks (chmod 0755, no git config)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			top, err := output("", "git", "rev-parse", "--show-toplevel")
			if err != nil {
				return fmt.Errorf("git rev-parse --show-toplevel: %w", err)
			}
			dst, err := installHook(top)
			if err != nil {
				return err
			}
			ui.Okf("installed %s", dst)
			return nil
		},
	})
	hooks.AddCommand(&cobra.Command{
		Use:   "post-commit",
		Short: "Run mise run fmt then mise run lint; SKIP_GIT_HOOKS=1 skips",
		Args:  cobra.ArbitraryArgs,
		RunE: func(*cobra.Command, []string) error {
			if skipHooks(os.Getenv("SKIP_GIT_HOOKS")) {
				return nil
			}
			top, err := output("", "git", "rev-parse", "--show-toplevel")
			if err != nil {
				return fmt.Errorf("git rev-parse --show-toplevel: %w", err)
			}
			if err := os.Setenv("PATH", hookPath(os.Getenv("HOME"), os.Getenv("PATH"))); err != nil {
				return err
			}
			if _, err := exec.LookPath("mise"); err != nil {
				ui.Warnf("post-commit: mise not on PATH; skip fmt/lint")
				return nil
			}
			if err := run(top, nil, "mise", "run", "fmt"); err != nil {
				return err
			}
			return run(top, nil, "mise", "run", "lint")
		},
	})
	register(hooks)
}

func skipHooks(v string) bool {
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func hookPath(home, path string) string {
	return strings.Join([]string{"/usr/local/bin", filepath.Join(home, ".local", "bin"), path}, string(os.PathListSeparator))
}

func installHook(top string) (string, error) {
	src := filepath.Join(top, hookSrc)
	body, err := os.ReadFile(src)
	if err != nil || len(body) == 0 {
		return "", errors.New("missing " + src)
	}
	dst := filepath.Join(top, hookDst)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, body, 0o755); err != nil {
		return "", err
	}
	return dst, os.Chmod(dst, 0o755)
}
