// SPEC: _spec/tests/00-test-fixtures.puml
package testx

import (
	"bytes"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/txtar"
)

// stage writes testdata/<name>.txtar from files in a temp cwd and returns the archive path.
func stage(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	ar := &txtar.Archive{}
	for _, n := range slices.Sorted(maps.Keys(files)) {
		ar.Files = append(ar.Files, txtar.File{Name: n, Data: []byte(files[n])})
	}
	path := filepath.Join(dir, "testdata", name+".txtar")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, txtar.Format(ar), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func setUpdate(t *testing.T, on bool) {
	t.Helper()
	was := *Update
	*Update = on
	t.Cleanup(func() { *Update = was })
}

// recTB records what the helper reports on a test it never ends.
type recTB struct {
	testing.TB
	name     string
	errors   []string
	fatal    string
	cleanups []func()
}

type fatalMark struct{}

func (r *recTB) Helper()        {}
func (r *recTB) Name() string   { return r.name }
func (r *recTB) Failed() bool   { return len(r.errors) > 0 || r.fatal != "" }
func (r *recTB) Error(a ...any) { r.errors = append(r.errors, fmt.Sprint(a...)) }
func (r *recTB) Errorf(format string, a ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, a...))
}
func (r *recTB) Fatal(a ...any) { r.fatal = fmt.Sprint(a...); panic(fatalMark{}) }
func (r *recTB) Fatalf(format string, a ...any) {
	r.fatal = fmt.Sprintf(format, a...)
	panic(fatalMark{})
}
func (r *recTB) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

// end runs the cleanups in reverse, as the test runner would at the test's end.
func (r *recTB) end() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
	r.cleanups = nil
}

// fatally runs f and returns what it reported through Fatal, or "".
func fatally(r *recTB, f func()) (msg string) {
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(fatalMark); !ok {
				panic(p)
			}
			msg = r.fatal
		}
	}()
	f()
	return ""
}

func TestAssertHoldsEveryEntryAndCloseFindsTheOrphans(t *testing.T) {
	setUpdate(t, false)
	stage(t, "frames", map[string]string{"a.txt": "a\n", "b.txt": "b\n", "c.txt": "c\n"})
	rec := &recTB{TB: t, name: "TestOwner"}
	g := Open(rec, "frames")
	g.Assert("a.txt", "a\n")
	g.Assert("b.txt", "changed\n")
	rec.end()
	if len(rec.errors) != 2 || !strings.Contains(rec.errors[0], "b.txt differs") || !strings.Contains(rec.errors[1], "c.txt in") {
		t.Errorf("Assert + close reported %q, want b.txt differs and c.txt is an orphan", rec.errors)
	}
}

func TestUpdateWritesTheOwnersEntriesSortedAndSettles(t *testing.T) {
	setUpdate(t, true)
	path := stage(t, "frames", map[string]string{"b.txt": "old\n", "orphan.txt": "o\n"})
	draw := func(rec *recTB) {
		g := Open(rec, "frames")
		g.Assert("c.txt", "c\n")
		g.Assert("a.txt", "a\n")
		g.Assert("b.txt", "new\n")
		rec.end()
	}
	first := &recTB{TB: t, name: "TestOwner"}
	draw(first)
	written, err := os.ReadFile(path)
	if err != nil || len(first.errors) != 0 {
		t.Fatalf("-update wrote nothing: %v %q", err, first.errors)
	}
	if want := "-- a.txt --\na\n-- b.txt --\nnew\n-- c.txt --\nc\n"; string(written) != want {
		t.Errorf("-update wrote:\n%s\nwant:\n%s", written, want)
	}
	second := &recTB{TB: t, name: "TestOwner"}
	draw(second)
	again, _ := os.ReadFile(path)
	if !bytes.Equal(written, again) || len(second.errors) != 0 {
		t.Errorf("a second -update run changed the archive:\n%s\nerrors %q", again, second.errors)
	}
	setUpdate(t, false)
	third := &recTB{TB: t, name: "TestOwner"}
	draw(third)
	if len(third.errors) != 0 {
		t.Errorf("the recorded archive does not hold what -update drew: %q", third.errors)
	}
}

func TestASecondOwnerOfAnArchiveFails(t *testing.T) {
	setUpdate(t, false)
	stage(t, "frames", map[string]string{"a.txt": "a\n"})
	first := &recTB{TB: t, name: "TestFirst"}
	g := Open(first, "frames")
	g.Assert("a.txt", "a\n")
	second := &recTB{TB: t, name: "TestSecond"}
	if msg := fatally(second, func() { Open(second, "frames") }); !strings.Contains(msg, "owned by TestFirst") {
		t.Errorf("a second Open reported %q, want the first owner named", msg)
	}
	if msg := fatally(first, func() { Open(first, "frames") }); !strings.Contains(msg, "owned by TestFirst") {
		t.Errorf("a second Open by the owner before its close reported %q", msg)
	}
	first.end()
	rerun := &recTB{TB: t, name: "TestFirst"}
	g = Open(rerun, "frames")
	g.Assert("a.txt", "a\n")
	rerun.end()
	if len(first.errors)+len(rerun.errors) != 0 {
		t.Errorf("the owner's rerun after close reported %q %q", first.errors, rerun.errors)
	}
}

func TestAssertRejectsAGoldenTxtarCannotHold(t *testing.T) {
	setUpdate(t, true)
	path := stage(t, "frames", map[string]string{"a.txt": "a\n"})
	before, _ := os.ReadFile(path)
	rec := &recTB{TB: t, name: "TestOwner"}
	g := Open(rec, "frames")
	g.Assert("a.txt", "a\n")
	g.Assert("a.txt", "a\n")
	g.Assert("nonl.txt", "no newline at the end")
	g.Assert("marker.txt", "one\n-- looks like a marker --\nthree\n")
	rec.end()
	after, _ := os.ReadFile(path)
	if len(rec.errors) != 3 || !bytes.Equal(before, after) {
		t.Errorf("the guards reported %q and left the archive %q", rec.errors, after)
	}
	for i, want := range []string{"two cases write", "ends with a newline", "shaped like a txtar marker"} {
		if i < len(rec.errors) && !strings.Contains(rec.errors[i], want) {
			t.Errorf("guard %d reported %q, want %q", i, rec.errors[i], want)
		}
	}
}

func TestWantReadsAnEntryWithoutOwning(t *testing.T) {
	setUpdate(t, false)
	stage(t, "checklist", map[string]string{"p1-rest_80x24.txt": "rest\n"})
	if got := Want(t, "checklist", "p1-rest_80x24.txt"); got != "rest\n" {
		t.Errorf("Want = %q, want rest", got)
	}
	rec := &recTB{TB: t, name: "TestOwner"}
	Open(rec, "checklist").Assert("p1-rest_80x24.txt", "rest\n")
	rec.end()
	missing := &recTB{TB: t, name: "TestReader"}
	if msg := fatally(missing, func() { Want(missing, "checklist", "nope.txt") }); !strings.Contains(msg, "no entry nope.txt") {
		t.Errorf("Want of a missing entry reported %q", msg)
	}
	if len(rec.errors) != 0 {
		t.Errorf("Want took ownership from the owner: %q", rec.errors)
	}
}

func TestExtractRoundTripsATree(t *testing.T) {
	setUpdate(t, false)
	files := map[string]string{"a-b/x.txt": "x\n", "a/y.txt": "y\n", "top.txt": "top\n", ".hidden": "h\n", "a/deep/z.sh": "#!/bin/sh\n"}
	path := stage(t, "tree", files)
	for _, dir := range []string{Extract(t, "tree"), Extract(t, path), ExtractTo(t, "tree")} {
		for name, want := range files {
			got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
			if err != nil || string(got) != want {
				t.Errorf("%s/%s = %q, %v; want %q", dir, name, got, err, want)
			}
		}
	}
	stale := filepath.Join("testdata", "out", "tree", "stale.txt")
	if err := os.WriteFile(stale, []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dir := ExtractTo(t, "tree"); dir != filepath.Join("testdata", "out", "tree") {
		t.Errorf("ExtractTo = %q, want testdata/out/tree", dir)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("ExtractTo kept a stale file from the last extraction")
	}
}

// narrow sets one of the test binary's own flags for the test's duration.
func narrow(t *testing.T, name, value string) {
	t.Helper()
	was := flag.Lookup(name).Value.String()
	if err := flag.Set(name, value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flag.Set(name, was) })
}

func TestARunNarrowedToASubtestNeitherReportsNorPrunesTheSiblings(t *testing.T) {
	for _, name := range []string{"test.run", "test.skip"} {
		t.Run(name, func(t *testing.T) {
			setUpdate(t, false)
			path := stage(t, "plan", map[string]string{"a.golden": "a\n", "b.golden": "b\n"})
			narrow(t, name, "TestOwner/a$")
			rec := &recTB{TB: t, name: "TestOwner"}
			Open(rec, "plan").Assert("a.golden", "a\n")
			rec.end()
			if len(rec.errors) != 0 {
				t.Errorf("the narrowed run reported %q; b.golden is no orphan", rec.errors)
			}
			setUpdate(t, true)
			rec = &recTB{TB: t, name: "TestOwner"}
			Open(rec, "plan").Assert("a.golden", "new\n")
			rec.end()
			after, _ := os.ReadFile(path)
			if want := "-- a.golden --\nnew\n-- b.golden --\nb\n"; string(after) != want || len(rec.errors) != 0 {
				t.Errorf("the narrowed -update wrote:\n%s\nerrors %q; want a.golden recorded and b.golden kept", after, rec.errors)
			}
		})
	}
}

func TestAMissingArchiveIsFatalUnlessAnOwnerRecordsIt(t *testing.T) {
	setUpdate(t, true)
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir(filepath.Join(dir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	reader := &recTB{TB: t, name: "TestReader"}
	if msg := fatally(reader, func() { Want(reader, "gone", "a.txt") }); !strings.Contains(msg, "gone.txtar") {
		t.Errorf("Want of a missing archive under -update reported %q, want the file named", msg)
	}
	if msg := fatally(reader, func() { Extract(reader, "gone") }); !strings.Contains(msg, "gone.txtar") {
		t.Errorf("Extract of a missing archive under -update reported %q, want the file named", msg)
	}
	owner := &recTB{TB: t, name: "TestOwner"}
	Open(owner, "gone").Assert("a.txt", "a\n")
	owner.end()
	written, err := os.ReadFile(filepath.Join(dir, "testdata", "gone.txtar"))
	if err != nil || string(written) != "-- a.txt --\na\n" || len(owner.errors) != 0 {
		t.Errorf("an owner under -update recorded %q, %v, errors %q; want the archive born", written, err, owner.errors)
	}
}
