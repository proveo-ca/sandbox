// Package shell knows how to add a directory to PATH across shells — the typed,
// tested replacement for the unparsed registry/shells.yaml. It captures the real
// per-shell differences (fish's `set -gx` vs POSIX `export`, and bash's
// macOS .bash_profile vs Linux .bashrc) so `proveo setup` can self-install.
// SPEC: _spec/internal/shell/setup-path.puml
package shell

import (
	"path/filepath"
	"strings"
)

// Marker delimits the block proveo appends to a shell rc, so it can be detected
// (idempotency) and removed later.
const Marker = "# added by `proveo setup` — proveo on PATH"

// Shell describes one shell's rc location and PATH syntax.
type Shell struct {
	Name      string
	Supported bool
}

var known = map[string]Shell{
	"bash": {Name: "bash", Supported: true},
	"zsh":  {Name: "zsh", Supported: true},
	"fish": {Name: "fish", Supported: true},
	"sh":   {Name: "sh", Supported: true},
	"ksh":  {Name: "ksh", Supported: true},
	"csh":  {Name: "csh", Supported: false},
	"tcsh": {Name: "tcsh", Supported: false},
}

// Detect resolves a Shell from a $SHELL-style path (e.g. "/bin/zsh" -> zsh).
func Detect(shellPath string) (Shell, bool) {
	base := filepath.Base(strings.TrimSpace(shellPath))
	s, ok := known[base]
	return s, ok
}

// RCFile returns the shell's startup file for the given GOOS and home dir.
func (s Shell) RCFile(goos, home string) string {
	switch s.Name {
	case "bash":
		// macOS Terminal starts login shells, which read .bash_profile.
		if goos == "darwin" {
			return filepath.Join(home, ".bash_profile")
		}
		return filepath.Join(home, ".bashrc")
	case "zsh":
		return filepath.Join(home, ".zshrc")
	case "fish":
		return filepath.Join(home, ".config", "fish", "config.fish")
	case "csh":
		return filepath.Join(home, ".cshrc")
	case "tcsh":
		return filepath.Join(home, ".tcshrc")
	default: // sh, ksh
		return filepath.Join(home, ".profile")
	}
}

// PathLine returns the line that prepends binDir to PATH in this shell's syntax.
func (s Shell) PathLine(binDir string) string {
	switch s.Name {
	case "fish":
		return `set -gx PATH "` + binDir + `" $PATH`
	case "csh", "tcsh":
		return `setenv PATH "` + binDir + `:$PATH"`
	default: // bash, zsh, sh, ksh
		return `export PATH="` + binDir + `:$PATH"`
	}
}

// ExportLine returns a line that sets name=value in this shell's syntax
// (placeholder-safe for secrets the user pastes themselves).
func (s Shell) ExportLine(name, value string) string {
	switch s.Name {
	case "fish":
		return `set -gx ` + name + ` "` + value + `"`
	case "csh", "tcsh":
		return `setenv ` + name + ` "` + value + `"`
	default: // bash, zsh, sh, ksh
		return `export ` + name + `="` + value + `"`
	}
}

// Block is the full snippet appended to the rc file (marker + PATH line).
func (s Shell) Block(binDir string) string {
	return "\n" + Marker + "\n" + s.PathLine(binDir) + "\n"
}

// AlreadyConfigured reports whether rcContent already puts binDir on PATH (so
// setup is idempotent).
func AlreadyConfigured(rcContent, binDir string) bool {
	return strings.Contains(rcContent, Marker) || strings.Contains(rcContent, binDir)
}
