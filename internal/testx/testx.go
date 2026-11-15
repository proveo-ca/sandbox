// SPEC: _spec/tests/00-test-fixtures.puml

// Package testx reads one txtar archive per fixture family; `go test ./<pkg> -update` rewrites it.
package testx

import (
	"flag"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/txtar"
)

// Update records every golden the run's owning tests draw: `go test ./<pkg> -update`.
var Update = flag.Bool("update", false, "rewrite the fixture archives from what the code produces now")

// Goldens is one archive of named goldens, owned by one test function.
type Goldens struct {
	path    string
	mu      sync.Mutex
	t       testing.TB
	owner   string
	closed  bool
	comment []byte
	want    map[string][]byte
	seen    map[string]bool
}

var (
	registryMu sync.Mutex
	registry   = map[string]*Goldens{}
)

// archivePath is testdata/<name>.txtar, or name itself when it ends in .txtar.
func archivePath(name string) string {
	if strings.HasSuffix(name, ".txtar") {
		return name
	}
	return filepath.Join("testdata", name+".txtar")
}

// load parses an archive once per test binary.
func load(t testing.TB, name string) *Goldens {
	t.Helper()
	path, err := filepath.Abs(archivePath(name))
	if err != nil {
		t.Fatal(err)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if g, ok := registry[path]; ok {
		return g
	}
	data, err := os.ReadFile(path)
	if err != nil && (!*Update || !os.IsNotExist(err)) {
		t.Fatalf("%v; `go test -update` records it", err)
	}
	ar := txtar.Parse(data)
	g := &Goldens{path: path, comment: ar.Comment, want: map[string][]byte{}, seen: map[string]bool{}}
	for _, f := range ar.Files {
		g.want[f.Name] = f.Data
	}
	registry[path] = g
	return g
}

// Open owns testdata/<name>.txtar for t: Assert names its entries, and close runs when t ends.
func Open(t testing.TB, name string) *Goldens {
	t.Helper()
	g := load(t, name)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.owner != "" && (g.owner != t.Name() || !g.closed) {
		t.Fatalf("%s is owned by %s: one archive per test function; Want reads an entry without owning it", g.path, g.owner)
	}
	g.t, g.owner, g.closed, g.seen = t, t.Name(), false, map[string]bool{}
	t.Cleanup(g.close)
	return g
}

// Assert holds got to the entry named name, or records it under -update.
func (g *Goldens) Assert(name, got string) {
	g.t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.seen[name]:
		g.t.Errorf("two cases write %s", name)
		return
	case got != "" && !strings.HasSuffix(got, "\n"):
		g.t.Errorf("%s: a golden ends with a newline; txtar adds one, so -update would never settle", name)
		return
	case markerShaped(got):
		g.t.Errorf("%s: a line shaped like a txtar marker (-- x --) would split the archive", name)
		return
	}
	g.seen[name] = true
	if *Update {
		g.want[name] = []byte(got)
		return
	}
	want, ok := g.want[name]
	if !ok {
		g.t.Errorf("%s: no golden in %s; record it with -update", name, g.path)
		return
	}
	if got != string(want) {
		g.t.Errorf("%s differs from its golden:\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}

// markerShaped reports whether a line of got would parse as a txtar file marker.
func markerShaped(got string) bool {
	for _, line := range strings.Split(got, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if len(line) >= 6 && strings.HasPrefix(line, "-- ") && strings.HasSuffix(line, " --") {
			return true
		}
	}
	return false
}

// close writes the entries the owner drew, sorted, under -update; otherwise it reports the entries no case drew.
func (g *Goldens) close() {
	g.t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	if !*Update {
		for _, name := range slices.Sorted(maps.Keys(g.want)) {
			if !g.seen[name] {
				g.t.Errorf("%s in %s is a golden no case draws; delete it with -update", name, g.path)
			}
		}
		return
	}
	if g.t.Failed() {
		return
	}
	ar := &txtar.Archive{Comment: g.comment}
	for _, name := range slices.Sorted(maps.Keys(g.want)) {
		if !g.seen[name] {
			delete(g.want, name)
			continue
		}
		ar.Files = append(ar.Files, txtar.File{Name: name, Data: g.want[name]})
	}
	if err := os.WriteFile(g.path, txtar.Format(ar), 0o644); err != nil {
		g.t.Error(err)
	}
}

// Want is one entry of testdata/<archive>.txtar, read by a test that owns none of it.
func Want(t testing.TB, archive, entry string) string {
	t.Helper()
	g := load(t, archive)
	g.mu.Lock()
	defer g.mu.Unlock()
	want, ok := g.want[entry]
	if !ok {
		t.Fatalf("%s has no entry %s", g.path, entry)
	}
	return string(want)
}

// Extract writes testdata/<name>.txtar into a fresh t.TempDir() and returns that directory.
func Extract(t testing.TB, name string) string {
	t.Helper()
	dir := t.TempDir()
	extract(t, name, dir)
	return dir
}

// ExtractTo writes testdata/<name>.txtar under testdata/out/<base>, emptied first, and returns that directory.
func ExtractTo(t testing.TB, name string) string {
	t.Helper()
	dir := filepath.Join("testdata", "out", strings.TrimSuffix(filepath.Base(archivePath(name)), ".txtar"))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	extract(t, name, dir)
	return dir
}

// extract mounts the archive with txtar.FS, so every entry name is a valid path under dir.
func extract(t testing.TB, name, dir string) {
	t.Helper()
	g := load(t, name)
	g.mu.Lock()
	ar := &txtar.Archive{}
	for _, n := range slices.Sorted(maps.Keys(g.want)) {
		ar.Files = append(ar.Files, txtar.File{Name: n, Data: g.want[n]})
	}
	g.mu.Unlock()
	fsys, err := txtar.FS(ar)
	if err != nil {
		t.Fatalf("%s: %v", g.path, err)
	}
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatalf("%s: %v", g.path, err)
	}
}
