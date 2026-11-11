// SPEC: _spec/internal/promptkit/prompt-kit.puml
package promptkit

import (
	"path/filepath"
	"strings"
)

// The marks that fence proveo's block in starship's config.
const (
	segmentOpen  = "# >>> proveo: what earlier runs left here (managed by `proveo init`) >>>"
	segmentClose = "# <<< proveo <<<"
)

// Segment is the starship module that prints `proveo prompt` inside a repository.
// `when = true` because a custom module defaults to never showing; the format hides
// the module when the command prints nothing.
const Segment = segmentOpen + `
[custom.proveo]
command = "proveo prompt"
when = true
require_repo = true
format = "[($output )]($style)"
style = "bold yellow"
description = "sandboxes proveo kept and work it carried home"
` + segmentClose + "\n"

// ConfigPath is starship's config file: STARSHIP_CONFIG, else ~/.config/starship.toml.
func ConfigPath(home string, getenv func(string) string) string {
	if p := strings.TrimSpace(getenv("STARSHIP_CONFIG")); p != "" {
		return p
	}
	if x := strings.TrimSpace(getenv("XDG_CONFIG_HOME")); x != "" {
		return filepath.Join(x, "starship.toml")
	}
	return filepath.Join(home, ".config", "starship.toml")
}

// WithSegment is config with proveo's block in it, once: an existing block is
// replaced in place, a missing one is appended. Nothing outside the marks changes.
func WithSegment(config string) string {
	if before, rest, found := strings.Cut(config, segmentOpen); found {
		if _, after, closed := strings.Cut(rest, segmentClose); closed {
			return before + strings.TrimSuffix(Segment, "\n") + after
		}
	}
	if config != "" && !strings.HasSuffix(config, "\n") {
		config += "\n"
	}
	if config != "" {
		config += "\n"
	}
	return config + Segment
}

// WithoutSegment is config with proveo's block, and the blank line before it, removed.
func WithoutSegment(config string) string {
	before, rest, found := strings.Cut(config, segmentOpen)
	if !found {
		return config
	}
	_, after, closed := strings.Cut(rest, segmentClose)
	if !closed {
		return config
	}
	before = strings.TrimRight(before, "\n")
	if before != "" {
		before += "\n"
	}
	return before + strings.TrimPrefix(after, "\n")
}
