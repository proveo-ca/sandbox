package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The retirement, asserted rather than assumed. proveo no longer chooses an
// agent's model: there are no bridge tables, no role-var reads, and no def
// entrypoint that maps one onto the variable its harness reads. An agent's
// model comes from its own saved config, and on a first run from its own
// default. SPEC: _spec/_plans/retire-model-bridging.puml
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestNoBridgeTablesRemain(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "..", "defs", "bridges")); !os.IsNotExist(err) {
		t.Error("defs/bridges/ is back. It supplied a default, a normalisation and a " +
			"provider pin as well as the role mapping, so a file that reappears for one " +
			"of those quietly restores all four.")
	}
}

// The shell half. A def entrypoint may still set a model for a LOCAL endpoint
// (PROVEO_LOCAL_MODEL is deliberately out of scope), but nothing may map
// proveo's role names onto a harness variable.
func TestNoEntrypointBridgesAModelByRole(t *testing.T) {
	for _, rel := range []string{
		"packages/lib/entrypoint-lib.sh",
		"defs/cecli/entrypoint.sh",
		"defs/cursor/entrypoint.sh",
		"defs/opencode/entrypoint.sh",
		"defs/claudecode/mcp/entrypoint.sh",
	} {
		src := repoFile(t, rel)
		for _, banned := range []string{"apply_model_bridges", "_apply_model_bridge", "/opt/proveo/bridges"} {
			if strings.Contains(src, banned) {
				t.Errorf("%s still references %q — the model bridging is retired", rel, banned)
			}
		}
	}
}

// The credential bridges are a DIFFERENT mechanism that happens to share a
// naming convention, and deleting the model half once took them with it.
func TestCredentialBridgesSurvivedTheRetirement(t *testing.T) {
	src := repoFile(t, "packages/lib/entrypoint-lib.sh")
	for _, want := range []string{"_apply_env_bridge()", "apply_env_bridges()", "GOOGLE_GENERATIVE_AI_API_KEY"} {
		if !strings.Contains(src, want) {
			t.Errorf("entrypoint-lib.sh lost %q. GEMINI_API_KEY -> GOOGLE_GENERATIVE_AI_API_KEY "+
				"bridges a CREDENTIAL, not a model, and it is what the Go/bash parity test "+
				"compares; removing it silently unauthenticates Gemini.", want)
		}
	}
}

// A def may not hand-roll what the tables used to do.
//
// Matched on a WORD BOUNDARY, and that is the whole difficulty: an agent's own
// variables are named after the same roles — CECLI_EDITOR_MODEL,
// OPENCODE_SMALL_MODEL — and those are exactly what SHOULD remain. A substring
// test flags every one of them and would be deleted within a week.
func TestNoEntrypointReadsARoleName(t *testing.T) {
	roles := map[string]*regexp.Regexp{
		"ARCHITECT_MODEL": regexp.MustCompile(`(^|[^0-9A-Za-z_])ARCHITECT_MODEL\b`),
		"EDITOR_MODEL":    regexp.MustCompile(`(^|[^0-9A-Za-z_])EDITOR_MODEL\b`),
		"SMALL_MODEL":     regexp.MustCompile(`(^|[^0-9A-Za-z_])SMALL_MODEL\b`),
	}
	for _, rel := range []string{
		"defs/cecli/entrypoint.sh",
		"defs/cursor/entrypoint.sh",
		"defs/opencode/entrypoint.sh",
		"defs/claudecode/mcp/entrypoint.sh",
	} {
		src := repoFile(t, rel)
		for role, re := range roles {
			if re.MatchString(src) {
				t.Errorf("%s reads %s. proveo does not choose a model; a def that reads a role "+
					"name is the bridging growing back one entrypoint at a time.", rel, role)
			}
		}
	}
}

// The guard on the guard: the boundary must still catch a bare role name, or
// the test above passes by being blind.
func TestTheRoleNameGuardActuallyFires(t *testing.T) {
	re := regexp.MustCompile(`(^|[^0-9A-Za-z_])EDITOR_MODEL\b`)
	if !re.MatchString(`export FOO="${EDITOR_MODEL}"`) {
		t.Error("the boundary pattern misses a bare role read")
	}
	if re.MatchString(`export CECLI_EDITOR_MODEL="x"`) {
		t.Error("the boundary pattern flags an agent's OWN variable, which must survive")
	}
}
