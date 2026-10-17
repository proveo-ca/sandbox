// SPEC: _spec/defs/opencode/engine-local-history.puml
package sbx

// OpenCodeImportStage is where a new engine receives the host database before the import installs it.
const OpenCodeImportStage = "/tmp/proveo-import-opencode.db"

// OpenCodeDatabaseHeader opens every SQLite database file.
const OpenCodeDatabaseHeader = "SQLite format 3\x00"

// Exit statuses of the engine-side history scripts.
const (
	OpenCodeImportExisting = 3
	OpenCodeImportCorrupt  = 4
	OpenCodeExportNone     = 3
)

// openCodeDatabasePath is the python that resolves the database path OpenCode itself opens.
const openCodeDatabasePath = `def database():
    if os.environ.get("OPENCODE_DB"):
        return os.environ["OPENCODE_DB"]
    data = os.environ.get("XDG_DATA_HOME") or os.path.expanduser("~/.local/share")
    return os.path.join(data, "opencode", "opencode.db")
`

// OpenCodeImportScript installs the staged database (argv[1]) as the engine's OpenCode database
// once, through SQLite's backup API, and removes the stage: exit 0 installed,
// OpenCodeImportExisting when the engine already holds one, OpenCodeImportCorrupt on a failed
// quick_check.
const OpenCodeImportScript = `import os, shutil, sqlite3, sys, tempfile
` + openCodeDatabasePath + `
stage = sys.argv[1]
dest = database()
code = 0
work = tempfile.mkdtemp(prefix="proveo-import-")
try:
    if os.path.lexists(dest):
        code = 3
    else:
        copy = os.path.join(work, "opencode.db")
        for suffix in ("", "-wal", "-journal"):
            if os.path.isfile(stage + suffix):
                shutil.copyfile(stage + suffix, copy + suffix)
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
    for suffix in ("", "-wal", "-shm", "-journal"):
        try:
            os.unlink(stage + suffix)
        except FileNotFoundError:
            pass
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

// OpenCodeImportCopyArgs copies one host database file (suffix "", "-wal" or "-journal") to the stage.
func OpenCodeImportCopyArgs(name, hostFile, suffix string) []string {
	return []string{"cp", hostFile, name + ":" + OpenCodeImportStage + suffix}
}

// OpenCodeImportArgs runs the import script in engine name.
func OpenCodeImportArgs(name string) []string {
	return []string{"exec", "-w", "/", name, "--", "python3", "-I", "-c", OpenCodeImportScript, OpenCodeImportStage}
}

// OpenCodeExportArgs streams engine name's OpenCode database on stdout.
func OpenCodeExportArgs(name string) []string {
	return []string{"exec", "-w", "/", name, "--", "python3", "-I", "-c", OpenCodeExportScript}
}
