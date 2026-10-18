// SPEC: _spec/tests/00-testing-overview.puml

// Package testimage reads the PROVEO_TEST_<TARGET>_IMAGE family that pins the image a test runs against.
package testimage

import (
	"os"
	"strings"
)

// Var names target's knob: PROVEO_TEST_<TARGET>_IMAGE, TARGET the imagebuild target upper-snake-cased.
func Var(target string) string {
	return "PROVEO_TEST_" + strings.ToUpper(strings.ReplaceAll(target, "-", "_")) + "_IMAGE"
}

// Ref returns the image Var(target) pins, else def.
func Ref(target, def string) string {
	if v := strings.TrimSpace(os.Getenv(Var(target))); v != "" {
		return v
	}
	return def
}
