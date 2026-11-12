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
	"time"

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
			"ladder-all would pass a ladder that skipped on a missing docker or sbx")
	}

	idle := topFunc(t, "e2e/idle_test.go", "idleAtPrompt").Body.List
	if len(idle) == 0 || !isGateCall(idle[0], "PROVEO_IDLE_TEST") {
		t.Error(`idleAtPrompt's first statement is not failSkipsWhenOptedIn(t, "PROVEO_IDLE_TEST"); ` +
			"idle-all would pass a def whose sbx, image or credential was missing")
	}
}

var idleForDefault = regexp.MustCompile(`^\{% set idle = get_env\(name='PROVEO_IDLE_FOR', default=''\) \| trim %\}` +
	`\{% if idle %\}\{\{ idle \}\}\{% else %\}([^{}]+)\{% endif %\}$`)

func TestE2EFullIsTheSilentDeathGate(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	full := cfg.task(t, "test-e2e-full")
	if !slices.Contains(full.list("depends"), "test-images") {
		t.Error("test-e2e-full must depend on test-images")
	}
	if runs := full.cmds(); len(full.run()) != 1 || len(runs) != 1 ||
		!strings.HasPrefix(runs[0], "mise run --continue-on-error e2e-gated") ||
		!strings.HasSuffix(runs[0], " ::: idle-all ::: ladder-all") {
		t.Errorf("test-e2e-full runs %q, want `mise run --continue-on-error e2e-gated … ::: idle-all ::: ladder-all`: "+
			"a red half must not stop the next", runs)
	}

	gated := cfg.task(t, "e2e-gated")
	for _, k := range []string{"PROVEO_IDLE_TEST", "PROVEO_TOOLCHAIN_TEST"} {
		if v := gated.env()[k]; v != "1" {
			t.Errorf("e2e-gated sets %s=%q, want 1", k, v)
		}
	}
	wantGated := "go test -tags=e2e ./e2e/ -count=1 -timeout " + timeoutArg("150m")
	if got := gated.cmds(); len(gated.run()) != 1 || len(got) != 1 || got[0] != wantGated {
		t.Errorf("e2e-gated runs %q, want %q", got, wantGated)
	}

	idleAll := cfg.task(t, "idle-all")
	if !slices.Contains(idleAll.list("wait_for"), "e2e-gated") {
		t.Error("idle-all must wait for e2e-gated")
	}
	env := idleAll.env()
	if env["PROVEO_IDLE_TEST"] != "1" || env["PROVEO_IDLE_TARGETS"] != "claudecode,cursor,opencode" {
		t.Errorf("idle-all env = %q, want PROVEO_IDLE_TEST=1 and PROVEO_IDLE_TARGETS=claudecode,cursor,opencode — "+
			"a per-def death would not run", env)
	}
	if m := idleForDefault.FindStringSubmatch(env["PROVEO_IDLE_FOR"]); m == nil {
		t.Errorf("idle-all PROVEO_IDLE_FOR = %q, want the operator's value, or a fallback when it is unset or blank", env["PROVEO_IDLE_FOR"])
	} else if d, err := time.ParseDuration(m[1]); err != nil || d <= 30*time.Second {
		t.Errorf("idle-all idles %q by default, want over 30s: the silent death lands past 30s", m[1])
	}
	if runs := idleAll.cmds(); len(runs) != 1 || !strings.Contains(runs[0], " -run=^TestAgentSurvivesIdleAtPrompt$ ") ||
		!strings.HasPrefix(runs[0], "go test -tags=e2e ") {
		t.Errorf("idle-all runs %q, want go test -tags=e2e … -run=^TestAgentSurvivesIdleAtPrompt$", runs)
	}
	if !slices.Contains(cfg.task(t, "ladder-all").list("wait_for"), "idle-all") {
		t.Error("ladder-all must wait for idle-all")
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

func TestLadderTasksClimbEveryDefInItsOwnProcess(t *testing.T) {
	t.Parallel()
	cfg := readMise(t)
	defs := []string{"claudecode", "codex", "cursor", "opencode"}
	want := "mise run --continue-on-error --jobs=1 ladder:claudecode ::: ladder:codex ::: ladder:cursor ::: ladder:opencode"
	all := cfg.task(t, "ladder-all")
	if got := all.cmds(); len(all.run()) != 1 || len(got) != 1 || got[0] != want {
		t.Errorf("ladder-all runs %q, want %q: one def at a time, in order, past a red one", got, want)
	}
	var declared []string
	for name := range cfg.tasks {
		if def, ok := strings.CutPrefix(name, "ladder:"); ok {
			declared = append(declared, def)
		}
	}
	slices.Sort(declared)
	if !slices.Equal(declared, defs) {
		t.Errorf("mise.toml declares ladder tasks for %q, want %q", declared, defs)
	}
	climb := "go test -tags=e2e -count=1 -timeout 30m ./e2e/ -run=^TestSandboxLadder$ -v"
	for _, def := range defs {
		task := cfg.task(t, "ladder:"+def)
		if env := task.env(); env["PROVEO_LADDER_TEST"] != "1" || env["PROVEO_LADDER_TARGET"] != def || len(env) != 2 {
			t.Errorf("ladder:%s env = %q, want PROVEO_LADDER_TEST=1 and PROVEO_LADDER_TARGET=%s", def, env, def)
		}
		if got := task.cmds(); len(task.run()) != 1 || len(got) != 1 || got[0] != climb {
			t.Errorf("ladder:%s runs %q, want %q: its own 30m process", def, got, climb)
		}
	}
}

// imageTasks are the tasks that reach the fleet main, each with its exact run line.
var imageTasks = map[string]string{
	"build": "go run ./internal/maintain/fleet -tag={{ usage.tag | quote }} {% if usage.no_cache %}-no-cache {% endif %}" +
		"{% if usage.print %}-print {% endif %}build {{ usage.target | quote }}",
	"deploy": "go run ./internal/maintain/fleet -tag={{ usage.tag | quote }} {% if usage.print %}-print {% endif %}" +
		"deploy {{ usage.target | quote }}",
	"test-defs": "go run ./internal/maintain/fleet {% if usage.print %}-print {% endif %}test {{ usage.target | quote }}",
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
		for _, line := range []string{`arg "<target>"`, `flag "--print"`} {
			if !declares(name, line) {
				t.Errorf("%s's usage lacks %s", name, line)
			}
		}
	}
	for name, line := range map[string]string{
		"build":  `flag "--tag <tag>" default="local"`,
		"deploy": `flag "--tag <tag>" default="latest"`,
	} {
		if !declares(name, line) {
			t.Errorf("%s's usage lacks %s", name, line)
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
	cfg := readMise(t)
	var image, entries []string
	for name, task := range cfg.tasks {
		for _, cmd := range task.cmds() {
			if strings.HasPrefix(cmd, "go test ") && strings.Contains(cmd, "./internal/imagetest/") {
				image = append(image, name)
				entries = append(entries, cmd)
			}
		}
	}
	if len(image) != 1 || image[0] != "test-images" {
		t.Fatalf("mise.toml runs the image suites from tasks %v, want test-images alone", image)
	}
	if narrowsTests.MatchString(entries[0]) {
		t.Errorf("test-images narrows the image suites with -run (%q); every TestImage* must run, hermes included", entries[0])
	}
	for _, gate := range []string{"test-e2e", "test-e2e-full"} {
		if !slices.Contains(cfg.task(t, gate).list("depends"), "test-images") {
			t.Errorf("%s must depend on test-images", gate)
		}
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
