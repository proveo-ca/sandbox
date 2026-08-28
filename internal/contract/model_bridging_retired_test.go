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

// shellCode is repoFile minus its comment lines.
//
// The guards below have to be comment-BLIND or comment-AWARE, and blind is what
// they were. This repo's house style is to name the deleted machinery in prose
// exactly where it used to live — the plan does it, model-alias-bridges.puml
// does it, and the entrypoint that lost apply_model_bridges says so. A guard
// that greps the whole file makes that habit unspellable and fires on the
// explanation instead of the code, which is how a guard gets deleted.
func shellCode(t *testing.T, rel string) string {
	t.Helper()
	var keep []string
	for _, line := range strings.Split(repoFile(t, rel), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
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
		"defs/codex/entrypoint.sh",
		"defs/cursor/entrypoint.sh",
		"defs/opencode/entrypoint.sh",
		"defs/claudecode/mcp/entrypoint.sh",
	} {
		src := shellCode(t, rel)
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
		"defs/codex/entrypoint.sh",
		"defs/cursor/entrypoint.sh",
		"defs/opencode/entrypoint.sh",
		"defs/claudecode/mcp/entrypoint.sh",
	} {
		src := shellCode(t, rel)
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

// THE SEED MAY NOT NAME A MODEL BY ENVIRONMENT. This is the defect the
// retirement actually shipped: opencode's seeds pinned every model slot to
// "{env:OPENCODE_MODEL}", the bridge tables were what filled it, and opencode
// substitutes an UNSET {env:...} with the EMPTY STRING. So the run after the
// deletion wrote model="" into the DURABLE HOME, where it outlives the session —
// which is the opposite of "the agent's own default applies".
//
// Checked in Go as well as in defs/opencode/tests, because the shell suite needs
// a built image and this needs to fail in `go test ./...`.
// SPEC: _spec/_plans/retire-model-bridging.puml
func TestNoOpencodeSeedNamesAModelByEnvironment(t *testing.T) {
	for _, rel := range []string{
		"defs/opencode/sample_opencode.json",
		"defs/opencode/defaults/opencode.json",
		"defs/opencode/entrypoint.sh",
	} {
		if strings.Contains(shellCode(t, rel), "{env:OPENCODE") {
			t.Errorf("%s interpolates a model variable. Nothing writes OPENCODE_MODEL any "+
				"more, and opencode resolves an unset {env:...} to the empty string — so this "+
				"does not fall back to the agent's default, it pins the model to nothing.", rel)
		}
	}
}

// --local-model IS THE ONE CALLER THAT STILL CHOOSES, and opencode is the one
// harness that needed the bridge to do it. cecli sets CECLI_MODEL from
// PROVEO_LOCAL_MODEL itself and claudecode gets ANTHROPIC_MODEL from `docker -e`
// (internal/egress/plan.go); opencode's entrypoint registered the ollama PROVIDER
// but never selected the model, because apply_model_bridges did. Q2 is deferred,
// so this path must keep working.
func TestLocalModelStillSelectsAModelForOpencode(t *testing.T) {
	src := repoFile(t, "defs/opencode/entrypoint.sh")
	if !strings.Contains(src, `.model = ("ollama/" + $model)`) {
		t.Error("defs/opencode/entrypoint.sh registers the ollama provider without selecting " +
			"the model. Registering makes the id available; something must still choose it, " +
			"and with the bridging gone nothing else does — --local-model silently runs on " +
			"whatever opencode picks.")
	}
}

// The three harnesses whose local-model tier the e2e asserts, each naming its
// model by a route that does NOT read a role name. e2e/hello_world_test.go's
// assertModels compares both PROVEO_MODELS tiers against --local-model for all
// three, so a harness that names nothing fails there — 8 minutes into a docker
// run. This says it in a second.
func TestEveryHarnessNamesItsLocalModelWithoutARoleName(t *testing.T) {
	for _, c := range []struct{ rel, want string }{
		{"defs/cecli/entrypoint.sh", `CECLI_MODEL="openai/${PROVEO_LOCAL_MODEL}"`},
		{"defs/opencode/entrypoint.sh", `OPENCODE_MODEL="ollama/$model"`},
		{"internal/egress/plan.go", `"-e", "ANTHROPIC_MODEL=" + model`},
	} {
		if !strings.Contains(repoFile(t, c.rel), c.want) {
			t.Errorf("%s no longer names its local model (%s). --local-model is the one caller "+
				"that still chooses, and each harness now has to do it for itself.", c.rel, c.want)
		}
	}
}

// The DOC is how the bridging comes back. CODING_HARNESSES.md told a contributor
// to bridge these three names into tool-specific vars; a doc that still says so
// is a standing instruction to re-add what was deleted.
func TestTheHarnessDocDoesNotAskForRoleBridging(t *testing.T) {
	src := repoFile(t, "CODING_HARNESSES.md")
	for _, banned := range []string{"`ARCHITECT_MODEL`", "`EDITOR_MODEL`", "`SMALL_MODEL`"} {
		if strings.Contains(src, banned) {
			t.Errorf("CODING_HARNESSES.md still documents %s as a name an entrypoint should "+
				"bridge. proveo does not choose an agent's model; the doc is the next "+
				"contributor's spec.", banned)
		}
	}
}

// THE DEF SUITES ASSERT THE RETIREMENT, NOT THE BRIDGE. Two of them asserted the
// bridge — that ARCHITECT_MODEL became OPENCODE_MODEL, that it became CURSOR_MODEL
// — and because a shell suite needs a built image, neither failed in
// `go test ./...`. They would have failed on the next image run instead, with the
// deletion long since merged.
//
// Pinned by naming the REPLACEMENT assertions rather than by banning the role
// names, because a suite proving a name goes nowhere has to say the name. That
// distinction is not one a regex over these files can draw, and a guard that
// cannot draw it flags the correct test and gets deleted.
func TestTheDefSuitesAssertTheRetirementRatherThanTheBridge(t *testing.T) {
	for _, c := range []struct{ rel, want, why string }{
		{"defs/opencode/tests/test_config.sh", `SAW OPENCODE_MODEL=\[\]`,
			"that a role name in .env reaches opencode as nothing"},
		{"defs/opencode/tests/test_config.sh", `SAW OPENCODE_SMALL_MODEL=\[xai/grok-4.3\]`,
			"that opencode's OWN variable still survives load_env"},
		{"defs/cursor/tests/test_config.sh", `PASSED_MODEL=explicit-model`,
			"that cursor's OWN CURSOR_MODEL still reaches --model"},
		{"defs/cursor/tests/test_config.sh", `no role name in .env reaches cursor's --model`,
			"that a role name never becomes cursor's --model"},
	} {
		if !strings.Contains(repoFile(t, c.rel), c.want) {
			t.Errorf("%s no longer asserts %s (looked for %q). The image suites are the only "+
				"place the retirement is checked end to end.", c.rel, c.why, c.want)
		}
	}
}
