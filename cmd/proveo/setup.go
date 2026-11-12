// SPEC: _spec/internal/shell/setup-path.puml
package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/shell"
	"github.com/proveo-ca/proveo/internal/ui"
)

// executable is the running proveo binary's path.
var executable = os.Executable

func setupCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Add the proveo binary's directory to your shell PATH",
		Long: `Add the directory that holds this proveo binary to PATH in your shell's startup
file: ~/.zshrc, ~/.bash_profile (bash on macOS), ~/.bashrc, ~/.profile (sh, ksh)
or ~/.config/fish/config.fish. A directory already on PATH, or an rc file that
already names it, gets no second line.

The line reaches a new shell; ` + "`source <rc>`" + ` brings it into this one. csh and tcsh
are not written: setup prints the line to add.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			exe, err := executable()
			if err != nil {
				return reported(sandbox.Item{Fail: true, Text: "setup — stopped", Detail: errLines(err)}, err)
			}
			it := setupPath(filepath.Dir(exe), os.Getenv("SHELL"), printOnly)
			if it.Fail {
				return reported(it, errors.New(it.Text))
			}
			sandbox.PrintBlock(func() {}, nil, []sandbox.Item{it})
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "show the line it would add, without writing it")
	return cmd
}

// setupPath puts dir on PATH in the startup file of the shell at shellPath, unless printOnly, and returns
// the one item that says what changed or what is left to do; a failed item means setup stopped.
func setupPath(dir, shellPath string, printOnly bool) sandbox.Item {
	path := ui.Tilde
	if printOnly {
		path = sandbox.Absolute
	}
	if onPath(dir) {
		return sandbox.Item{Ok: true, Text: path(dir) + " — already on PATH"}
	}
	sh, ok := shell.Detect(shellPath)
	if !ok {
		why := filepath.Base(shellPath) + " is not a shell setup knows"
		if shellPath == "" {
			why = "$SHELL is not set"
		}
		fix := "add it to PATH in your shell's startup file"
		if runtime.GOOS == "windows" {
			fix = "add it to your user PATH"
		}
		return sandbox.Item{Fail: true, Text: path(dir) + " — not on PATH", Detail: []string{why, fix}}
	}
	home, _ := os.UserHomeDir()
	rc := sh.RCFile(runtime.GOOS, home)
	line := sh.PathLine(dir)
	toAdd := sandbox.Item{Warn: true, Text: path(dir) + " — not on PATH", Detail: []string{"add to " + path(rc) + ": " + line}}
	if !sh.Supported {
		return toAdd
	}
	content, _ := os.ReadFile(rc) // a missing rc is fine; it gets created
	if shell.AlreadyConfigured(string(content), dir) {
		return sandbox.Item{Warn: true, Text: path(dir) + " — not on this shell's PATH", Detail: []string{path(rc) + " adds it; a new shell picks it up"}}
	}
	if printOnly {
		return toAdd
	}
	if err := appendRC(rc, sh.Block(dir)); err != nil {
		return sandbox.Item{Fail: true, Text: path(rc) + " — not written", Detail: append(errLines(err), "add it yourself: "+line)}
	}
	return sandbox.Item{Ok: true, Text: path(dir) + " — added to PATH in " + path(rc), Detail: []string{"a new shell picks it up"}}
}

// appendRC appends block to the rc file, creating it and its directory when missing.
func appendRC(rc, block string) error {
	if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(rc, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(block); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func onPath(dir string) bool {
	dir = filepath.Clean(dir)
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p != "" && filepath.Clean(p) == dir {
			return true
		}
	}
	return false
}
