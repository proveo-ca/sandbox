// SPEC: _spec/internal/results/result-store.puml
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/results"
	"github.com/proveo-ca/proveo/internal/run"
	"github.com/proveo-ca/proveo/internal/shell"
)

// promptCmd prints the shell-prompt segment: what earlier runs left in this repository.
// A shell calls it on every prompt, so it reads the store and starts no process.
func promptCmd() *cobra.Command {
	var initShell string
	cmd := &cobra.Command{
		Use:    "prompt",
		Short:  "Print what earlier runs left in this repository, for a shell prompt",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if initShell != "" {
				line, err := starshipInit(initShell)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), line)
				return nil
			}
			wd, err := os.Getwd()
			if err != nil {
				return nil
			}
			if line := leftLine(run.StateDir(), wd); line != "" {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&initShell, "init", "", "print the line that starts starship in this shell (bash, zsh, fish)")
	return cmd
}

// starshipInit is the rc line that starts starship in the named shell.
func starshipInit(name string) (string, error) {
	sh, _ := shell.Detect(name)
	if line := sh.StarshipLine(); line != "" {
		return line, nil
	}
	return "", fmt.Errorf("no starship line is known for %q; proveo writes one for bash, zsh and fish", name)
}

// leftLine is the segment for the repository dir sits in; "" outside one or when nothing was left.
func leftLine(state, dir string) string {
	repo := results.RepoOf(dir)
	if repo == "" {
		return ""
	}
	return results.Summary(results.List(results.Dir(state)), repo)
}
