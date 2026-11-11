// SPEC: _spec/internal/shell/setup-path.puml
package shell

import (
	"path/filepath"
	"strings"
)

// Marker delimits the block proveo appends to a shell rc, so it can be detected
// (idempotency) and removed later.
const Marker = "# added by `proveo setup` — proveo on PATH"

// SbxMarker is the same idea for the OTHER directory proveo puts on PATH: the
// sbx prefix, written by `proveo init`.
const SbxMarker = "# added by `proveo init` — sbx on PATH"

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

func Detect(shellPath string) (Shell, bool) {
	base := filepath.Base(strings.TrimSpace(shellPath))
	s, ok := known[base]
	return s, ok
}

// RCFile returns the shell's startup file for the given GOOS and home dir.
func (s Shell) RCFile(goos, home string) string {
	switch s.Name {
	case "bash":
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

// PathLine returns the line that prepends binDir to PATH in this shell's
// syntax.
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

// SbxBlock is Block for the sbx prefix's bin directory.
func (s Shell) SbxBlock(binDir string) string {
	return "\n" + SbxMarker + "\n" + s.PathLine(binDir) + "\n"
}

// PromptMarker opens the block `proveo init` appends to turn starship on.
const PromptMarker = "# added by `proveo init` — starship prompt"

// StarshipLine is the line that starts starship in this shell; "" for a shell starship
// does not hook through an rc line proveo knows.
func (s Shell) StarshipLine() string {
	switch s.Name {
	case "bash":
		return `eval "$(starship init bash)"`
	case "zsh":
		return `eval "$(starship init zsh)"`
	case "fish":
		return `starship init fish | source`
	}
	return ""
}

// PromptBlock is the snippet appended to the rc file to turn starship on.
func (s Shell) PromptBlock() string {
	return "\n" + PromptMarker + "\n" + s.StarshipLine() + "\n"
}

// StartsStarship reports whether an rc file already starts starship, by proveo or by hand.
func StartsStarship(rcContent string) bool {
	return strings.Contains(rcContent, "starship init")
}

// WithoutPromptBlock is rcContent without the block PromptBlock appended; a
// `starship init` line the operator wrote is left alone.
func WithoutPromptBlock(rcContent string) string {
	lines := strings.Split(rcContent, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		if lines[i] != PromptMarker {
			out = append(out, lines[i])
			continue
		}
		if n := len(out); n > 0 && out[n-1] == "" {
			out = out[:n-1]
		}
		if i+1 < len(lines) && strings.Contains(lines[i+1], "starship init") {
			i++
		}
	}
	return strings.Join(out, "\n")
}

func AlreadyConfigured(rcContent, binDir string) bool {
	return strings.Contains(rcContent, Marker) || strings.Contains(rcContent, binDir)
}
