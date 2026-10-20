// SPEC: _spec/defs/opencode/engine-local-history.puml
package sbx

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// python is the host python3 the engine scripts run under in these tests.
func python(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	return p
}

// engineEnv is a clean environment whose HOME is home, as `sbx exec` gives the engine user.
func engineEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_DATA_HOME=") && !strings.HasPrefix(kv, "OPENCODE_DB=") {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+home)
}

// runScript runs an engine script on stdin under its own TMPDIR, tmp, and returns its stdout,
// stderr and exit status.
func runScript(t *testing.T, home, tmp, script string, stdin []byte) ([]byte, string, int) {
	t.Helper()
	cmd := exec.Command(python(t), "-I", "-c", script)
	cmd.Env = append(engineEnv(home), "TMPDIR="+tmp)
	cmd.Dir = t.TempDir()
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.Bytes(), errb.String(), 0
	case errors.As(err, &ee):
		return out.Bytes(), errb.String(), ee.ExitCode()
	}
	t.Fatal(err)
	return nil, "", -1
}

// walDatabase writes a WAL-mode database at path whose one row, value, lives only in its -wal.
func walDatabase(t *testing.T, path, value string) {
	t.Helper()
	script := `import os, sqlite3, sys
con = sqlite3.connect(sys.argv[1])
con.execute("PRAGMA journal_mode=WAL")
con.execute("PRAGMA wal_autocheckpoint=0")
con.execute("CREATE TABLE session (id TEXT)")
con.commit()
con.execute("INSERT INTO session VALUES (?)", (sys.argv[2],))
con.commit()
os._exit(0)`
	if out, err := exec.Command(python(t), "-I", "-c", script, path, value).CombinedOutput(); err != nil {
		t.Fatalf("seed %s: %v\n%s", path, err, out)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("the fixture left no -wal beside %s, so it would not prove the import folds one: %v", path, err)
	}
}

// sessions reads every session id in the database at path.
func sessions(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command(python(t), "-I", "-c",
		`import sqlite3, sys; print(",".join(r[0] for r in sqlite3.connect(sys.argv[1]).execute("SELECT id FROM session ORDER BY id")))`,
		path).CombinedOutput()
	if err != nil {
		t.Fatalf("read %s: %v\n%s", path, err, out)
	}
	return strings.TrimSpace(string(out))
}

// stream is the import stream WriteOpenCodeImport writes for the host database src.
func stream(t *testing.T, src string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := WriteOpenCodeImport(&b, src, OpenCodeImportFiles(src)); err != nil {
		t.Fatalf("WriteOpenCodeImport(%s) = %v", src, err)
	}
	return b.Bytes()
}

// tarOf is a tar of {name, body} members in order.
func tarOf(t *testing.T, members ...[2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, m := range members {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: m[0], Mode: 0o600, Size: int64(len(m[1]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(m[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// leftovers lists what an engine script left in its TMPDIR.
func leftovers(t *testing.T, tmp string) []string {
	t.Helper()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// appendOnWrite appends to path on its first Write, as an OpenCode writing the source mid-stream.
type appendOnWrite struct {
	io.Writer
	path string
	done bool
}

func (a *appendOnWrite) Write(p []byte) (int, error) {
	if !a.done {
		a.done = true
		f, err := os.OpenFile(a.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			return 0, err
		}
		_, _ = f.WriteString("grown")
		_ = f.Close()
	}
	return a.Writer.Write(p)
}

func TestTheOpenCodeImportInstallsTheHostDatabaseOnce(t *testing.T) {
	t.Parallel()
	host := filepath.Join(t.TempDir(), "opencode.db")
	walDatabase(t, host, "ses_host")
	home, tmp := t.TempDir(), t.TempDir()
	target := filepath.Join(home, ".local", "share", "opencode", "opencode.db")

	if _, errOut, code := runScript(t, home, tmp, OpenCodeImportScript, stream(t, host)); code != 0 {
		t.Fatalf("import = exit %d, want 0\n%s", code, errOut)
	}
	if got := sessions(t, target); got != "ses_host" {
		t.Errorf("the engine database holds %q, want the session that lived only in the host -wal", got)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("engine database mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	if left := leftovers(t, tmp); len(left) != 0 {
		t.Errorf("the import left %v in the engine's temp dir", left)
	}

	other := filepath.Join(t.TempDir(), "opencode.db")
	walDatabase(t, other, "ses_other")
	if _, _, code := runScript(t, home, tmp, OpenCodeImportScript, stream(t, other)); code != OpenCodeImportExisting {
		t.Errorf("a second import = exit %d, want %d: the engine's own history must never be overwritten", code, OpenCodeImportExisting)
	}
	if got := sessions(t, target); got != "ses_host" {
		t.Errorf("after a refused second import the engine holds %q, want ses_host", got)
	}
}

func TestTheOpenCodeImportRefusesACorruptCopy(t *testing.T) {
	t.Parallel()
	home, tmp := t.TempDir(), t.TempDir()
	host := filepath.Join(t.TempDir(), "opencode.db")
	if err := os.WriteFile(host, []byte("not a database, just bytes long enough to look like one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runScript(t, home, tmp, OpenCodeImportScript, stream(t, host)); code != OpenCodeImportCorrupt {
		t.Errorf("import of a corrupt copy = exit %d, want %d", code, OpenCodeImportCorrupt)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "opencode", "opencode.db")); err == nil {
		t.Error("a corrupt copy was installed as the engine database")
	}
	if left := leftovers(t, tmp); len(left) != 0 {
		t.Errorf("the import left %v in the engine's temp dir", left)
	}
}

func TestTheOpenCodeImportInstallsNothingWithoutTheCommit(t *testing.T) {
	t.Parallel()
	host := filepath.Join(t.TempDir(), "opencode.db")
	walDatabase(t, host, "ses_host")
	whole := stream(t, host)
	db, err := os.ReadFile(host)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		stdin []byte
	}{
		{"no stdin", nil},
		{"a stream cut before the commit", tarOf(t, [2]string{"opencode.db", string(db)})},
		{"a stream cut inside a file", whole[:len(whole)/2]},
		{"a member outside the database files", tarOf(t, [2]string{"../opencode.db", string(db)}, [2]string{OpenCodeImportCommit, ""})},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			home, tmp := t.TempDir(), t.TempDir()
			if _, errOut, code := runScript(t, home, tmp, OpenCodeImportScript, c.stdin); code != OpenCodeImportIncomplete {
				t.Errorf("import = exit %d, want %d\n%s", code, OpenCodeImportIncomplete, errOut)
			}
			if _, err := os.Stat(filepath.Join(home, ".local", "share", "opencode", "opencode.db")); err == nil {
				t.Error("an uncommitted stream was installed as the engine database")
			}
			if left := leftovers(t, tmp); len(left) != 0 {
				t.Errorf("the import left %v in the engine's temp dir", left)
			}
		})
	}
}

func TestWriteOpenCodeImportWithholdsTheCommitFromAChangingSource(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, grows string }{
		{"the database grows", ""},
		{"its -wal grows", "-wal"},
		{"a -journal appears", "-journal"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			host := filepath.Join(t.TempDir(), "opencode.db")
			walDatabase(t, host, "ses_host")
			files := OpenCodeImportFiles(host)
			var b bytes.Buffer
			err := WriteOpenCodeImport(&appendOnWrite{Writer: &b, path: host + c.grows}, host, files)
			if !errors.Is(err, ErrOpenCodeImportChanged) {
				t.Fatalf("WriteOpenCodeImport = %v, want ErrOpenCodeImportChanged", err)
			}
			if bytes.Contains(b.Bytes(), []byte(OpenCodeImportCommit+"\x00")) {
				t.Error("the stream of a changing source carries the commit member")
			}
			if _, _, code := runScript(t, t.TempDir(), t.TempDir(), OpenCodeImportScript, b.Bytes()); code != OpenCodeImportIncomplete {
				t.Errorf("import of the withheld stream = exit %d, want %d", code, OpenCodeImportIncomplete)
			}
		})
	}
}

func TestTheOpenCodeExportStreamsAConsistentCopy(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if _, _, code := runScript(t, home, t.TempDir(), OpenCodeExportScript, nil); code != OpenCodeExportNone {
		t.Errorf("export from an engine with no database = exit %d, want %d", code, OpenCodeExportNone)
	}
	db := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	walDatabase(t, db, "ses_engine")
	out, errOut, code := runScript(t, home, t.TempDir(), OpenCodeExportScript, nil)
	if code != 0 {
		t.Fatalf("export = exit %d\n%s", code, errOut)
	}
	if !bytes.HasPrefix(out, []byte(OpenCodeDatabaseHeader)) {
		t.Fatalf("export streamed %d bytes without the SQLite header", len(out))
	}
	carried := filepath.Join(t.TempDir(), "carried.db")
	if err := os.WriteFile(carried, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := sessions(t, carried); got != "ses_engine" {
		t.Errorf("the carried copy holds %q, want the session that lived only in the engine's -wal", got)
	}
}

func TestOpenCodeHistoryArgsTargetTheEngine(t *testing.T) {
	t.Parallel()
	imp := OpenCodeImportArgs("eng")
	if strings.Join(imp[:6], " ") != "exec -i -w / eng --" || imp[len(imp)-1] != OpenCodeImportScript {
		t.Errorf("OpenCodeImportArgs = %q, want an exec from / in eng with stdin attached and no staged path", imp[:len(imp)-1])
	}
	if exp := OpenCodeExportArgs("eng"); strings.Join(exp[:5], " ") != "exec -w / eng --" {
		t.Errorf("OpenCodeExportArgs = %q", exp)
	}
}
