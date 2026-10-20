//go:build !windows

// SPEC: _spec/defs/opencode/engine-local-history.puml
package sandbox

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

const ocEngine = "proveo-opencode-1a2b3c4d"

// fakeEngine is an sbx on PATH with one sandbox, engine, whose guest home is guest/home.
// state/exists marks the engine present; state/create-fails, state/exec-fails and
// state/mutate (it names a host file that grows as the import starts) inject failures.
type fakeEngine struct {
	calls, state, guest string
}

func newFakeEngine(t *testing.T, exists bool) fakeEngine {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	root := t.TempDir()
	f := fakeEngine{calls: filepath.Join(root, "calls"), state: filepath.Join(root, "state"), guest: filepath.Join(root, "guest")}
	for _, d := range []string{f.state, filepath.Join(f.guest, "tmp"), filepath.Join(f.guest, "home")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if exists {
		f.set(t, "exists")
	}
	script := `#!/bin/sh
echo "$*" >> '` + f.calls + `'
S='` + f.state + `'; G='` + f.guest + `'
case "$1" in
  ls) printf 'SANDBOX AGENT STATUS\n'; [ -e "$S/exists" ] && printf '` + ocEngine + ` opencode stopped\n'; exit 0 ;;
  create) [ -e "$S/create-fails" ] && exit 1; touch "$S/exists" ;;
  rm) rm -f "$S/exists" ;;
  cp) echo 'sbx cp: the history import must not stage files' >&2; exit 1 ;;
  exec) shift; I=; while [ "$1" != "--" ]; do [ "$1" = -i ] && I=1; shift; done; shift
    [ "$1" = python3 ] || exit 0
    [ -e "$S/exec-fails" ] && { echo 'sbx: exec failed' >&2; exit 1; }
    [ -s "$S/mutate" ] && echo grown >> "$(cat "$S/mutate")"
    [ -n "$I" ] || exec </dev/null
    exec env -u XDG_DATA_HOME -u OPENCODE_DB HOME="$G/home" TMPDIR="$G/tmp" '` + py + `' -I -c "$4" ;;
esac
`
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, sbx.Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	orig := receiptDir
	t.Cleanup(func() { receiptDir = orig })
	receipts := filepath.Join(root, "receipts")
	receiptDir = func() string { return receipts }
	return f
}

func (f fakeEngine) set(t *testing.T, flag string) {
	t.Helper()
	f.setTo(t, flag, "")
}

func (f fakeEngine) setTo(t *testing.T, flag, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.state, flag), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f fakeEngine) exists() bool {
	_, err := os.Stat(filepath.Join(f.state, "exists"))
	return err == nil
}

func (f fakeEngine) log() string {
	b, _ := os.ReadFile(f.calls)
	return string(b)
}

// guestDB is the engine's own OpenCode database.
func (f fakeEngine) guestDB() string {
	return filepath.Join(f.guest, "home", ".local", "share", "opencode", "opencode.db")
}

// writeDB writes a WAL-mode database at path holding one session, id.
func writeDB(t *testing.T, path, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	script := `import os, sqlite3, sys
con = sqlite3.connect(sys.argv[1])
con.execute("PRAGMA journal_mode=WAL")
con.execute("PRAGMA wal_autocheckpoint=0")
con.execute("CREATE TABLE session (id TEXT)")
con.execute("INSERT INTO session VALUES (?)", (sys.argv[2],))
con.commit()
os._exit(0)`
	if out, err := exec.Command("python3", "-I", "-c", script, path, id).CombinedOutput(); err != nil {
		t.Fatalf("write %s: %v\n%s", path, err, out)
	}
}

// sessionsIn reads every session id in the database at path; "" when it holds none.
func sessionsIn(t *testing.T, path string) string {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	out, err := exec.Command("python3", "-I", "-c",
		`import sqlite3, sys; print(",".join(r[0] for r in sqlite3.connect(sys.argv[1]).execute("SELECT id FROM session ORDER BY id")))`,
		path).CombinedOutput()
	if err != nil {
		t.Fatalf("read %s: %v\n%s", path, err, out)
	}
	return strings.TrimSpace(string(out))
}

// snapshotTree is every regular file under root with its bytes.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			files[p] = string(b)
		}
		return nil
	})
	return files
}

func openCodeInput(home string) Input {
	return Input{Target: "opencode", HomeRoot: home}
}

func TestAnExistingEngineKeepsItsOwnHistory(t *testing.T) {
	f := newFakeEngine(t, true)
	home := t.TempDir()
	writeDB(t, filepath.Join(home, "opencode", "v2", "share", "opencode.db"), "ses_share")

	launch, created, err := ensureOpenCodeEngine(openCodeInput(home), sbx.RunConfig{Name: ocEngine, KitDir: "/kit"})
	if err != nil || created {
		t.Fatalf("ensureOpenCodeEngine = created %v, %v; want a plain reattach", created, err)
	}
	if launch.KitDir != "" || launch.Name != ocEngine {
		t.Errorf("launch = %+v, want sbx.Reattach of the engine", launch)
	}
	if log := f.log(); strings.Contains(log, "create") || strings.Contains(log, "cp ") || strings.Contains(log, "python3") {
		t.Errorf("an existing engine was created or imported into again:\n%s", log)
	}
}

func TestANewEngineImportsTheFirstHostDatabaseOnce(t *testing.T) {
	for _, c := range []struct {
		name      string
		dbs       map[string]string
		symlink   string
		want      string
		wantCarry bool
	}{
		{"no host history starts empty", nil, "", "", false},
		{"the V2 share", map[string]string{"opencode/v2/share/opencode.db": "ses_share", "opencode/share/opencode.db": "ses_v1"}, "", "ses_share", false},
		{"the legacy share when no V2 one exists", map[string]string{"opencode/share/opencode.db": "ses_v1"}, "", "ses_v1", false},
		{"a carried engine database outranks the share", map[string]string{"opencode/carry/" + ocEngine + ".db": "ses_carry", "opencode/v2/share/opencode.db": "ses_share"}, "", "ses_carry", true},
		{"a symlinked database is skipped", map[string]string{"opencode/share/opencode.db": "ses_v1"}, "opencode/v2/share/opencode.db", "ses_v1", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeEngine(t, false)
			home := t.TempDir()
			for rel, id := range c.dbs {
				writeDB(t, filepath.Join(home, filepath.FromSlash(rel)), id)
			}
			if c.symlink != "" {
				elsewhere := filepath.Join(t.TempDir(), "opencode.db")
				writeDB(t, elsewhere, "ses_symlink")
				link := filepath.Join(home, filepath.FromSlash(c.symlink))
				if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(elsewhere, link); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotTree(t, filepath.Join(home, "opencode", "v2"))
			before1 := snapshotTree(t, filepath.Join(home, "opencode", "share"))

			launch, created, err := ensureOpenCodeEngine(openCodeInput(home), sbx.RunConfig{Name: ocEngine, KitDir: "/kit"})
			if err != nil || !created || launch.KitDir != "" {
				t.Fatalf("ensureOpenCodeEngine = %+v, created %v, %v; want a created engine reattached", launch, created, err)
			}
			if got := sessionsIn(t, f.guestDB()); got != c.want {
				t.Errorf("the new engine holds %q, want %q\ncalls:\n%s", got, c.want, f.log())
			}
			if after := snapshotTree(t, filepath.Join(home, "opencode", "v2")); len(after) != len(before) {
				t.Errorf("the import changed the host V2 share: %d files before, %d after", len(before), len(after))
			}
			for p, b := range before1 {
				if a, _ := os.ReadFile(p); string(a) != b {
					t.Errorf("the import changed host file %s", p)
				}
			}
			_, err = os.Stat(filepath.Join(home, "opencode", "carry", ocEngine+".db"))
			if c.wantCarry && err == nil {
				t.Error("the carried database is still on the host after a successful import")
			}
			if left, _ := os.ReadDir(filepath.Join(f.guest, "tmp")); len(left) != 0 {
				t.Errorf("the import left %d entries in the engine's /tmp", len(left))
			}
			if log := f.log(); c.want != "" && !strings.Contains(log, "exec -i -w / "+ocEngine+" --") {
				t.Errorf("the import did not stream over `sbx exec -i`:\n%s", log)
			}
		})
	}
}

func TestAFailedImportRemovesTheFreshEngineAndKeepsTheSource(t *testing.T) {
	for _, c := range []struct {
		name    string
		flag    string
		bytes   string
		wantErr string
	}{
		{"a corrupt database", "", "not a sqlite database at all, but long enough", "fails SQLite's quick_check"},
		{"a source that changes while it streams", "mutate", strings.Repeat("x", 4<<20), "changed while it was copied"},
		{"an exec that fails", "exec-fails", "", "sbx: exec failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeEngine(t, false)
			home := t.TempDir()
			carry := filepath.Join(home, "opencode", "carry", ocEngine+".db")
			if c.flag != "" {
				f.setTo(t, c.flag, carry)
			}
			if c.bytes != "" {
				if err := os.MkdirAll(filepath.Dir(carry), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(carry, []byte(c.bytes), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				writeDB(t, carry, "ses_carry")
			}
			writeReceipt(ocEngine, launchReceipt{Agent: "opencode"})

			_, _, err := ensureOpenCodeEngine(openCodeInput(home), sbx.RunConfig{Name: ocEngine, KitDir: "/kit"})
			if err == nil || !strings.Contains(err.Error(), "next") || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("ensureOpenCodeEngine = %v, want an error naming %q that says the next run retries", err, c.wantErr)
			}
			if _, err := os.Stat(f.guestDB()); err == nil {
				t.Error("a failed import installed an engine database")
			}
			if f.exists() {
				t.Errorf("the fresh engine survived a failed import, so the next run would reattach to it empty\ncalls:\n%s", f.log())
			}
			if readReceipt(ocEngine) != nil {
				t.Error("the fresh engine's receipt survived a failed import")
			}
			if _, err := os.Stat(carry); err != nil {
				t.Errorf("the carried database is gone after a failed import: %v", err)
			}
		})
	}
}

func TestRetiringAnOpenCodeEngineCarriesItsDatabase(t *testing.T) {
	for _, c := range []struct {
		name       string
		guest      string
		flag       string
		wantCarry  string
		wantErr    bool
		wantRemove bool
	}{
		{"the engine's history is carried", "ses_engine", "", "ses_engine", false, true},
		{"an engine with no database carries nothing", "", "", "", false, true},
		{"an export that fails keeps the engine", "ses_engine", "exec-fails", "", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeEngine(t, true)
			if c.guest != "" {
				writeDB(t, f.guestDB(), c.guest)
			}
			if c.flag != "" {
				f.set(t, c.flag)
			}
			home := t.TempDir()
			legacy := legacyOwnerRecord(ocEngine)
			if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacy, []byte(`{"cache_owner":"1:2"}`), 0o600); err != nil {
				t.Fatal(err)
			}

			err := retireSandbox(openCodeInput(home), sbx.RunConfig{Name: ocEngine})
			if (err != nil) != c.wantErr {
				t.Fatalf("retireSandbox = %v, want error %v\ncalls:\n%s", err, c.wantErr, f.log())
			}
			if f.exists() == c.wantRemove {
				t.Errorf("engine present after retire = %v, want %v", f.exists(), !c.wantRemove)
			}
			carry := filepath.Join(home, "opencode", "carry", ocEngine+".db")
			if got := sessionsIn(t, carry); got != c.wantCarry {
				t.Errorf("carried database holds %q, want %q", got, c.wantCarry)
			}
			if b, err := os.ReadFile(carry); err == nil && !bytes.HasPrefix(b, []byte(sbx.OpenCodeDatabaseHeader)) {
				t.Error("the carried file is not a SQLite database")
			}
			if _, err := os.Stat(legacy); c.wantRemove && err == nil {
				t.Error("the legacy keeper's owner record survived the engine's retirement")
			}
		})
	}
}
