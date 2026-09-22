// SPEC: _spec/_plans/host-shell-to-go.puml
// Command proveo-dev is the maintainer tooling mise runs; it is not shipped.
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

var root = &cobra.Command{
	Use:           "proveo-dev",
	Short:         "Maintainer tooling for the proveo repo (mise tasks call it)",
	SilenceUsage:  true,
	SilenceErrors: true,
}

// register adds a subcommand; each tool registers itself from its own file.
func register(c *cobra.Command) { root.AddCommand(c) }

func main() {
	if err := root.Execute(); err != nil {
		if ee, ok := errors.AsType[*exitError](err); ok {
			os.Exit(ee.code)
		}
		ui.Failf("%v", err)
		os.Exit(1)
	}
}

// exitError carries a child's exit status through cobra without a message.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// repoRoot is the directory holding go.mod, found from the working directory.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("not inside the proveo repo (no go.mod above the working directory)")
		}
		dir = parent
	}
}

// run executes name args in dir with the caller's stdio and extra env.
func run(dir string, env []string, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if len(env) > 0 {
		c.Env = append(os.Environ(), env...)
	}
	err := c.Run()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return &exitError{code: ee.ExitCode()}
	}
	return err
}

// output executes name args in dir and returns trimmed stdout.
func output(dir string, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Stderr = os.Stderr
	b, err := c.Output()
	return strings.TrimSpace(string(b)), err
}
