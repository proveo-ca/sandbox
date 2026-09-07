package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// SPEC: _spec/_devops/sandbox-template-rebase.puml
func TestBaseExtendsASandboxTemplate(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/Dockerfile"))

	arg := regexp.MustCompile(`(?m)^ARG SANDBOX_TEMPLATE=(\S+)`).FindStringSubmatch(src)
	if arg == nil {
		t.Fatal("defs/base names no SANDBOX_TEMPLATE — the base image is the one place " +
			"the supported lineage is declared")
	}
	ref := arg[1]
	if !strings.HasPrefix(ref, "docker/sandbox-templates:") {
		t.Fatalf("base is %q: custom templates must extend a built-in agent environment, "+
			"not a bare distro image", ref)
	}
	if !regexp.MustCompile(`^ARG SANDBOX_TEMPLATE=\S+$`).MatchString(arg[0]) {
		t.Fatalf("malformed template pin: %q", arg[0])
	}
	if !regexp.MustCompile(`(?m)^FROM \$\{SANDBOX_TEMPLATE\}`).MatchString(src) {
		t.Error("SANDBOX_TEMPLATE is declared but no FROM line builds from it " +
			"(a mention inside a comment does not count)")
	}

	// SPEC: _spec/_devops/sandbox-template-rebase.puml
	argAt := strings.Index(src, arg[0])
	firstFROM := regexp.MustCompile(`(?m)^FROM `).FindStringIndex(src)
	if firstFROM == nil {
		t.Fatal("defs/base/Dockerfile has no FROM at all")
	}
	if argAt > firstFROM[0] {
		t.Errorf("ARG SANDBOX_TEMPLATE is declared after the first FROM, so it is scoped "+
			"to that stage and resolves EMPTY in `FROM ${SANDBOX_TEMPLATE}` — move it above "+
			"every FROM (arg at byte %d, first FROM at %d)", argAt, firstFROM[0])
	}
}

func TestEveryArgUsedByFromIsGlobal(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{"defs/base/Dockerfile", "defs/base-node/Dockerfile",
		"defs/base-node-lsp/Dockerfile", "defs/base-node-browser/Dockerfile"} {
		src := readFileOrFail(t, filepath.Join(repoRoot(t), rel))
		firstFROM := regexp.MustCompile(`(?m)^FROM `).FindStringIndex(src)
		if firstFROM == nil {
			continue
		}
		for _, m := range regexp.MustCompile(`(?m)^FROM \$\{([A-Za-z_][A-Za-z0-9_]*)\}`).FindAllStringSubmatch(src, -1) {
			name := m[1]
			decl := regexp.MustCompile(`(?m)^ARG ` + regexp.QuoteMeta(name) + `(=|\s*$)`).FindStringIndex(src)
			if decl == nil {
				t.Errorf("%s: FROM interpolates %s but never declares it", rel, name)
				continue
			}
			if decl[0] > firstFROM[0] {
				t.Errorf("%s: ARG %s is declared after the first FROM, so it resolves empty "+
					"in the FROM that uses it", rel, name)
			}
		}
	}
}

func TestBaseTemplateCarriesTheEngine(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/Dockerfile"))
	ref := regexp.MustCompile(`(?m)^ARG SANDBOX_TEMPLATE=(\S+)`).FindStringSubmatch(src)[1]
	tag := ref[strings.LastIndex(ref, ":")+1:]
	if !strings.Contains(tag, "-docker") {
		t.Fatalf("template tag %q is not a -docker variant, so the image has no Engine — "+
			"`docker: sbx` in the manifests would again promise a daemon nothing supplies", tag)
	}
	// A floating tag lets a republished template silently change the libc, the
	// toolchains and the identity of every harness.
	if !regexp.MustCompile(`-\d+\.\d+\.\d+$`).MatchString(tag) {
		t.Errorf("template tag %q is not version-pinned", tag)
	}
}

// SPEC: _spec/_devops/sandbox-template-rebase.puml
func TestHardenPassStripsEverySetuidBinary(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/proveo-harden"))

	for _, must := range []string{"-perm -4000", "-perm -2000", "chmod u-s", "chmod g-s"} {
		if !strings.Contains(src, must) {
			t.Errorf("proveo-harden no longer strips setuid/setgid (%q missing)", must)
		}
	}
	// Any skip-list resurrects the defect three security suites assert against.
	if regexp.MustCompile(`\*/sudo(\.ws)?\)`).MatchString(src) {
		t.Error("proveo-harden exempts sudo again — measured unnecessary (dockerd starts as " +
			"root from init; the socket is reached via the docker group) and it breaks the " +
			"no-setuid-binaries contract in three defs")
	}
	if strings.Contains(src, "continue") {
		t.Error("the stripping loop skips something; the pass is meant to be blanket")
	}
}

func TestBaseAssertsWhatItInherits(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/Dockerfile"))
	for _, probe := range []string{"command -v docker", "command -v dockerd", "-perm -4000"} {
		if !strings.Contains(src, probe) {
			t.Errorf("base does not verify %q at build time", probe)
		}
	}
}

// SPEC: _spec/_devops/sandbox-template-rebase.puml
func TestBaseDoesNotInheritTheTemplateWorkdir(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/Dockerfile"))

	loc := regexp.MustCompile(`(?m)^FROM \$\{SANDBOX_TEMPLATE\}`).FindStringIndex(src)
	if loc == nil {
		t.Fatal("no runtime stage building FROM ${SANDBOX_TEMPLATE}")
	}
	runtime := src[loc[0]:]
	if !regexp.MustCompile(`(?m)^WORKDIR `).MatchString(runtime) {
		t.Fatal("the runtime stage declares no WORKDIR, so it inherits the template's " +
			"/home/agent/workspace — which BuildKit recreates under a home the defs rename")
	}
	if regexp.MustCompile(`(?m)^WORKDIR /home/agent`).MatchString(runtime) {
		t.Error("the base workdir sits inside /home/agent, the very directory the defs move")
	}
}

// Every def that moves uid 1000's home must tolerate an empty stub at the
// destination, because an inherited workdir can put one there between steps.
func TestHomeMoveToleratesARecreatedStub(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{"defs/cecli/Dockerfile", "defs/claudecode/mcp/Dockerfile",
		"defs/cursor/Dockerfile", "defs/opencode/Dockerfile"} {
		src := readFileOrFail(t, filepath.Join(repoRoot(t), rel))
		if !strings.Contains(src, "usermod -d /home/agent -m") {
			continue
		}
		if !strings.Contains(src, "rmdir /home/agent") {
			t.Errorf("%s moves the home without clearing an empty stub first — usermod "+
				"refuses a destination that exists", rel)
		}
		// rmdir, never rm -rf: a stub is empty, real content must fail loudly.
		if regexp.MustCompile(`rm -rf /home/agent(\s|$)`).MatchString(src) {
			t.Errorf("%s clears /home/agent with rm -rf, which would silently discard a "+
				"real home; rmdir removes only an empty stub", rel)
		}
	}
}

func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// SPEC: _spec/_devops/release-gate.puml
func TestLadderAllDetectsOnlyATopLevelSkip(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "mise.toml"))
	if !strings.Contains(src, `'^--- SKIP: TestSandboxLadder [(]'`) {
		t.Fatal("ladder-all's skip grep is not anchored at column 0 with a bracketed '(' — " +
			"an unanchored pattern matches the indented SUBTEST line and reports a def that " +
			"climbed every rung as not-run")
	}

	// The two shapes it has to tell apart, verbatim from real runs.
	ranButOneRungSkipped := "--- PASS: TestSandboxLadder (168.93s)\n" +
		"    --- SKIP: TestSandboxLadder/2-proveo-browser-image (0.14s)\n"
	neverRan := "    ladder_test.go:314: sbx not available: sbx not on PATH\n" +
		"--- SKIP: TestSandboxLadder (0.00s)\n"

	re := regexp.MustCompile(`(?m)^--- SKIP: TestSandboxLadder [(]`)
	if re.MatchString(ranButOneRungSkipped) {
		t.Error("a def that climbed every rung, with one rung skipped, reads as not-run")
	}
	if !re.MatchString(neverRan) {
		t.Error("a ladder that never ran reads as having run — the gate would certify nothing")
	}
}
