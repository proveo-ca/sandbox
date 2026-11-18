// SPEC: _spec/_devops/sandbox-template-rebase.puml, _spec/_devops/release-gate.puml
package contract_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/maintain"
	"github.com/proveo-ca/proveo/internal/manifest"
)

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
		"defs/base-node-browser/Dockerfile"} {
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
			"every manifest without `daemon: false` would promise a daemon nothing supplies", tag)
	}
	// A floating tag lets a republished template silently change the libc, the
	// toolchains and the identity of every harness.
	if !regexp.MustCompile(`-\d+\.\d+\.\d+$`).MatchString(tag) {
		t.Errorf("template tag %q is not version-pinned", tag)
	}
}

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
	for _, rel := range []string{"defs/claudecode/mcp/Dockerfile",
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

// topFunc parses the repo Go file rel and returns its top-level func name.
func topFunc(t *testing.T, rel, name string) *ast.FuncDecl {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), rel, readRepoFile(t, rel), parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("%s declares no func %s", rel, name)
	return nil
}

// selectorCalls lists every x.M(…) call under n as "x.M".
func selectorCalls(n ast.Node) []string {
	var out []string
	ast.Inspect(n, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok {
					out = append(out, x.Name+"."+sel.Sel.Name)
				}
			}
		}
		return true
	})
	return out
}

// isGateCall reports whether stmt is failSkipsWhenOptedIn(t, "<optIn>").
func isGateCall(stmt ast.Stmt, optIn string) bool {
	es, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	fn, okFn := call.Fun.(*ast.Ident)
	tv, okT := call.Args[0].(*ast.Ident)
	lit, okLit := call.Args[1].(*ast.BasicLit)
	return okFn && fn.Name == "failSkipsWhenOptedIn" && okT && tv.Name == "t" && okLit && lit.Value == strconv.Quote(optIn)
}

func TestOptedInE2EGatesFailTheirSkips(t *testing.T) {
	t.Parallel()
	gate := topFunc(t, "e2e/gate_test.go", "failSkipsWhenOptedIn")
	for _, call := range []string{"envflag.On", "t.Cleanup", "t.Skipped", "t.Errorf"} {
		if !slices.Contains(selectorCalls(gate.Body), call) {
			t.Errorf("e2e/gate_test.go failSkipsWhenOptedIn never calls %s; a skip under an opted-in gate must fail at cleanup", call)
		}
	}

	ladder := topFunc(t, "e2e/ladder_test.go", "TestSandboxLadder").Body.List
	optOut, ok := ladder[0].(*ast.IfStmt)
	var lits []string
	if ok {
		ast.Inspect(optOut.Cond, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok {
				lits = append(lits, lit.Value)
			}
			return true
		})
	}
	if !ok || !slices.Contains(lits, `"PROVEO_LADDER_TEST"`) || !slices.Contains(selectorCalls(optOut.Body), "t.Skip") {
		t.Error("TestSandboxLadder's first statement is not the PROVEO_LADDER_TEST opt-out skip")
	}
	if len(ladder) < 2 || !isGateCall(ladder[1], "PROVEO_LADDER_TEST") {
		t.Error(`TestSandboxLadder's second statement is not failSkipsWhenOptedIn(t, "PROVEO_LADDER_TEST"); ` +
			"test-ladder would pass a ladder that skipped on a missing docker or sbx")
	}

	idle := topFunc(t, "e2e/idle_test.go", "idleAtPrompt").Body.List
	if len(idle) == 0 || !isGateCall(idle[0], "PROVEO_IDLE_TEST") {
		t.Error(`idleAtPrompt's first statement is not failSkipsWhenOptedIn(t, "PROVEO_IDLE_TEST"); ` +
			"test-idle would pass a def whose sbx, image or credential was missing")
	}
}

// e2eRun is the e2e task's run line: every flag reaches the e2e main, and each agent is one quoted word.
const e2eRun = "go run ./internal/maintain/e2e {% if usage.print %}-print {% endif %}{% if usage.idle %}-idle {% endif %}" +
	"{% if usage.ladder %}-ladder {% endif %}{% if usage.toolchain %}-toolchain {% endif %}{% if usage.full %}-full {% endif %}" +
	"{% if usage.no_images %}-no-images {% endif %}{% if usage.run %}-run={{ usage.run | quote }}{% endif %}" +
	"{% for a in usage.agent %} {{ a | quote }}{% endfor %}"

func TestE2EIsTheOneE2ETask(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	e2e := cfg.task(t, "e2e")
	if got := e2e.cmds(); len(e2e.run()) != 1 || len(got) != 1 || got[0] != e2eRun {
		t.Errorf("e2e runs %q, want %q", got, e2eRun)
	}
	usage := e2e.str("usage")
	for _, line := range []string{`arg "[agent]..." var=#true`, `flag "--idle"`, `flag "--ladder"`, `flag "--toolchain"`,
		`flag "--full"`, `flag "--no-images"`, `flag "--run <regex>"`, `flag "--print"`} {
		if !strings.Contains(usage, line) {
			t.Errorf("e2e's usage lacks %s", line)
		}
	}
	if got, want := e2e.choices(), append([]string{"all"}, maintain.E2EAgents...); !slices.Equal(got, want) {
		t.Errorf("e2e offers the agents %q, want %q (all, then maintain.E2EAgents)", got, want)
	}
	if pin := e2e.fields["tools"].table["goreleaser"]; !pin.isStr || pin.str != goreleaserPin {
		t.Errorf("e2e pins tools.goreleaser = %q, want %q: its image gate snapshots the CLI", pin.text(), goreleaserPin)
	}
	for name, task := range cfg.tasks {
		for _, cmd := range task.cmds() {
			if strings.HasPrefix(cmd, "go test ") && (strings.Contains(cmd, "./e2e/") || strings.Contains(cmd, "./internal/imagetest/")) {
				t.Errorf("%s runs %q; the e2e and image suites start from the e2e and fleet mains alone", name, cmd)
			}
		}
		if strings.HasPrefix(name, "ladder:") || name == "e2e-gated" {
			t.Errorf("mise.toml declares %s; mise run e2e --ladder and --full plan those passes in Go", name)
		}
	}

	idle := readRepoFile(t, "e2e/idle_test.go")
	for _, needle := range []string{
		"idleWorkspace",
		"injectTerminalReports",
		`\x1b[?6c`,
		"go.mod",
		"package.json",
	} {
		if !strings.Contains(idle, needle) {
			t.Errorf("idle_test.go is missing %q — an empty TempDir and a PTY that never answers DA1 is how this gate passed while proveo run cursor died", needle)
		}
	}
}

// imageTasks are the tasks that reach the fleet main, each with its exact run line.
var imageTasks = map[string]string{
	"build": "go run ./internal/maintain/fleet -tag={{ usage.tag | quote }} {% if usage.no_cache %}-no-cache {% endif %}" +
		"{% if usage.print %}-print {% endif %}{% if usage.cli %}-cli {% endif %}build{% if usage.target %} {{ usage.target | quote }}{% endif %}",
	"deploy": "go run ./internal/maintain/fleet -tag={{ usage.tag | quote }} {% if usage.print %}-print {% endif %}" +
		"{% if usage.cli %}-cli -cli-version={{ usage.cli_version | quote }} {% endif %}{% if usage.yes %}-yes {% endif %}" +
		"deploy{% if usage.target %} {{ usage.target | quote }}{% endif %}",
	"test-images": "go run ./internal/maintain/fleet {% if usage.print %}-print {% endif %}test{% if usage.target %} {{ usage.target | quote }}{% endif %}",
}

func TestImageTasksChoicesMatchTheRegistry(t *testing.T) {
	t.Parallel()
	defs := filepath.Join(repoRoot(t), "defs")
	ms, err := manifest.Load(defs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"all"}
	for _, target := range maintain.Registry(ms, defs) {
		want = append(want, target.Name)
	}
	cfg := readMise(t)
	for name := range imageTasks {
		if got := cfg.task(t, name).choices(); !slices.Equal(got, want) {
			t.Errorf("%s offers the targets %q, want %q (all, then maintain.Registry in order)", name, got, want)
		}
	}
}

func TestImageTasksRunTheFleetMain(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	declares := func(name, line string) bool {
		return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + `( |$)`).MatchString(cfg.task(t, name).str("usage"))
	}
	for name, want := range imageTasks {
		task := cfg.task(t, name)
		if got := task.cmds(); len(task.run()) != 1 || len(got) != 1 || got[0] != want {
			t.Errorf("%s runs %q, want %q", name, got, want)
		}
		if !declares(name, `flag "--print"`) {
			t.Errorf(`%s's usage lacks flag "--print"`, name)
		}
	}
	for name, lines := range map[string][]string{
		"build":       {`arg "[target]"`, `flag "--cli"`, `flag "--tag <tag>" default="local"`},
		"deploy":      {`arg "[target]"`, `flag "--cli"`, `flag "--cli-version <version>" default="patch"`, `flag "--yes"`, `flag "--tag <tag>" default="latest"`},
		"test-images": {`arg "[target]"`},
	} {
		for _, line := range lines {
			if !declares(name, line) {
				t.Errorf("%s's usage lacks %s", name, line)
			}
		}
	}
	if !declares("build", `flag "--no-cache"`) {
		t.Error(`build's usage lacks flag "--no-cache"`)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", readRepoFile(t, "internal/maintain/fleet/main.go"), parser.PackageClauseOnly)
	if err != nil || f.Name.Name != "main" {
		t.Errorf("internal/maintain/fleet/main.go is not a package main (%v)", err)
	}
}

var narrowsTests = regexp.MustCompile(`(^|\s)'?-(test\.)?run\b`)

func TestOneImageTaskGatesBothReleaseGates(t *testing.T) {
	t.Parallel()
	var image []string
	for _, c := range maintain.ImageGatePlan() {
		if cmd := strings.Join(c.Argv, " "); strings.HasPrefix(cmd, "go test ") && strings.Contains(cmd, "./internal/imagetest/") {
			image = append(image, cmd)
		}
	}
	if len(image) != 1 {
		t.Fatalf("the image gate runs the image suites %d times (%q), want once", len(image), image)
	}
	if narrowsTests.MatchString(image[0]) {
		t.Errorf("the image gate narrows the image suites with -run (%q); every TestImage* must run, hermes included", image[0])
	}
	main := readRepoFile(t, "internal/maintain/e2e/main.go")
	if !strings.Contains(main, "if !*noImages {") || !strings.Contains(main, "maintain.RunImageGate(") {
		t.Error("the e2e main no longer runs the image gate unless -no-images")
	}
}

func TestBaseInstallsFfmpegForEveryHarness(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/Dockerfile"))
	apt := regexp.MustCompile(`(?s)apt-get install -y --no-install-recommends \\(.*?)&&`).FindStringSubmatch(src)
	if apt == nil || !regexp.MustCompile(`\bffmpeg\b`).MatchString(apt[1]) {
		t.Error("defs/base/Dockerfile apt layer lacks ffmpeg; every harness inherits its media tools from base")
	}
}

func TestBaseShipsAsOneLayerThatStillReadsAsASandboxTemplate(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/Dockerfile"))
	froms := regexp.MustCompile(`(?m)^FROM .*$`).FindAllStringIndex(src, -1)
	if len(froms) == 0 {
		t.Fatal("defs/base/Dockerfile has no FROM")
	}
	last := src[froms[len(froms)-1][0]:]
	if !strings.HasPrefix(last, "FROM scratch\n") {
		t.Fatalf("the final stage is %q; want FROM scratch so the base is one layer (sbx 0.49.2 hangs at 45 layers)",
			strings.SplitN(last, "\n", 2)[0])
	}
	if !regexp.MustCompile(`(?m)^FROM \$\{SANDBOX_TEMPLATE\} AS base-full$`).MatchString(src) {
		t.Error("the template stage must be named base-full for the flat stage to copy from")
	}
	for _, want := range []string{
		"COPY --from=base-full / /",
		"com.docker.sandboxes=templates", "com.docker.sandboxes.flavor=shell-docker",
		"com.docker.sandboxes.start-docker=true", `proveo.base.template="${SANDBOX_TEMPLATE}"`,
		"USER root", "WORKDIR /", `ENTRYPOINT ["tini", "--"]`, `CMD ["bash"]`, "BASH_ENV=", "PATH=",
	} {
		if !strings.Contains(last, want) {
			t.Errorf("the flat stage lacks %q; FROM scratch drops every config the template set", want)
		}
	}
}

func TestTheBasePointsClaudeAtTheFileSbxPersistsEnvIn(t *testing.T) {
	t.Parallel()
	src := readFileOrFail(t, filepath.Join(repoRoot(t), "defs/base/Dockerfile"))
	froms := regexp.MustCompile(`(?m)^FROM .*$`).FindAllStringIndex(src, -1)
	full, flat := src[:froms[len(froms)-1][0]], src[froms[len(froms)-1][0]:]
	for stage, body := range map[string]string{"base-full": full, "the flat stage": flat} {
		if !strings.Contains(body, "CLAUDE_ENV_FILE=/etc/sandbox-persistent.sh") {
			t.Errorf("%s lacks CLAUDE_ENV_FILE=/etc/sandbox-persistent.sh; the published claude kit's context tells the agent to persist env there", stage)
		}
	}
}
