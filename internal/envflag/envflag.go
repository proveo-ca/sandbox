// SPEC: _spec/internal/envflag/env-switches.puml
// Package envflag reads on/off environment switches with one vocabulary.
package envflag

import "strings"

// On reports whether v spells on: 1, true, yes, on, enable, enabled.
func On(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "enable", "enabled":
		return true
	}
	return false
}

// Off reports whether v spells off: 0, false, no, off, disable, disabled.
func Off(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off", "disable", "disabled":
		return true
	}
	return false
}

// Bool is true for On(v), false for Off(v), else def.
func Bool(v string, def bool) bool {
	switch {
	case On(v):
		return true
	case Off(v):
		return false
	}
	return def
}
