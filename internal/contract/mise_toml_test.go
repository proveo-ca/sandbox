// SPEC: _spec/tests/20-contract.puml
package contract_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// miseValue is one TOML value: a string, a bare word (true, false, a number), an array or an inline table.
type miseValue struct {
	str   string
	isStr bool
	bare  string
	items []miseValue
	table map[string]miseValue
}

// text is the decoded string, or the bare word as written.
func (v miseValue) text() string {
	if v.isStr {
		return v.str
	}
	return v.bare
}

// miseTable is one [table] of mise.toml: its keys and its source text.
type miseTable struct {
	name   string
	fields map[string]miseValue
	body   string
}

// miseRun is one run entry: a command string, or the task a { task = … } table names.
type miseRun struct {
	cmd, task string
	isCmd     bool
}

func (r miseRun) String() string {
	if r.isCmd {
		return r.cmd
	}
	return "{ task = " + r.task + " }"
}

// miseConfig is mise.toml by table; tasks drop the tasks. prefix and every quote of their header.
type miseConfig struct {
	top    map[string]miseValue
	tables map[string]*miseTable
	tasks  map[string]*miseTable
	vars   map[string]string
}

func readMise(t *testing.T) *miseConfig {
	t.Helper()
	cfg, err := parseMiseTOML(readRepoFile(t, "mise.toml"))
	if err != nil {
		t.Fatalf("mise.toml: %v", err)
	}
	return cfg
}

func (c *miseConfig) task(t *testing.T, name string) *miseTable {
	t.Helper()
	task, ok := c.tasks[name]
	if !ok {
		t.Fatalf("mise.toml declares no [tasks.%s]", name)
	}
	return task
}

func (m *miseTable) str(key string) string { return m.fields[key].text() }

// code is the table's source text minus its description line.
func (m *miseTable) code() string {
	var keep []string
	for _, line := range strings.Split(m.body, "\n") {
		if !strings.HasPrefix(line, "description =") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

// run lists the task's run entries: one for a string, one per item for an array.
func (m *miseTable) run() []miseRun {
	v, ok := m.fields["run"]
	if !ok {
		return nil
	}
	if v.items == nil {
		return []miseRun{{cmd: v.text(), isCmd: v.isStr}}
	}
	var out []miseRun
	for _, item := range v.items {
		if item.table != nil {
			out = append(out, miseRun{task: item.table["task"].text()})
			continue
		}
		out = append(out, miseRun{cmd: item.text(), isCmd: true})
	}
	return out
}

// cmds lists the task's string run entries.
func (m *miseTable) cmds() []string {
	var out []string
	for _, r := range m.run() {
		if r.isCmd {
			out = append(out, r.cmd)
		}
	}
	return out
}

// env is the task's inline env table; a bare value such as false stays its literal word.
func (m *miseTable) env() map[string]string {
	out := map[string]string{}
	for k, v := range m.fields["env"].table {
		out[k] = v.text()
	}
	return out
}

// list is an array of strings such as depends or wait_for.
func (m *miseTable) list(key string) []string {
	var out []string
	for _, item := range m.fields[key].items {
		out = append(out, item.text())
	}
	return out
}

var usageQuoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// choices lists the words of the usage spec's choices line.
func (m *miseTable) choices() []string {
	for _, line := range strings.Split(m.str("usage"), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "choices ")
		if !ok {
			continue
		}
		var out []string
		for _, q := range usageQuoted.FindAllString(rest, -1) {
			if s, err := strconv.Unquote(q); err == nil {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// trackedFiles lists the repo's tracked paths the working tree still holds.
func trackedFiles(t *testing.T) []string {
	t.Helper()
	return trackedOrSkip(t, repoRoot(t), nil)
}

// trackedOrSkip lists root's tracked files; when git cannot answer it fails tb for a root
// that holds .git and skips tb for one that holds none.
func trackedOrSkip(tb testing.TB, root string, env []string) []string {
	tb.Helper()
	files, err := trackedFilesIn(root, env)
	if err == nil {
		return files
	}
	if _, statErr := os.Lstat(filepath.Join(root, ".git")); statErr == nil {
		tb.Fatalf("git ls-files in %s: %v; the checkout holds .git, so a gate over tracked files fails instead of skipping", root, err)
		return nil
	}
	tb.Skipf("git ls-files: %v; %s holds no .git", err, root)
	return nil
}

// trackedFilesIn lists the paths git tracks under root, minus the ones its working tree deleted.
// git runs under env; a nil env inherits the test's.
func trackedFilesIn(root string, env []string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	cmd.Env = env
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, rel)); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		files = append(files, rel)
	}
	return files, nil
}

func TestTrackedFilesSkipWhatTheWorkingTreeDeleted(t *testing.T) {
	exportDecoyGitDir(t)
	dir := t.TempDir()
	hermeticGit(t, dir, "init", "-q")
	for _, name := range []string{"kept.go", "gone.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hermeticGit(t, dir, "add", "kept.go", "gone.go")
	hermeticGit(t, dir, "commit", "-qm", "two files")
	if err := os.Remove(filepath.Join(dir, "gone.go")); err != nil {
		t.Fatal(err)
	}
	got, err := trackedFilesIn(dir, hermeticEnv(t))
	if err != nil || !slices.Equal(got, []string{"kept.go"}) {
		t.Errorf("trackedFilesIn = %q, %v; want [kept.go]: a deletion git has not staged has nothing to read", got, err)
	}
}

func TestTrackedFilesFailWhereACheckoutCannotAnswer(t *testing.T) {
	t.Parallel()
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("not a gitdir\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loose := t.TempDir()
	env := append(hermeticEnv(t), "GIT_CEILING_DIRECTORIES="+filepath.Dir(loose))
	for _, c := range []struct {
		name, root    string
		fatals, skips int
	}{
		{name: "a checkout whose .git git cannot read", root: broken, fatals: 1},
		{name: "a tree with no .git", root: loose, skips: 1},
	} {
		fake := &gateTB{}
		if got := trackedOrSkip(fake, c.root, env); got != nil {
			t.Errorf("%s: trackedOrSkip listed %q", c.name, got)
		}
		if len(fake.fatals) != c.fatals || len(fake.skips) != c.skips {
			t.Errorf("%s: trackedOrSkip failed %q and skipped %q, want %d failure(s) and %d skip(s): "+
				"a skip would pass the gofmt and comment-block gates in lint", c.name, fake.fatals, fake.skips, c.fatals, c.skips)
		}
	}
}

type tomlScan struct {
	src string
	pos int
}

func (p *tomlScan) errorf(format string, args ...any) error {
	return fmt.Errorf("line %d: %s", strings.Count(p.src[:p.pos], "\n")+1, fmt.Sprintf(format, args...))
}

func (p *tomlScan) eof() bool { return p.pos >= len(p.src) }

func (p *tomlScan) skipSpaces() {
	for !p.eof() && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

// skipBlank skips spaces, newlines and # comments.
func (p *tomlScan) skipBlank() {
	for !p.eof() {
		switch p.src[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		case '#':
			p.skipComment()
		default:
			return
		}
	}
}

func (p *tomlScan) skipComment() {
	if end := strings.IndexByte(p.src[p.pos:], '\n'); end >= 0 {
		p.pos += end
		return
	}
	p.pos = len(p.src)
}

// endLine accepts blanks and a comment up to the end of the line.
func (p *tomlScan) endLine() error {
	p.skipSpaces()
	if !p.eof() && p.src[p.pos] == '#' {
		p.skipComment()
	}
	if !p.eof() && p.src[p.pos] != '\n' && p.src[p.pos] != '\r' {
		return p.errorf("unexpected %q after a value", p.src[p.pos])
	}
	return nil
}

// key reads a bare or quoted key and its = sign.
func (p *tomlScan) key() (string, error) {
	end := strings.IndexByte(p.src[p.pos:], '=')
	if end < 0 || strings.Contains(p.src[p.pos:p.pos+end], "\n") {
		return "", p.errorf("a key without =")
	}
	k := strings.TrimSpace(p.src[p.pos : p.pos+end])
	p.pos += end + 1
	return strings.Trim(k, `"'`), nil
}

func (p *tomlScan) value() (miseValue, error) {
	p.skipSpaces()
	rest := p.src[p.pos:]
	switch {
	case rest == "":
		return miseValue{}, p.errorf("a key without a value")
	case strings.HasPrefix(rest, `"""`), strings.HasPrefix(rest, `'''`):
		return p.multiline(rest[:3])
	case rest[0] == '"':
		end := 1
		for end < len(rest) && rest[end] != '"' && rest[end] != '\n' {
			if rest[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(rest) || rest[end] != '"' {
			return miseValue{}, p.errorf("an unterminated string")
		}
		s, err := strconv.Unquote(rest[:end+1])
		if err != nil {
			return miseValue{}, p.errorf("%v in %s", err, rest[:end+1])
		}
		p.pos += end + 1
		return miseValue{str: s, isStr: true}, nil
	case rest[0] == '\'':
		end := strings.IndexAny(rest[1:], "'\n")
		if end < 0 || rest[1+end] != '\'' {
			return miseValue{}, p.errorf("an unterminated literal string")
		}
		p.pos += end + 2
		return miseValue{str: rest[1 : 1+end], isStr: true}, nil
	case rest[0] == '[':
		return p.array()
	case rest[0] == '{':
		return p.inlineTable()
	}
	end := strings.IndexAny(rest, ",]} \t#\r\n")
	if end < 0 {
		end = len(rest)
	}
	word := strings.TrimSpace(rest[:end])
	if word == "" {
		return miseValue{}, p.errorf("an empty value")
	}
	p.pos += end
	return miseValue{bare: word}, nil
}

// multiline reads a triple-quoted string: it drops the newline after the opener, and in a basic
// one each backslash-newline with the blanks that follow it.
func (p *tomlScan) multiline(delim string) (miseValue, error) {
	start := p.pos + 3
	end := strings.Index(p.src[start:], delim)
	if end < 0 {
		return miseValue{}, p.errorf("an unterminated %s string", delim)
	}
	body := strings.TrimPrefix(strings.TrimPrefix(p.src[start:start+end], "\r"), "\n")
	p.pos = start + end + 3
	if delim == "'''" {
		return miseValue{str: body, isStr: true}, nil
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '\\' && strings.HasPrefix(strings.TrimLeft(body[i+1:], " \t\r"), "\n"):
			i = len(body) - len(strings.TrimLeft(body[i+1:], " \t\r\n")) - 1
		case c == '\\' && i+1 < len(body):
			b.WriteString(body[i : i+2])
			i++
		case c == '"':
			b.WriteString(`\"`)
		case c == '\n':
			b.WriteString(`\n`)
		default:
			b.WriteByte(c)
		}
	}
	s, err := strconv.Unquote(`"` + b.String() + `"`)
	if err != nil {
		return miseValue{}, p.errorf("%v in a \"\"\" string", err)
	}
	return miseValue{str: s, isStr: true}, nil
}

func (p *tomlScan) array() (miseValue, error) {
	p.pos++
	items := []miseValue{}
	for {
		p.skipBlank()
		if p.eof() {
			return miseValue{}, p.errorf("an unterminated array")
		}
		if p.src[p.pos] == ']' {
			p.pos++
			return miseValue{items: items}, nil
		}
		v, err := p.value()
		if err != nil {
			return miseValue{}, err
		}
		items = append(items, v)
		p.skipBlank()
		if !p.eof() && p.src[p.pos] == ',' {
			p.pos++
		}
	}
}

func (p *tomlScan) inlineTable() (miseValue, error) {
	p.pos++
	table := map[string]miseValue{}
	for {
		p.skipSpaces()
		if p.eof() || p.src[p.pos] == '\n' {
			return miseValue{}, p.errorf("an unterminated inline table")
		}
		if p.src[p.pos] == '}' {
			p.pos++
			return miseValue{table: table}, nil
		}
		k, err := p.key()
		if err != nil {
			return miseValue{}, err
		}
		v, err := p.value()
		if err != nil {
			return miseValue{}, err
		}
		table[k] = v
		p.skipSpaces()
		if !p.eof() && p.src[p.pos] == ',' {
			p.pos++
		}
	}
}

func parseMiseTOML(src string) (*miseConfig, error) {
	cfg := &miseConfig{top: map[string]miseValue{}, tables: map[string]*miseTable{},
		tasks: map[string]*miseTable{}, vars: map[string]string{}}
	p := &tomlScan{src: src}
	fields := cfg.top
	var cur *miseTable
	start, last := 0, 0
	closeBody := func() {
		if cur != nil {
			cur.body = src[start:last]
		}
	}
	for {
		p.skipBlank()
		if p.eof() {
			break
		}
		if p.src[p.pos] == '[' {
			closeBody()
			line, _, _ := strings.Cut(p.src[p.pos:], "\n")
			end := strings.LastIndexByte(line, ']')
			if end < 0 {
				return nil, p.errorf("an unterminated table header")
			}
			name := strings.ReplaceAll(strings.TrimSpace(line[1:end]), `"`, "")
			if _, dup := cfg.tables[name]; dup {
				return nil, p.errorf("[%s] declared twice", name)
			}
			cur = &miseTable{name: name, fields: map[string]miseValue{}}
			cfg.tables[name] = cur
			if task, ok := strings.CutPrefix(name, "tasks."); ok {
				cfg.tasks[task] = cur
			}
			fields = cur.fields
			start = p.pos
			p.pos += len(line)
			last = p.pos
			continue
		}
		k, err := p.key()
		if err != nil {
			return nil, err
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		if err := p.endLine(); err != nil {
			return nil, err
		}
		if _, dup := fields[k]; dup {
			return nil, p.errorf("key %s declared twice", k)
		}
		fields[k] = v
		last = p.pos
	}
	closeBody()
	if vars, ok := cfg.tables["vars"]; ok {
		for k, v := range vars.fields {
			cfg.vars[k] = v.text()
		}
	}
	return cfg, nil
}

const miseReaderFixture = `# skip_if needs mise 2026.10.10.
min_version = "2026.10.10"

[vars]
names = """\
  TestA|\
  TestB\
  """

[tools]
go = "1.26.10"

# ── section ──

[tasks.one]
description = "a single run string"
dir = "{{config_root}}"
run = "go test -count=1 '-run=^(TestA|TestB)$' ./internal/contract/"

[tasks.mixed]
env = { PROVEO_X = "apps/cli/public/cli", GOBIN = false }
depends = ["one", "chain"]
wait_for = ["one"]
run = [
  { task = "snapshot" },
  "go test -count=1 ./internal/installtest/", # trailing comment
  { task = "sub", args = ["-v", "x"], env = { A = "1" } },
  "go vet ./...",
]

[tasks.chain]
hide = true
skip_if = "{% if get_env(name='SKIP', default='') | trim | lower in ['1', 'on'] %}true{% else %}false{% endif %}"
run = [{ task = "fmt" }, { task = "lint" }]

[tasks."ladder:codex"]
env = { PROVEO_LADDER_TARGET = "codex" }
run = "go test -run=^TestSandboxLadder$ -v"

[tasks.build]
usage = '''
arg "<target>" help="a target" {
  choices "all" "base" "claudecode"
}
flag "--print" help="print the plan"
'''
run = "go run ./x -print={{usage.print}} build {{usage.target}}"

[tasks.lane]
usage = 'flag "--no-race" help="no race"'
run = "printf '\\n→ ok.\\n'"
`

func TestMiseTomlReaderReadsEveryRunShape(t *testing.T) {
	t.Parallel()
	cfg, err := parseMiseTOML(miseReaderFixture)
	if err != nil {
		t.Fatal(err)
	}
	eq := func(what string, got, want any) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s = %q, want %q", what, got, want)
		}
	}
	runs := func(name string) []string {
		var out []string
		for _, r := range cfg.task(t, name).run() {
			out = append(out, r.String())
		}
		return out
	}
	eq("min_version", cfg.top["min_version"].text(), "2026.10.10")
	eq("vars.names", cfg.vars["names"], "TestA|TestB")
	eq("tools.go", cfg.tables["tools"].str("go"), "1.26.10")
	eq("one.run", runs("one"), []string{"go test -count=1 '-run=^(TestA|TestB)$' ./internal/contract/"})
	eq("one.dir", cfg.task(t, "one").str("dir"), "{{config_root}}")
	eq("mixed.run", runs("mixed"), []string{"{ task = snapshot }", "go test -count=1 ./internal/installtest/",
		"{ task = sub }", "go vet ./..."})
	eq("mixed.cmds", cfg.task(t, "mixed").cmds(), []string{"go test -count=1 ./internal/installtest/", "go vet ./..."})
	eq("mixed.env", cfg.task(t, "mixed").env(), map[string]string{"PROVEO_X": "apps/cli/public/cli", "GOBIN": "false"})
	eq("mixed.depends", cfg.task(t, "mixed").list("depends"), []string{"one", "chain"})
	eq("mixed.wait_for", cfg.task(t, "mixed").list("wait_for"), []string{"one"})
	eq("chain.run", runs("chain"), []string{"{ task = fmt }", "{ task = lint }"})
	eq("chain.cmds", len(cfg.task(t, "chain").cmds()), 0)
	eq("chain.hide", cfg.task(t, "chain").str("hide"), "true")
	eq("chain.skip_if", cfg.task(t, "chain").str("skip_if"),
		"{% if get_env(name='SKIP', default='') | trim | lower in ['1', 'on'] %}true{% else %}false{% endif %}")
	eq("ladder:codex.env", cfg.task(t, "ladder:codex").env(), map[string]string{"PROVEO_LADDER_TARGET": "codex"})
	eq("build.usage", cfg.task(t, "build").str("usage"),
		"arg \"<target>\" help=\"a target\" {\n  choices \"all\" \"base\" \"claudecode\"\n}\nflag \"--print\" help=\"print the plan\"\n")
	eq("build.choices", cfg.task(t, "build").choices(), []string{"all", "base", "claudecode"})
	eq("lane.usage", cfg.task(t, "lane").str("usage"), `flag "--no-race" help="no race"`)
	eq("lane.run", runs("lane"), []string{`printf '\n→ ok.\n'`})
	body := cfg.task(t, "chain").body
	if !strings.HasPrefix(body, "[tasks.chain]\n") || !strings.HasSuffix(body, `{ task = "lint" }]`) {
		t.Errorf("chain.body = %q, want its header through its last line", body)
	}
	if code := cfg.task(t, "one").code(); strings.Contains(code, "a single run string") || !strings.Contains(code, "run = ") {
		t.Errorf("one.code() = %q, want the table without its description line", code)
	}
	var names []string
	for name := range cfg.tasks {
		names = append(names, name)
	}
	slices.Sort(names)
	eq("tasks", names, []string{"build", "chain", "ladder:codex", "lane", "mixed", "one"})
	for _, bad := range []string{
		"run = \"unterminated\n",
		"run = [\"a\",\n",
		"run = { task = \"x\"\n",
		"[tasks.x\n",
		"x = \"\"\"never closed\n",
		"x = 1 y\n",
		"[a]\n[a]\n",
	} {
		if _, err := parseMiseTOML(bad); err == nil {
			t.Errorf("parseMiseTOML(%q) accepted malformed TOML", bad)
		}
	}
}
