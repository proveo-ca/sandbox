// SPEC: _spec/internal/devports/dev-ports.puml
// Package devports finds a workspace's run commands and the port each one serves.
package devports

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Candidate is one run command and the port it serves.
type Candidate struct {
	Port     int
	Command  string // the run command's name: "dev", "tasks.serve", "web", "runserver"
	Tool     string // the catalog entry that matched: "next", "vite", "django" …
	Source   string // workspace-relative file the command came from
	Explicit bool   // the command names its port; false = the tool's default
	Line     string // the command line as the file states it
}

// Label is the one-line form a choice row prints.
func (c Candidate) Label() string {
	return strconv.Itoa(c.Port) + " " + c.Tool
}

// tool is one catalog entry: a command matcher and the port it serves by default.
type tool struct {
	name string
	re   *regexp.Regexp
	port int
	bind string // args that make the server listen on every interface
}

// catalog is ordered: the first match wins.
var catalog = []tool{
	{"storybook", regexp.MustCompile(`\bstorybook\s+dev\b|\bstart-storybook\b`), 6006, "--host 0.0.0.0"},
	{"vite preview", regexp.MustCompile(`\bvite\s+preview\b`), 4173, "--host 0.0.0.0"},
	{"next", regexp.MustCompile(`\bnext\s+(dev|start)\b`), 3000, "-H 0.0.0.0"},
	{"nuxt", regexp.MustCompile(`\bnuxi?\s+(dev|preview)\b`), 3000, "--host 0.0.0.0"},
	{"astro", regexp.MustCompile(`\bastro\s+(dev|preview)\b`), 4321, "--host 0.0.0.0"},
	{"remix", regexp.MustCompile(`\bremix\s+dev\b`), 3000, "--host 0.0.0.0"},
	{"react-router", regexp.MustCompile(`\breact-router\s+dev\b`), 5173, "--host 0.0.0.0"},
	{"angular", regexp.MustCompile(`\bng\s+serve\b`), 4200, "--host 0.0.0.0"},
	{"gatsby", regexp.MustCompile(`\bgatsby\s+develop\b`), 8000, "-H 0.0.0.0"},
	{"expo", regexp.MustCompile(`\bexpo\s+start\b`), 8081, ""},
	{"wrangler", regexp.MustCompile(`\bwrangler\s+(pages\s+)?dev\b`), 8787, "--ip 0.0.0.0"},
	{"docusaurus", regexp.MustCompile(`\bdocusaurus\s+start\b`), 3000, "--host 0.0.0.0"},
	{"react-scripts", regexp.MustCompile(`\breact-scripts\s+start\b`), 3000, ""},
	{"webpack", regexp.MustCompile(`\bwebpack(-dev-server|\s+serve)\b`), 8080, "--host 0.0.0.0"},
	{"parcel", regexp.MustCompile(`\bparcel\b`), 1234, "--host 0.0.0.0"},
	{"eleventy", regexp.MustCompile(`\beleventy\b.*--serve\b`), 8080, ""},
	{"vite", regexp.MustCompile(`\bvite(\s+dev)?\s*($|&|;|\s-)`), 5173, "--host 0.0.0.0"},
	{"django", regexp.MustCompile(`\bmanage\.py\s+runserver\b`), 8000, ""},
	{"fastapi", regexp.MustCompile(`\bfastapi\s+(dev|run)\b`), 8000, "--host 0.0.0.0"},
	{"uvicorn", regexp.MustCompile(`\buvicorn\b`), 8000, "--host 0.0.0.0"},
	{"gunicorn", regexp.MustCompile(`\bgunicorn\b`), 8000, ""},
	{"flask", regexp.MustCompile(`\bflask\s+run\b`), 5000, "--host 0.0.0.0"},
	{"streamlit", regexp.MustCompile(`\bstreamlit\s+run\b`), 8501, "--server.address 0.0.0.0"},
	{"jupyter", regexp.MustCompile(`\bjupyter\s+(lab|notebook)\b`), 8888, "--ip 0.0.0.0"},
	{"mkdocs", regexp.MustCompile(`\bmkdocs\s+serve\b`), 8000, ""},
	{"rails", regexp.MustCompile(`\brails\s+(s|server)\b`), 3000, "-b 0.0.0.0"},
	{"hugo", regexp.MustCompile(`\bhugo\s+server\b`), 1313, "--bind 0.0.0.0"},
	{"jekyll", regexp.MustCompile(`\bjekyll\s+serve\b`), 4000, "--host 0.0.0.0"},
	{"php", regexp.MustCompile(`\bphp\s+(-S|artisan\s+serve)\b`), 8000, ""},
	{"air", regexp.MustCompile(`(^|\s)air(\s|$)`), 8080, ""},
	{"go run", regexp.MustCompile(`\bgo\s+run\b`), 0, ""},
	{"cargo run", regexp.MustCompile(`\bcargo\s+(run|watch)\b`), 0, ""},
	{"python", regexp.MustCompile(`\bpython3?\s+-m\s+http\.server\b`), 8000, ""},
}

var explicitPort = []*regexp.Regexp{
	regexp.MustCompile(`(?:--port|--http-port|-p|-P)[ =](\d{2,5})\b`),
	regexp.MustCompile(`\bPORT=(\d{2,5})\b`),
	regexp.MustCompile(`(?:--bind|-b|--host)[ =][\w.\[\]:]*:(\d{2,5})\b`),
	regexp.MustCompile(`\brunserver\s+(?:[\w.]+:)?(\d{2,5})\b`),
	regexp.MustCompile(`\bhttp\.server\s+(\d{2,5})\b`),
	regexp.MustCompile(`\bphp\s+-S\s+[\w.]+:(\d{2,5})\b`),
}

// runNames are the command names that usually start a server.
var runNames = map[string]bool{
	"dev": true, "start": true, "serve": true, "server": true, "preview": true,
	"run": true, "web": true, "watch": true, "storybook": true, "develop": true,
}

func isRunName(name string) bool {
	n := strings.ToLower(name)
	if i := strings.LastIndexAny(n, ":."); i >= 0 {
		n = n[i+1:]
	}
	return runNames[n] || strings.HasPrefix(strings.ToLower(name), "dev")
}

// Match resolves one command line against the catalog; ok is false when no tool or port is known.
func Match(line string) (toolName string, port int, explicit bool, ok bool) {
	for _, re := range explicitPort {
		if m := re.FindStringSubmatch(line); m != nil {
			if p, err := strconv.Atoi(m[1]); err == nil && valid(p) {
				port, explicit = p, true
				break
			}
		}
	}
	for _, t := range catalog {
		if t.re.MatchString(line) {
			toolName = t.name
			if !explicit {
				port = t.port
			}
			break
		}
	}
	if toolName == "" && explicit {
		toolName = "custom"
	}
	return toolName, port, explicit, toolName != "" && valid(port)
}

func valid(p int) bool { return p >= 1 && p <= 65535 }

// maxDepth bounds the walk: the root and two levels of monorepo members.
const maxDepth = 2

var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "vendor": true, "dist": true, "build": true,
	".venv": true, "venv": true, "target": true, ".next": true, ".turbo": true,
}

// Discover walks root and returns every run command with a known port, ordered by port.
func Discover(root string) []Candidate {
	var out []Candidate
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			if rel != "." && strings.Count(rel, string(filepath.Separator)) >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		switch d.Name() {
		case "package.json":
			out = append(out, fromPackageJSON(path, rel)...)
		case "mise.toml", ".mise.toml":
			out = append(out, fromMise(path, rel)...)
		case "Makefile":
			out = append(out, fromMakefile(path, rel)...)
		case "Procfile", "Procfile.dev":
			out = append(out, fromProcfile(path, rel)...)
		case "manage.py":
			out = append(out, Candidate{Port: 8000, Command: "runserver", Tool: "django", Source: rel})
		case "main.go":
			out = append(out, fromGoMain(path, rel)...)
		}
		return nil
	})
	return dedupe(out)
}

func candidate(name, line, rel string) (Candidate, bool) {
	t, port, explicit, ok := Match(line)
	if !ok {
		return Candidate{}, false
	}
	return Candidate{Port: port, Command: name, Tool: t, Source: rel, Explicit: explicit, Line: strings.TrimSpace(line)}, true
}

func fromPackageJSON(path, rel string) []Candidate {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return nil
	}
	var out []Candidate
	for name, line := range pkg.Scripts {
		if !isRunName(name) {
			continue
		}
		if c, ok := candidate(name, line, rel); ok {
			out = append(out, c)
		}
	}
	return out
}

var (
	miseTask = regexp.MustCompile(`^\[tasks\.("?)([\w:.-]+)("?)\]`)
	miseRun  = regexp.MustCompile(`^run\s*=\s*(?:"([^"]*)"|'([^']*)'|\[\s*"([^"]*)")`)
)

func fromMise(path, rel string) []Candidate {
	var out []Candidate
	task := ""
	eachLine(path, func(ln string) {
		if m := miseTask.FindStringSubmatch(ln); m != nil {
			task = m[2]
			return
		}
		if strings.HasPrefix(ln, "[") {
			task = ""
			return
		}
		if task == "" || !isRunName(task) {
			return
		}
		if m := miseRun.FindStringSubmatch(ln); m != nil {
			if c, ok := candidate("tasks."+task, m[1]+m[2]+m[3], rel); ok {
				out = append(out, c)
			}
		}
	})
	return out
}

var makeTarget = regexp.MustCompile(`^([\w.-]+)\s*:([^=]|$)`)

func fromMakefile(path, rel string) []Candidate {
	var out []Candidate
	target := ""
	eachLine(path, func(ln string) {
		if m := makeTarget.FindStringSubmatch(ln); m != nil {
			target = m[1]
			return
		}
		if target == "" || !isRunName(target) || !strings.HasPrefix(ln, "\t") {
			return
		}
		if c, ok := candidate(target, strings.TrimSpace(ln), rel); ok {
			out = append(out, c)
		}
	})
	return out
}

func fromProcfile(path, rel string) []Candidate {
	var out []Candidate
	eachLine(path, func(ln string) {
		name, line, ok := strings.Cut(ln, ":")
		if !ok {
			return
		}
		if c, ok := candidate(strings.TrimSpace(name), line, rel); ok {
			out = append(out, c)
		}
	})
	return out
}

var goListen = regexp.MustCompile(`(?:ListenAndServe(?:TLS)?|Listen|Run|Start)\(\s*"[\w.]*:(\d{2,5})"`)

func fromGoMain(path, rel string) []Candidate {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	m := goListen.FindSubmatch(b)
	if m == nil {
		return nil
	}
	p, err := strconv.Atoi(string(m[1]))
	if err != nil || !valid(p) {
		return nil
	}
	return []Candidate{{Port: p, Command: "go run", Tool: "go", Source: rel, Explicit: true}}
}

func eachLine(path string, fn func(string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fn(s.Text())
	}
}

// dedupe keeps one candidate per port: an explicit port beats a default, then the shorter source path.
func dedupe(in []Candidate) []Candidate {
	best := map[int]Candidate{}
	for _, c := range in {
		cur, seen := best[c.Port]
		if !seen || better(c, cur) {
			best[c.Port] = c
		}
	}
	out := make([]Candidate, 0, len(best))
	for _, c := range best {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func better(a, b Candidate) bool {
	if a.Explicit != b.Explicit {
		return a.Explicit
	}
	if la, lb := len(a.Source), len(b.Source); la != lb {
		return la < lb
	}
	return a.Command < b.Command
}

// bindOf is the catalog's bind args for a tool name.
func bindOf(toolName string) string {
	for _, t := range catalog {
		if t.name == toolName {
			return t.bind
		}
	}
	return ""
}

var bindsAll = regexp.MustCompile(`0\.0\.0\.0|::`)

// Launch is the shell command that starts c's server on every interface, and the
// directory, relative to root, it runs in.
func Launch(c Candidate, root string) (dir, cmd string) {
	dir = filepath.Dir(c.Source)
	line := c.Line
	bind := bindOf(c.Tool)
	if bindsAll.MatchString(line) {
		bind = ""
	}
	env := "HOST=0.0.0.0 PORT=" + strconv.Itoa(c.Port) + " "
	switch filepath.Base(c.Source) {
	case "package.json":
		pm := packageManager(filepath.Join(root, dir), root)
		run := pm + " run " + shellQuote(c.Command)
		if bind != "" {
			sep := " "
			if pm == "npm" {
				sep = " -- "
			}
			run += sep + bind
		}
		return dir, env + run
	case "mise.toml", ".mise.toml":
		run := "mise run " + shellQuote(strings.TrimPrefix(c.Command, "tasks."))
		if bind != "" {
			run += " -- " + bind
		}
		return dir, env + run
	case "Makefile":
		return dir, env + "make " + shellQuote(c.Command)
	case "manage.py":
		return dir, env + "python3 manage.py runserver 0.0.0.0:" + strconv.Itoa(c.Port)
	case "main.go":
		return dir, env + "go run ."
	}
	if bind != "" {
		line += " " + bind
	}
	return dir, env + line
}

// packageManager walks from dir up to root for a lockfile.
func packageManager(dir, root string) string {
	for {
		for _, lf := range []struct{ file, pm string }{
			{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lockb", "bun"}, {"bun.lock", "bun"},
		} {
			if _, err := os.Stat(filepath.Join(dir, lf.file)); err == nil {
				return lf.pm
			}
		}
		if dir == root || dir == filepath.Dir(dir) {
			return "npm"
		}
		dir = filepath.Dir(dir)
	}
}

func shellQuote(s string) string {
	if regexp.MustCompile(`^[\w:./@-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
