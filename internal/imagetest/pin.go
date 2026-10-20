// SPEC: _spec/tests/00-testing-overview.puml
package imagetest

import (
	"os"
	"strings"
)

// PinVar names target's PROVEO_TEST_<TARGET>_IMAGE knob, TARGET the imagebuild target upper-snake-cased.
func PinVar(target string) string {
	return "PROVEO_TEST_" + strings.ToUpper(strings.ReplaceAll(target, "-", "_")) + "_IMAGE"
}

// PinnedRef returns the image PinVar(target) pins, else def.
func PinnedRef(target, def string) string {
	if v := strings.TrimSpace(os.Getenv(PinVar(target))); v != "" {
		return v
	}
	return def
}
