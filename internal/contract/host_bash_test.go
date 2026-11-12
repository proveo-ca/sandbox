// SPEC: _spec/tests/20-contract.puml
package contract_test

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// hostShellAllowed is every tracked shell file the operator's Mac may execute.
var hostShellAllowed = map[string]string{
	"apps/cli/public/cli/install.sh":   "bootstraps proveo before it exists; guarded by installer_bash_test.go",
	"apps/cli/public/cli/uninstall.sh": "removes proveo; guarded by installer_bash_test.go",
}

// inImageOrFixture are trees whose shell runs inside an image (bash 5) or is a
// fixture workspace an agent operates on, never the host.
var inImageOrFixture = []string{"defs/", "packages/lib/", "e2e/testdata/", "e2e/samples/"}

var shellShebang = regexp.MustCompile(`^#!\S*(/|env\s+)(ba|da|z)?sh(\s|$)`)

func TestNoHostShellOutsideTheAllowlist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	seen := map[string]bool{}
	for _, rel := range trackedFiles(t) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		first, _, _ := bytes.Cut(b, []byte("\n"))
		if !strings.HasSuffix(rel, ".sh") && !shellShebang.Match(first) {
			continue
		}
		if _, ok := hostShellAllowed[rel]; ok {
			seen[rel] = true
			continue
		}
		inside := false
		for _, prefix := range inImageOrFixture {
			inside = inside || strings.HasPrefix(rel, prefix)
		}
		if !inside {
			t.Errorf("%s is a host shell script; macOS runs it under /bin/bash 3.2. "+
				"Port it to a mise task (plain argv), a Go test, a package main under internal/, or cmd/proveo, "+
				"or justify it in hostShellAllowed", rel)
		}
	}
	for rel := range hostShellAllowed {
		if !seen[rel] {
			t.Errorf("hostShellAllowed lists %s, which is not a tracked shell file — drop the entry", rel)
		}
	}
}

const plainArgv = "a mise run string is plain argv: put logic in a Go test or a package main under internal/, " +
	"and env in the task's env table"

var (
	teraValue     = regexp.MustCompile(`(?s)\{\{(.*?)\}\}`)
	teraBlock     = regexp.MustCompile(`(?s)\{\{.*?\}\}|\{%.*?%\}|\{#.*?#\}`)
	quotedValue   = regexp.MustCompile(`(?s)^(.*?)\s*\|\s*quote$`)
	teraPath      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	quotedArgv    = regexp.MustCompile(`'[^']*'`)
	leadingAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	// shellChars names the sh syntax each byte outside argvByte starts.
	shellChars = map[byte]string{
		'\n': "a newline", '\t': "a tab", '|': "a pipe", '&': "an &", ';': "a ;", '<': "a redirect", '>': "a redirect",
		'`': "a backtick", '"': "a double quote", '\\': "a backslash", '\'': "an unpaired '", '$': "an expansion",
		'*': "a glob", '?': "a glob", '[': "a glob", ']': "a glob", '(': "a subshell", ')': "a subshell",
		'{': "a brace expansion", '}': "a brace expansion", '~': "a tilde expansion", '!': "a negation", '#': "a comment",
	}
	// shellWords are the first words a run string may not start with: bash 3.2's keywords and builtins
	// (compgen -k -b) that argvByte passes, and env.
	shellWords = []string{".", ":", "alias", "bg", "bind", "break", "builtin", "caller", "case", "cd", "command",
		"compgen", "complete", "continue", "declare", "dirs", "disown", "do", "done", "echo", "elif", "else",
		"enable", "env", "esac", "eval", "exec", "exit", "export", "false", "fc", "fg", "fi", "for", "function", "getopts",
		"hash", "help", "history", "if", "in", "jobs", "kill", "let", "local", "logout", "popd", "printf", "pushd",
		"pwd", "read", "readonly", "return", "select", "set", "shift", "shopt", "source", "suspend", "test",
		"then", "time", "times", "trap", "true", "type", "typeset", "ulimit", "umask", "unalias", "unset", "until",
		"wait", "while"}
	// shells run a quoted argument as shell text wherever they stand in the argv.
	shells = []string{"ash", "bash", "busybox", "csh", "dash", "fish", "ksh", "mksh", "pwsh", "sh", "tcsh", "zsh"}
)

// argvByte reports whether c may stand unquoted in a plain argv word.
func argvByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(" -_./=:,@+%^", c) >= 0
}

// oneQuotedOperand reports whether a Tera value renders as one quoted word: a variable path, or one
// parenthesised group, piped last through quote.
func oneQuotedOperand(v string) bool {
	m := quotedValue.FindStringSubmatch(strings.TrimSpace(strings.Trim(strings.TrimSpace(v), "-")))
	if m == nil {
		return false
	}
	expr := strings.TrimSpace(m[1])
	return teraPath.MatchString(expr) || wrapsWhole(expr)
}

// wrapsWhole reports whether expr is one parenthesised group, reading past parentheses inside string literals.
func wrapsWhole(expr string) bool {
	if !strings.HasPrefix(expr, "(") {
		return false
	}
	depth := 0
	var lit byte
	for i := 0; i < len(expr); i++ {
		switch c := expr[i]; {
		case lit != 0:
			if c == lit {
				lit = 0
			}
		case c == '\'' || c == '"' || c == '`':
			lit = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i == len(expr)-1
			}
		}
	}
	return false
}

// shellLogic names the first piece of shell syntax in a mise run string, or returns "" for plain argv.
func shellLogic(s string) string {
	for _, r := range s {
		if r > 127 {
			return fmt.Sprintf("the non-ASCII %q", r)
		}
	}
	for _, m := range teraValue.FindAllStringSubmatch(s, -1) {
		if !oneQuotedOperand(m[1]) {
			return "the Tera value " + m[0] + ", which is not one quoted operand: pipe a variable or one ( … ) group through quote last"
		}
	}
	s = quotedArgv.ReplaceAllString(teraBlock.ReplaceAllString(s, ""), "")
	for i := 0; i < len(s); i++ {
		if s[i] == '$' && (i+1 == len(s) || s[i+1] == ' ') {
			continue
		}
		if !argvByte(s[i]) {
			if what, ok := shellChars[s[i]]; ok {
				return what
			}
			return fmt.Sprintf("the shell character %q", s[i])
		}
	}
	s = strings.TrimSpace(s)
	if leadingAssign.MatchString(s) {
		return "a leading assignment"
	}
	if first, _, _ := strings.Cut(s, " "); slices.Contains(shellWords, first) {
		return "the shell word " + first
	}
	for w := range strings.FieldsSeq(s) {
		if slices.Contains(shells, path.Base(w)) {
			return "the shell " + w
		}
	}
	return ""
}

// shellFindings lists "[tasks.x] runs …: why" for every string run entry and skip_if holding shell logic.
func shellFindings(cfg *miseConfig) []string {
	var out []string
	for name, task := range cfg.tasks {
		lines := task.cmds()
		if v, ok := task.fields["skip_if"]; ok {
			lines = append(lines, v.text())
		}
		for _, line := range lines {
			if why := shellLogic(line); why != "" {
				out = append(out, fmt.Sprintf("[tasks.%s] runs %q: %s", name, line, why))
			}
		}
	}
	slices.Sort(out)
	return out
}

// taskKeys are the task keys the scan accounts for: shellFindings reads run and skip_if, and the rest hold no command.
var taskKeys = []string{"alias", "confirm", "depends", "description", "dir", "env", "hide", "run", "skip_if", "tools",
	"usage", "wait_for"}

// taskShapeFindings lists every task form shellFindings cannot read: a [tasks] shorthand, a top-level
// tasks. key, a task sub-table or array of tables, and a task key outside taskKeys.
func taskShapeFindings(cfg *miseConfig) []string {
	var out []string
	for k := range cfg.top {
		if k == "tasks" || strings.HasPrefix(k, "tasks.") {
			out = append(out, fmt.Sprintf("the top-level key %s declares a task outside a [tasks.<name>] table", k))
		}
	}
	for name, table := range cfg.tables {
		inner, array := strings.CutPrefix(name, "[")
		inner = strings.TrimSuffix(inner, "]")
		sub, isTask := strings.CutPrefix(inner, "tasks.")
		switch {
		case array && (inner == "tasks" || isTask):
			out = append(out, fmt.Sprintf("[[%s]] is an array of tables; write the run entries inline", inner))
		case inner == "tasks":
			for k := range table.fields {
				out = append(out, fmt.Sprintf("[tasks] declares %s as a shorthand; give it a [tasks.%s] table", k, k))
			}
		case isTask && strings.Contains(sub, "."):
			out = append(out, fmt.Sprintf("[%s] is a task sub-table; write it as an inline table", inner))
		case isTask:
			for k := range table.fields {
				if !slices.Contains(taskKeys, k) {
					out = append(out, fmt.Sprintf("[tasks.%s] sets %s, a key the scan does not read", sub, k))
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

func TestMiseTasksRunNoShellScripts(t *testing.T) {
	t.Parallel()
	src := readRepoFile(t, "mise.toml")
	for _, banned := range []string{`run = """`, `run = '''`, "#!"} {
		if strings.Contains(src, banned) {
			t.Errorf("mise.toml contains %q; %s", banned, plainArgv)
		}
	}
	cfg, err := parseMiseTOML(src)
	if err != nil {
		t.Fatalf("mise.toml: %v", err)
	}
	for _, finding := range append(taskShapeFindings(cfg), shellFindings(cfg)...) {
		t.Errorf("%s; %s", finding, plainArgv)
	}
}

const taskShapesFixture = `tasks.dotted.run = "go vet ./... | tee x"

[tasks]
short = "go vet ./... | tee x"

[tasks.win]
run_windows = "go vet ./... | tee x"

[tasks.script]
file = "scripts/x.sh"

[tasks.sub.env]
GOFLAGS = "-race"

[[tasks.arr.run]]
task = "lint"

[tasks.plain]
description = "a [tasks.<name>] table"
dir = "{{config_root}}"
run = "go vet ./..."
`

func TestShellScanReadsEveryTaskShape(t *testing.T) {
	t.Parallel()
	cfg, err := parseMiseTOML(taskShapesFixture)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"the top-level key tasks.dotted.run declares a task outside a [tasks.<name>] table",
		"[tasks] declares short as a shorthand; give it a [tasks.short] table",
		"[tasks.win] sets run_windows, a key the scan does not read",
		"[tasks.script] sets file, a key the scan does not read",
		"[tasks.sub.env] is a task sub-table; write it as an inline table",
		"[[tasks.arr.run]] is an array of tables; write the run entries inline",
	}
	slices.Sort(want)
	if got := taskShapeFindings(cfg); !slices.Equal(got, want) {
		t.Errorf("taskShapeFindings = %q,\nwant %q", got, want)
	}
}

// ciGlyph is U+2705, a non-ASCII glyph built at run time.
var ciGlyph = string(rune(0x2705))

// glyphCIRun is a ci run string that prints ciGlyph, as the reader decodes it.
var glyphCIRun = `printf '\n` + ciGlyph + ` ci succeeded.\n'`

// shellTasksFixture holds a ci task that prints ciGlyph and a test-images task with a leading assignment.
var shellTasksFixture = strings.ReplaceAll(`[tasks.ci]
description = "Local CI gate: lint + go build + unit/contract tests (no Docker)"
depends = ["lint", "build-check", "test-go"]
run = "printf '\\nCHECK ci succeeded.\\n'"

[tasks.test-images]
description = "Every image suite (TestImage*) + the consumer CLI suite (needs Docker; slow)"
dir = "{{config_root}}"
run = [
  "PROVEO_INSTALLTEST_CDN=apps/cli/public/cli go test -count=1 ./internal/installtest/",
  "go test -tags=image -count=1 -timeout 60m ./internal/imagetest/",
]
`, "CHECK", ciGlyph)

func TestShellLogicScanFlagsHandRolledShell(t *testing.T) {
	t.Parallel()
	flagged := []string{
		glyphCIRun,
		"PROVEO_INSTALLTEST_CDN=apps/cli/public/cli go test -count=1 ./internal/installtest/",
		"go test ./... | tee cov/out.txt",
		"go build ./... && go test ./...",
		"go test ./... || true",
		"go test $(go list ./internal/...)",
		"go test ./... > cov/out.txt",
		"[[ -d cov ]]",
		"sh -c 'go test ./...'",
		"cd e2e",
		"go test -timeout ${PROVEO_TEST_GO_TIMEOUT:-45m} ./e2e/",
		"go test -run $RUN ./...",
		"rm -rf cov/*",
		"go vet ./...; go test ./...",
		"go test \"./...\"",
		"echo `date`",
		"go test 'unpaired",
		"go vet ./...\ngo test ./...",
		"export GOFLAGS=-race",
		"test -n x",
		"! go test ./...",
		"(go test ./...)",
		"go test ~/x",
		"rm -rf cov/?",
		"go test [ab]",
		"go test {a,b}",
		"go test ./... # note",
		"go test -run=$1 ./...",
		"go test -count=$? ./...",
		"time go test ./...",
		"exit 1",
		"trap x EXIT",
		"unset GOFLAGS",
		"ulimit -n 10",
		"until false",
		"command -v go",
		"printf ok",
		". ./env",
		"env GOFLAGS=-race go test ./...",
		"go run ./x -tag={{usage.tag}}",
		"go test {{ a }}",
		`go test {{ "./...; rm -rf cov" }}`,
		"go test {{ a | quote | upper }}",
		"go test {% if x %}|{% endif %} tee x",
		"go test {{ 'x; touch /tmp/pwn; ' ~ a | quote }}",
		"go test {{ a ~ 'x' | quote }}",
		"go test {{ (a) ~ (b) | quote }}",
		"xargs sh -c 'go vet ./... && go test ./...'",
		"find . -name x -exec /bin/sh -c 'go vet ./...' +",
		"ksh -c 'go vet ./... && go test ./...'",
		"fish -c 'go vet ./...; go test ./...'",
	}
	clean := []string{
		"pnpm install",
		"golangci-lint fmt",
		"go test -count=1 '-run=^(TestEveryTrackedGoFileIsGofmted|TestNoCommentBlockOverThreeLines)$' ./internal/contract/",
		"go vet ./...",
		"go vet -tags=image ./internal/imagetest/",
		"go vet -tags=e2e ./e2e/",
		"golangci-lint run",
		"{% if get_env(name='SKIP_GIT_HOOKS', default='') | trim | lower in ['1', 'true', 'yes', 'on', 'enable', 'enabled'] %}true{% else %}false{% endif %}",
		"mise generate git-pre-commit --hook post-commit --task post-commit --write",
		"go build ./...",
		"go test {% if not usage.no_race %}-race {% endif %}-count=1 -skip={{ ('^(' ~ vars.unit_baseline ~ ')$') | quote }}{% for a in usage.go_test_args %} {{ a | quote }}{% endfor %}",
		"go install -trimpath '-ldflags=-s -w' ./cmd/proveo ./cmd/proveo-entrypoint",
		"rm -rf cov/unit",
		"mkdir -p cov/unit",
		"go test {% if not usage.no_race %}-race {% endif %}-cover -covermode=atomic ./... -args -test.gocoverdir={{ config_root | quote }}/cov/unit",
		"go test -tags=e2e ./e2e/ -count=1 -timeout " + timeoutArg("45m"),
		"go tool covdata percent -i=cov/unit",
		"go tool covdata textfmt -i=cov/unit -o=cov/coverage.out",
		"go test -count=1 ./internal/installtest/",
		"go test -tags=image -count=1 -timeout 60m ./internal/imagetest/",
		"mise run --continue-on-error e2e-gated{% for a in usage.go_test_args %} {{ a | quote }}{% endfor %} ::: idle-all ::: ladder-all",
		"go test -tags=e2e ./e2e/ -count=1 -timeout " + timeoutArg("150m"),
		"go test -tags=e2e -count=1 -timeout 30m ./e2e/ -run=^TestAgentSurvivesIdleAtPrompt$ -v",
		"mise run --continue-on-error --jobs=1 ladder:claudecode ::: ladder:codex ::: ladder:cursor ::: ladder:opencode",
		"go test -tags=e2e -count=1 -timeout 30m ./e2e/ -run=^TestSandboxLadder$ -v",
		"pnpm exec wrangler --version",
		"goreleaser release --snapshot --clean",
		"go test -count=1 '-run=^(TestNoRetirementOutlivesOneRelease|TestReleaseShipsATaggedHead)$' ./internal/contract/",
		"goreleaser release --clean --skip=publish",
		"go run ./internal/cdn/stage",
		"pnpm exec wrangler deploy --cwd apps/cli",
		"pnpm exec wrangler dev --cwd apps/cli",
		"go test {{- a | quote -}}",
		"mise run lint ::: build-check ::: test-go{% if usage.no_race %} --no-race{% endif %}",
		"go test -count=1 '-run=^TestStagedCDNIsTheTaggedRelease$' ./internal/contract/",
		"go test {{ (a | trim) | quote }}",
	}
	for _, line := range imageTasks {
		clean = append(clean, line)
	}
	for _, s := range flagged {
		if shellLogic(s) == "" {
			t.Errorf("the scan passed hand-rolled shell %q", s)
		}
	}
	for _, s := range clean {
		if why := shellLogic(s); why != "" {
			t.Errorf("the scan flagged plain argv %q: %s", s, why)
		}
	}
	fixture, err := parseMiseTOML(shellTasksFixture)
	if err != nil {
		t.Fatal(err)
	}
	got := shellFindings(fixture)
	want := []string{
		fmt.Sprintf("[tasks.ci] runs %q: the non-ASCII %q", glyphCIRun, rune(0x2705)),
		`[tasks.test-images] runs "PROVEO_INSTALLTEST_CDN=apps/cli/public/cli go test -count=1 ./internal/installtest/": a leading assignment`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("shellTasksFixture yields %q, want %q", got, want)
	}
}
