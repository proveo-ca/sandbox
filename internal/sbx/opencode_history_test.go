// SPEC: _spec/defs/opencode/engine-local-history.puml
package sbx

import (
	"bytes"
	"errors"
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

// runScript runs an engine script with args and returns its stdout, stderr and exit status.
func runScript(t *testing.T, home, script string, args ...string) ([]byte, string, int) {
	t.Helper()
	cmd := exec.Command(python(t), append([]string{"-I", "-c", script}, args...)...)
	cmd.Env = engineEnv(home)
	cmd.Dir = t.TempDir()
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

// stage copies a host database and its -wal to dir the way OpenCodeImportCopyArgs places them.
func stage(t *testing.T, src, dir string) string {
	t.Helper()
	staged := filepath.Join(dir, "proveo-import-opencode.db")
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(src + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(staged+suffix, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return staged
}

func TestTheOpenCodeImportInstallsTheHostDatabaseOnce(t *testing.T) {
	t.Parallel()
	host := filepath.Join(t.TempDir(), "opencode.db")
	walDatabase(t, host, "ses_host")
	home := t.TempDir()
	target := filepath.Join(home, ".local", "share", "opencode", "opencode.db")

	staged := stage(t, host, t.TempDir())
	if _, errOut, code := runScript(t, home, OpenCodeImportScript, staged); code != 0 {
		t.Fatalf("import = exit %d, want 0\n%s", code, errOut)
	}
	if got := sessions(t, target); got != "ses_host" {
		t.Errorf("the engine database holds %q, want the session that lived only in the host -wal", got)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("engine database mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Stat(staged + suffix); err == nil {
			t.Errorf("the staged %s was left behind in the engine", filepath.Base(staged+suffix))
		}
	}

	other := filepath.Join(t.TempDir(), "opencode.db")
	walDatabase(t, other, "ses_other")
	if _, _, code := runScript(t, home, OpenCodeImportScript, stage(t, other, t.TempDir())); code != OpenCodeImportExisting {
		t.Errorf("a second import = exit %d, want %d: the engine's own history must never be overwritten", code, OpenCodeImportExisting)
	}
	if got := sessions(t, target); got != "ses_host" {
		t.Errorf("after a refused second import the engine holds %q, want ses_host", got)
	}
}

func TestTheOpenCodeImportRefusesACorruptCopy(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	staged := filepath.Join(t.TempDir(), "proveo-import-opencode.db")
	if err := os.WriteFile(staged, []byte("not a database, just bytes long enough to look like one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runScript(t, home, OpenCodeImportScript, staged); code != OpenCodeImportCorrupt {
		t.Errorf("import of a corrupt copy = exit %d, want %d", code, OpenCodeImportCorrupt)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "opencode", "opencode.db")); err == nil {
		t.Error("a corrupt copy was installed as the engine database")
	}
}

func TestTheOpenCodeExportStreamsAConsistentCopy(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if _, _, code := runScript(t, home, OpenCodeExportScript); code != OpenCodeExportNone {
		t.Errorf("export from an engine with no database = exit %d, want %d", code, OpenCodeExportNone)
	}
	db := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	walDatabase(t, db, "ses_engine")
	out, errOut, code := runScript(t, home, OpenCodeExportScript)
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
	if got := strings.Join(OpenCodeImportCopyArgs("eng", "/h/opencode.db", "-wal"), " "); got != "cp /h/opencode.db eng:"+OpenCodeImportStage+"-wal" {
		t.Errorf("OpenCodeImportCopyArgs = %q", got)
	}
	imp := OpenCodeImportArgs("eng")
	if strings.Join(imp[:5], " ") != "exec -w / eng --" || imp[len(imp)-1] != OpenCodeImportStage {
		t.Errorf("OpenCodeImportArgs = %q, want an exec from / in eng that names the stage", imp)
	}
	if exp := OpenCodeExportArgs("eng"); strings.Join(exp[:5], " ") != "exec -w / eng --" {
		t.Errorf("OpenCodeExportArgs = %q", exp)
	}
}
