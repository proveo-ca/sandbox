// SPEC: _spec/_runtimes/toolchain-provisioning.puml
package contract_test

import (
	"regexp"
	"strings"
	"testing"
)

// The two Go pins must agree. mise.toml drives `mise run <task>` and go.mod's
// toolchain drives GOTOOLCHAIN=auto, so a drift between them means a contributor
// compiles against one Go and CI against another, over one shared build cache —
// the `compile: version "goX" does not match go tool version "goY"` failure that
// kept Go out of mise.toml in the first place.
func TestMiseGoPinMatchesGoModToolchain(t *testing.T) {
	t.Parallel()

	misePin := regexp.MustCompile(`(?m)^go = "([0-9]+\.[0-9]+\.[0-9]+)"$`).
		FindStringSubmatch(readRepoFile(t, "mise.toml"))
	if misePin == nil {
		t.Fatalf(`mise.toml has no ` + "`" + `go = "X.Y.Z"` + "`" + ` pin under [tools]. Go is provisioned ` +
			`through mise on every backend now, so the host needs the same pin the ` +
			`containers resolve — see _spec/_runtimes/toolchain-provisioning.puml`)
	}

	modPin := regexp.MustCompile(`(?m)^toolchain go([0-9]+\.[0-9]+\.[0-9]+)$`).
		FindStringSubmatch(readRepoFile(t, "go.mod"))
	if modPin == nil {
		t.Fatal("go.mod has no `toolchain goX.Y.Z` line")
	}

	if misePin[1] != modPin[1] {
		t.Errorf("mise.toml pins go %s but go.mod pins toolchain go%s — "+
			"`mise run` and GOTOOLCHAIN=auto would resolve different compilers "+
			"over one build cache", misePin[1], modPin[1])
	}
}

// `g` is retired. It installed fine, including inside an sbx sandbox; what it
// never did was put the toolchain on PATH, and a sandbox has no persistent shell
// rc to make up the difference. Comments are stripped before this check, so the
// notes explaining the retirement are allowed to name it — a live call is not.
func TestNoProvisioningPathShellsOutToG(t *testing.T) {
	t.Parallel()
	src := entrypointLib(t)

	banned := []struct {
		re  *regexp.Regexp
		why string
	}{
		{regexp.MustCompile(`stefanmaric/g`),
			"fetches the `g` installer"},
		{regexp.MustCompile(`(?m)\bg install\b`),
			"invokes `g install`"},
		{regexp.MustCompile(`(?m)^\s*export GOROOT=`),
			"exports GOROOT — an explicit one overrides what the go binary infers " +
				"from its own path, so it aims a mise toolchain at an empty directory"},
	}
	for _, b := range banned {
		if loc := b.re.FindString(src); loc != "" {
			t.Errorf("packages/lib/entrypoint-lib.sh %s (%q). mise is the only Go path — "+
				"see _spec/_runtimes/toolchain-provisioning.puml", b.why, loc)
		}
	}
}

// The replacement is not merely "not `g`" — it has to route through the shared
// mise helper, which is what carries MISE_YES and the bounded timeout.
func TestGoProvisioningRoutesThroughMise(t *testing.T) {
	t.Parallel()
	body := funcBody(t, entrypointLib(t), "_install_go")

	if !strings.Contains(body, `_mise_install "go@${version}"`) {
		t.Errorf("_install_go must provision through _mise_install (it carries MISE_YES "+
			"and the PROVEO_LSP_INSTALL_TIMEOUT bound); body was:\n%s", body)
	}
	// The `g` call ended in `>/dev/null 2>&1`, so its own refusal never reached
	// the operator. Whatever mise says has to survive to the warning.
	if !strings.Contains(body, "${out}") {
		t.Errorf("_install_go must report mise's own output on failure rather than "+
			"discarding it; body was:\n%s", body)
	}
}

// The generated rc block must not pin a GOROOT either: it outlives the toolchain
// it names, so the next `mise use -g go@<other>` leaves the rc pointing at the
// previous install.
func TestPersistedToolEnvCarriesNoGOROOT(t *testing.T) {
	t.Parallel()
	body := funcBody(t, entrypointLib(t), "_proveo_persist_tool_env")

	if strings.Contains(body, "GOROOT") {
		t.Errorf("_proveo_persist_tool_env writes a GOROOT into the agent rc; the mise "+
			"shims already on PATH cover Go, and a written-down GOROOT goes stale on "+
			"the next toolchain change. Body was:\n%s", body)
	}
	if !strings.Contains(body, "shims") {
		t.Errorf("_proveo_persist_tool_env must still persist the mise shims directory — "+
			"that is what puts Go on PATH for the agent. Body was:\n%s", body)
	}
}

// funcBody returns the text of `name() { ... }`, matching braces so a nested
// block does not end it early.
func funcBody(t *testing.T, src, name string) string {
	t.Helper()
	head := name + "() {"
	start := strings.Index(src, head)
	if start < 0 {
		t.Fatalf("%s not found in entrypoint-lib.sh — did it move or get renamed?", name)
	}
	depth := 0
	for i := start + len(head) - 1; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("%s has no closing brace", name)
	return ""
}

// mise picks the interpreter for a task from its shebang, defaulting to `sh`.
// A task written in bash without one parses under dash until it reaches the first
// bashism, so it fails PART WAY THROUGH — test-images died on `tests=(` only after
// staging eight binaries, which reads as a broken test script rather than a wrong
// interpreter.
func TestMiseTasksUsingBashSyntaxDeclareBash(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "mise.toml")

	// Bodies are sliced BETWEEN header positions rather than matched with a
	// non-greedy tail. RE2 has no lookahead, so `(.*?)(?:\n\[|$)` consumes the
	// `\n[` that opens the NEXT header, leaving the scanner mid-token and silently
	// skipping every other task — this guard saw 10 of 20 and passed vacuously.
	header := regexp.MustCompile(`(?m)^\[tasks\.([\w-]+)\]$`)
	locs := header.FindAllStringSubmatchIndex(src, -1)
	if len(locs) == 0 {
		t.Fatal("mise.toml declares no [tasks.*] — did the file move?")
	}
	// Constructs dash cannot parse.
	bashisms := map[string]string{
		"=(":  "array assignment",
		"[[":  "[[ ... ]] test",
		"<(":  "process substitution",
		"${!": "indirect expansion",
	}

	for i, loc := range locs {
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		name := src[loc[2]:loc[3]]
		_, run, ok := strings.Cut(src[loc[1]:end], `run = """`)
		if !ok {
			continue
		}
		run, _, _ = strings.Cut(run, `"""`)
		if strings.HasPrefix(strings.TrimLeft(run, "\n"), "#!") {
			continue
		}
		for frag, what := range bashisms {
			if strings.Contains(run, frag) {
				t.Errorf("mise task %q uses a bash %s (%q) but declares no shebang, so mise "+
					"runs it under sh and it dies part way through. Add `#!/usr/bin/env bash` "+
					"as the first line of the run block.", name, what, frag)
			}
		}
	}
}
