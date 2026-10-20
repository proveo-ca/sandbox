// SPEC: _spec/defs/opencode/engine-local-history.puml
package sbx

import (
	"archive/tar"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
)

// OpenCodeDatabaseHeader opens every SQLite database file.
const OpenCodeDatabaseHeader = "SQLite format 3\x00"

// OpenCodeImportCommit is the last member of an import stream; the import installs nothing without it.
const OpenCodeImportCommit = "commit"

// Exit statuses of the engine-side history scripts.
const (
	OpenCodeImportExisting   = 3
	OpenCodeImportCorrupt    = 4
	OpenCodeImportIncomplete = 5
	OpenCodeExportNone       = 3
)

// ErrOpenCodeImportChanged reports a host database file that changed while it streamed.
var ErrOpenCodeImportChanged = errors.New("changed while it was copied")

// openCodeImportSides are the suffixes of the host database files an import carries.
var openCodeImportSides = []string{"", "-wal", "-journal"}

// openCodeDatabasePath is the python that resolves the database path OpenCode itself opens.
const openCodeDatabasePath = `def database():
    if os.environ.get("OPENCODE_DB"):
        return os.environ["OPENCODE_DB"]
    data = os.environ.get("XDG_DATA_HOME") or os.path.expanduser("~/.local/share")
    return os.path.join(data, "opencode", "opencode.db")
`

// OpenCodeImportScript reads a WriteOpenCodeImport tar on stdin into a private temp dir and installs it
// once as the engine's OpenCode database through SQLite's backup API: exit 0 installed,
// OpenCodeImportExisting, OpenCodeImportCorrupt, or OpenCodeImportIncomplete without the commit member.
const OpenCodeImportScript = `import os, shutil, sqlite3, sys, tarfile, tempfile
` + openCodeDatabasePath + `
dest = database()
if os.path.lexists(dest):
    sys.exit(3)
names = {"opencode.db", "opencode.db-wal", "opencode.db-journal"}
def receive(work):
    try:
        with tarfile.open(fileobj=sys.stdin.buffer.raw, mode="r|") as tar:
            for member in tar:
                if member.name == "` + OpenCodeImportCommit + `":
                    return True
                if member.name not in names or not member.isfile():
                    return False
                with tar.extractfile(member) as r, open(os.path.join(work, member.name), "xb") as w:
                    shutil.copyfileobj(r, w)
    except (tarfile.TarError, EOFError):
        pass
    return False
code = 0
work = tempfile.mkdtemp(prefix="proveo-import-")
try:
    copy = os.path.join(work, "opencode.db")
    if not receive(work) or not os.path.isfile(copy):
        code = 5
    else:
        try:
            src = sqlite3.connect(copy)
            ok = src.execute("PRAGMA quick_check").fetchone()[0] == "ok"
        except sqlite3.DatabaseError:
            src, ok = None, False
        if not ok:
            code = 4
        else:
            os.makedirs(os.path.dirname(dest), mode=0o700, exist_ok=True)
            fd, tmp = tempfile.mkstemp(prefix=".proveo-import-", dir=os.path.dirname(dest))
            os.close(fd)
            try:
                out = sqlite3.connect(tmp)
                src.backup(out)
                out.close()
                os.chmod(tmp, 0o600)
                try:
                    os.link(tmp, dest)
                except FileExistsError:
                    code = 3
            finally:
                os.unlink(tmp)
        if src is not None:
            src.close()
finally:
    shutil.rmtree(work, ignore_errors=True)
sys.exit(code)
`

// OpenCodeExportScript streams a consistent single-file copy of the engine's OpenCode database
// to stdout: exit 0 streamed, OpenCodeExportNone when the engine has none.
const OpenCodeExportScript = `import os, shutil, sqlite3, sys, tempfile
` + openCodeDatabasePath + `
source = database()
if not os.path.isfile(source):
    sys.exit(3)
work = tempfile.mkdtemp(prefix="proveo-export-")
try:
    copy = os.path.join(work, "opencode.db")
    src = sqlite3.connect(source, timeout=30)
    out = sqlite3.connect(copy)
    src.backup(out)
    out.execute("PRAGMA journal_mode=DELETE")
    out.close()
    src.close()
    with open(copy, "rb") as f:
        shutil.copyfileobj(f, sys.stdout.buffer)
finally:
    shutil.rmtree(work, ignore_errors=True)
`

// OpenCodeImportFiles stats src and its -wal/-journal, keyed by suffix; -shm never travels.
func OpenCodeImportFiles(src string) map[string]os.FileInfo {
	files := map[string]os.FileInfo{}
	for _, s := range openCodeImportSides {
		if info, err := os.Lstat(src + s); err == nil && info.Mode().IsRegular() {
			files[s] = info
		}
	}
	return files
}

// WriteOpenCodeImport streams files, OpenCodeImportFiles(src) taken before, to w as the tar
// OpenCodeImportScript reads; it appends OpenCodeImportCommit only when none changed while it streamed,
// else it returns ErrOpenCodeImportChanged.
func WriteOpenCodeImport(w io.Writer, src string, files map[string]os.FileInfo) error {
	tw := tar.NewWriter(w)
	for _, s := range openCodeImportSides {
		info, ok := files[s]
		if !ok {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "opencode.db" + s, Mode: 0o600, Size: info.Size()}); err != nil {
			return err
		}
		if err := copyOpenCodeFile(tw, src+s, info.Size()); err != nil {
			return err
		}
	}
	if !maps.EqualFunc(OpenCodeImportFiles(src), files, sameOpenCodeFile) {
		return ErrOpenCodeImportChanged
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: OpenCodeImportCommit, Mode: 0o600}); err != nil {
		return err
	}
	return tw.Close()
}

// copyOpenCodeFile copies the first size bytes of path to w.
func copyOpenCodeFile(w io.Writer, path string, size int64) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrOpenCodeImportChanged
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := io.CopyN(w, f, size); errors.Is(err, io.EOF) {
		return ErrOpenCodeImportChanged
	} else if err != nil {
		return err
	}
	return nil
}

// sameOpenCodeFile reports an unchanged size and mtime.
func sameOpenCodeFile(a, b os.FileInfo) bool {
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// OpenCodeImportArgs runs the import script in engine name with stdin attached.
func OpenCodeImportArgs(name string) []string {
	return []string{"exec", "-i", "-w", "/", name, "--", "python3", "-I", "-c", OpenCodeImportScript}
}

// OpenCodeExportArgs streams engine name's OpenCode database on stdout.
func OpenCodeExportArgs(name string) []string {
	return []string{"exec", "-w", "/", name, "--", "python3", "-I", "-c", OpenCodeExportScript}
}
