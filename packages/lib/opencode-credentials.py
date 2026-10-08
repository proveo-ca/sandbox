#!/usr/bin/env python3
# SPEC: _spec/_paradigms/credential-boundary.puml, _spec/internal/sbx/state-sync.puml

import argparse
import contextlib
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import re
import sqlite3
import stat
import sys
import tempfile
import time


CREDENTIAL_COLUMNS = [
    ("id", "TEXT", 0, 1),
    ("integration_id", "TEXT", 0, 0),
    ("label", "TEXT", 1, 0),
    ("value", "TEXT", 1, 0),
    ("connector_id", "TEXT", 0, 0),
    ("method_id", "TEXT", 0, 0),
    ("active", "INTEGER", 0, 0),
    ("time_created", "INTEGER", 1, 0),
    ("time_updated", "INTEGER", 1, 0),
]
LOCK_NAME = ".proveo-opencode-snapshot.lock"
LIFECYCLE_LOCK_NAME = ".proveo-opencode-runtime.lock"
RECOVERY_NAME = ".proveo-opencode-recovery"
STATE_NAME = ".proveo-opencode-state"
MIGRATION_NAME = ".proveo-opencode-migrated"
PREFIX_NAME = ".proveo-opencode-paths.json"
SIDECARS = ("-wal", "-shm", "-journal")
AUTH_FILES = ("auth.json", "mcp-auth.json")
TOKEN_SCHEMAS = {
    "credential": CREDENTIAL_COLUMNS,
    "account": [
        ("id", "TEXT", 0, 1),
        ("email", "TEXT", 1, 0),
        ("url", "TEXT", 1, 0),
        ("access_token", "TEXT", 1, 0),
        ("refresh_token", "TEXT", 1, 0),
        ("token_expiry", "INTEGER", 0, 0),
        ("time_created", "INTEGER", 1, 0),
        ("time_updated", "INTEGER", 1, 0),
    ],
    "control_account": [
        ("email", "TEXT", 1, 1),
        ("url", "TEXT", 1, 2),
        ("access_token", "TEXT", 1, 0),
        ("refresh_token", "TEXT", 1, 0),
        ("token_expiry", "INTEGER", 0, 0),
        ("active", "INTEGER", 1, 0),
        ("time_created", "INTEGER", 1, 0),
        ("time_updated", "INTEGER", 1, 0),
    ],
    "account_state": [
        ("id", "INTEGER", 0, 1),
        ("active_account_id", "TEXT", 0, 0),
        ("active_org_id", "TEXT", 0, 0),
    ],
}
JSON_COLUMNS = {
    "session_message": ("data",),
    "event": ("data",),
    "session_pending": ("data",),
    "session_inbox": ("payload",),
    "instruction_blob": ("value",),
    "instruction_entry": ("value",),
    "instruction_state": ("initial_values", "current_values"),
    "session_v2": ("metadata", "summary_diffs", "revert"),
}


class SnapshotError(Exception):
    pass


def check_database(connection):
    if connection.execute("PRAGMA quick_check").fetchall() != [("ok",)]:
        raise SnapshotError("SQLite integrity check failed")
    if connection.execute("PRAGMA foreign_key_check").fetchone() is not None:
        raise SnapshotError("SQLite foreign key check failed")


def credential_schema(connection):
    objects = connection.execute(
        "SELECT name, type, tbl_name FROM sqlite_schema"
    ).fetchall()
    tables = {name for name, kind, _ in objects if kind == "table"}
    if "credential" not in tables:
        if (
            "migration" not in tables
            and {"project", "session", "message", "part"} <= tables
            and not any("credential" in name.lower() for name, _, _ in objects)
        ):
            pass
        else:
            raise SnapshotError("unsupported credential schema")
    for table, expected in TOKEN_SCHEMAS.items():
        if table not in tables:
            continue
        info = connection.execute('PRAGMA table_xinfo("' + table + '")').fetchall()
        columns = [
            (name, kind.upper(), required, primary)
            for _, name, kind, required, _, primary, _ in info
        ]
        if any(row[6] != 0 for row in info) or columns != expected:
            raise SnapshotError("unsupported authentication schema")
    if any(kind == "trigger" and table in TOKEN_SCHEMAS for _, kind, table in objects):
        raise SnapshotError("unsupported authentication trigger")
    for table in tables:
        quoted = table.replace('"', '""')
        for row in connection.execute('PRAGMA foreign_key_list("' + quoted + '")'):
            if row[2].lower() not in TOKEN_SCHEMAS:
                continue
            if (
                table == "account_state"
                and row[2] == "account"
                and row[3:5] == ("active_account_id", "id")
                and row[6] == "SET NULL"
            ):
                continue
            raise SnapshotError("unsupported authentication reference")
    return bool(tables.intersection(TOKEN_SCHEMAS))


def rebased(value, old, new):
    if isinstance(value, str):
        return value.replace(str(old).rstrip("/") + "/", str(new).rstrip("/") + "/")
    if isinstance(value, list):
        return [rebased(item, old, new) for item in value]
    if isinstance(value, dict):
        return {key: rebased(item, old, new) for key, item in value.items()}
    return value


def rebase_database(db, old, new):
    tables = {
        row[0]
        for row in db.execute("SELECT name FROM sqlite_schema WHERE type = 'table'")
    }
    for table, columns in JSON_COLUMNS.items():
        if table not in tables:
            continue
        present = {row[1] for row in db.execute('PRAGMA table_info("' + table + '")')}
        for column in columns:
            if column not in present:
                continue
            for rowid, text in db.execute(
                f'SELECT rowid, "{column}" FROM "{table}" WHERE "{column}" IS NOT NULL'
            ).fetchall():
                if str(old).rstrip("/") + "/" not in text:
                    continue
                value = json.loads(text)
                changed = rebased(value, old, new)
                if changed != value:
                    db.execute(
                        f'UPDATE "{table}" SET "{column}" = ? WHERE rowid = ?',
                        (json.dumps(changed, separators=(",", ":")), rowid),
                    )
    for table, column in (
        ("session_v2", "directory"),
        ("project", "worktree"),
        ("project_directory", "directory"),
        ("worktree", "directory"),
    ):
        if table in tables and column in {
            row[1] for row in db.execute(f'PRAGMA table_info("{table}")')
        }:
            for rowid, value in db.execute(
                f'SELECT rowid, "{column}" FROM "{table}"'
            ).fetchall():
                changed = rebased(value, old, new)
                if changed != value:
                    db.execute(
                        f'UPDATE "{table}" SET "{column}" = ? WHERE rowid = ?',
                        (changed, rowid),
                    )


def database_snapshot(source, target, rebase=None):
    deadline = time.monotonic() + 30

    def progress(*_):
        if time.monotonic() > deadline:
            raise SnapshotError("SQLite backup timed out")

    with contextlib.closing(
        sqlite3.connect(source.as_uri() + "?mode=ro", uri=True, timeout=1)
    ) as src:
        src.execute("PRAGMA query_only = ON")
        src.execute("PRAGMA trusted_schema = OFF")
        with contextlib.closing(sqlite3.connect(target)) as dst:
            src.backup(dst, pages=256, progress=progress, sleep=0.05)
            dst.execute("PRAGMA trusted_schema = OFF")
            check_database(dst)
            has_credentials = credential_schema(dst)
            tables = set()
            if dst.execute("PRAGMA journal_mode = DELETE").fetchone() != ("delete",):
                raise SnapshotError("cannot create standalone SQLite snapshot")
            dst.execute("PRAGMA secure_delete = ON")
            if has_credentials:
                tables = {
                    row[0]
                    for row in dst.execute(
                        "SELECT name FROM sqlite_schema WHERE type = 'table'"
                    )
                }
                if "account_state" in tables:
                    dst.execute(
                        "UPDATE account_state SET active_account_id = NULL, active_org_id = NULL"
                    )
                for table in ("credential", "account", "control_account"):
                    if table in tables:
                        dst.execute('DELETE FROM "' + table + '"')
            if rebase is not None:
                rebase_database(dst, *rebase)
            dst.commit()
            dst.execute("VACUUM")
            check_database(dst)
            if has_credentials:
                for table in ("credential", "account", "control_account"):
                    if table in tables and dst.execute(
                        'SELECT count(*) FROM "' + table + '"'
                    ).fetchone() != (0,):
                        raise SnapshotError("credential removal failed")
    os.chmod(target, 0o600)


def native_database(path, selected):
    return len(path.parts) == 1 and (
        path.name == selected
        or path.name == "opencode.db"
        or re.fullmatch(r"opencode-[A-Za-z0-9._-]+\.db", path.name) is not None
    )


def root_databases(root, selected):
    databases = set()
    for path in root.iterdir():
        if path.name in (
            *AUTH_FILES,
            LOCK_NAME,
            LIFECYCLE_LOCK_NAME,
            MIGRATION_NAME,
            PREFIX_NAME,
        ) or path.name.endswith(SIDECARS):
            continue
        if not stat.S_ISREG(path.lstat().st_mode):
            continue
        if native_database(Path(path.name), selected):
            databases.add(path.name)
            continue
        with path.open("rb") as file:
            header = file.read(16)
        if header != b"SQLite format 3\x00":
            continue
        with contextlib.closing(
            sqlite3.connect(path.as_uri() + "?mode=ro", uri=True, timeout=1)
        ) as db:
            db.execute("PRAGMA query_only = ON")
            names = {
                row[0]
                for row in db.execute(
                    "SELECT name FROM sqlite_schema WHERE type = 'table'"
                )
            }
            sensitive = False
            for table in names.intersection(TOKEN_SCHEMAS):
                columns = {
                    row[1] for row in db.execute(f'PRAGMA table_info("{table}")')
                }
                sensitive |= (
                    {"access_token", "refresh_token"} <= columns
                    or table == "credential"
                    and {"id", "value"} <= columns
                )
            if sensitive or {"project", "session", "message", "part"} <= names:
                databases.add(path.name)
    return databases


def artifact_link(root, path, databases):
    text = os.readlink(path)
    if Path(text).is_absolute():
        raise SnapshotError("artifact symlink is absolute")
    try:
        target = (path.parent / text).resolve(strict=True)
        rel = target.relative_to(root)
    except (ValueError, OSError, RuntimeError):
        raise SnapshotError("artifact symlink escapes or has no target") from None
    if (
        not rel.parts
        or rel.parts[0]
        in (
            *AUTH_FILES,
            LOCK_NAME,
            LIFECYCLE_LOCK_NAME,
            MIGRATION_NAME,
            PREFIX_NAME,
            RECOVERY_NAME,
            STATE_NAME,
        )
        or len(rel.parts) == 1
        and rel.name in databases
    ):
        raise SnapshotError("artifact symlink targets managed authentication state")
    return text


def inventory(root, selected="opencode.db"):
    databases, files, directories, sidecars = [], [], [], []
    if not root.exists():
        return databases, files, directories
    known = root_databases(root, selected)
    for directory, dirnames, filenames in os.walk(root, followlinks=False):
        if Path(directory) == root and RECOVERY_NAME in dirnames:
            if (root / RECOVERY_NAME).is_symlink():
                raise SnapshotError("recovery directory contains a symlink")
            dirnames.remove(RECOVERY_NAME)
        for name in list(dirnames):
            path = Path(directory, name)
            if str(path.relative_to(root)) in AUTH_FILES:
                raise SnapshotError("legacy auth path is not a file")
            if path.is_symlink():
                artifact_link(root, path, known)
                files.append(path.relative_to(root))
                dirnames.remove(name)
                continue
            directories.append(path.relative_to(root))
        for name in filenames:
            path = Path(directory, name)
            rel = path.relative_to(root)
            if str(rel) in (
                *AUTH_FILES,
                LOCK_NAME,
                LIFECYCLE_LOCK_NAME,
                MIGRATION_NAME,
            ):
                continue
            if path.is_symlink():
                artifact_link(root, path, known)
                if len(rel.parts) == 1 and native_database(rel, selected):
                    raise SnapshotError("native database is a symlink")
                files.append(rel)
                continue
            if not stat.S_ISREG(path.lstat().st_mode):
                raise SnapshotError("data directory contains a non-regular file")
            suffix = next(
                (suffix for suffix in SIDECARS if name.endswith(suffix)), None
            )
            base = Path(str(rel)[: -len(suffix)]) if suffix else None
            if (
                base is not None
                and len(base.parts) == 1
                and (base.name in known or native_database(base, selected))
            ):
                sidecars.append(rel)
                continue
            if len(rel.parts) == 1 and rel.name in known:
                databases.append(rel)
            else:
                files.append(rel)
    for rel in sidecars:
        if not any(
            str(rel) == str(db) + suffix for db in databases for suffix in SIDECARS
        ):
            raise SnapshotError("orphan SQLite sidecar")
    return databases, files, directories


def publish(source, target):
    fd, tmp = tempfile.mkstemp(prefix=".proveo-clean-", dir=target.parent)
    try:
        if source.is_symlink():
            os.close(fd)
            os.unlink(tmp)
            os.symlink(os.readlink(source), tmp)
            os.replace(tmp, target)
            return
        with os.fdopen(fd, "wb") as output, source.open("rb") as input_file:
            shutil.copyfileobj(input_file, output)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(tmp, stat.S_IMODE(source.stat().st_mode))
        os.replace(tmp, target)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def normalize_offline(root, selected="opencode.db"):
    root = Path(root).resolve()
    databases, _, _ = inventory(root, selected)
    with contextlib.ExitStack() as stack:
        connections = []
        for rel in databases:
            db = stack.enter_context(
                contextlib.closing(
                    sqlite3.connect(
                        (root / rel).as_uri() + "?mode=rw", uri=True, timeout=0
                    )
                )
            )
            db.execute("PRAGMA trusted_schema = OFF")
            db.execute("PRAGMA locking_mode = EXCLUSIVE")
            db.execute("BEGIN EXCLUSIVE")
            db.execute("ROLLBACK")
            connections.append(db)
        for db in connections:
            checkpoint = db.execute("PRAGMA wal_checkpoint(TRUNCATE)").fetchone()
            if checkpoint[0] != 0:
                raise SnapshotError("offline checkpoint is busy")
            if db.execute("PRAGMA journal_mode = DELETE").fetchone() != ("delete",):
                raise SnapshotError("cannot checkpoint offline destination")
            check_database(db)
    for rel in databases:
        for suffix in ("-wal", "-shm"):
            path = Path(str(root / rel) + suffix)
            if path.exists():
                if suffix == "-wal" and path.stat().st_size:
                    raise SnapshotError("offline checkpoint left a non-empty WAL")
                path.unlink()


def copy_state(source, destination, rebase=None):
    source, destination = Path(source), Path(destination)
    destination.mkdir(mode=0o700, parents=True, exist_ok=True)
    if not source.exists():
        return
    for directory, dirs, files in os.walk(source, followlinks=False):
        if Path(directory) == source:
            dirs[:] = [name for name in dirs if name != "locks"]
        for name in dirs:
            if Path(directory, name).is_symlink():
                raise SnapshotError("native state contains a symlink")
        for name in files:
            rel = Path(directory, name).relative_to(source)
            if len(rel.parts) == 1 and (
                name in AUTH_FILES
                or re.fullmatch(r"service(?:-[A-Za-z0-9._-]+)?\.json", name)
            ):
                continue
            origin = source / rel
            if not stat.S_ISREG(origin.lstat().st_mode):
                raise SnapshotError("native state contains a non-regular file")
            target = destination / rel
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            shutil.copy2(origin, target)
            if rebase and target.suffix in (".json", ".jsonl"):
                text = target.read_text()
                updated = text.replace(
                    str(rebase[0]).rstrip("/") + "/", str(rebase[1]).rstrip("/") + "/"
                )
                if text != updated:
                    target.write_text(updated)


def database_digest(path):
    digest = hashlib.sha256()
    with contextlib.closing(
        sqlite3.connect(Path(path).as_uri() + "?mode=ro", uri=True)
    ) as db:
        for statement in db.iterdump():
            digest.update(statement.encode())
    return digest.digest()


def snapshot(
    source,
    destination,
    selected="opencode.db",
    rebase=None,
    state_source=None,
    prefix=None,
):
    source = Path(source).resolve()
    destination = Path(destination).resolve()
    if (
        source == destination
        or source in destination.parents
        or destination in source.parents
    ):
        raise SnapshotError(
            "source and offline destination must be separate directories"
        )
    if source.exists() and not source.is_dir():
        raise SnapshotError("source is not a data directory")
    destination.mkdir(mode=0o700, parents=True, exist_ok=True)
    lock = os.open(
        destination / LOCK_NAME, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600
    )
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        databases, files, directories = inventory(source, selected)
        old_databases, _, _ = inventory(destination, selected)
        for rel in set(databases + old_databases):
            for suffix in SIDECARS:
                if os.path.lexists(str(destination / rel) + suffix):
                    raise SnapshotError(
                        "offline destination has SQLite sidecars; stage its backup first"
                    )
        with tempfile.TemporaryDirectory(
            prefix="proveo-opencode-private-", dir="/tmp"
        ) as temporary:
            stage = Path(temporary)
            for rel in set(databases + old_databases):
                target = stage / rel
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                origin = source if rel in databases else destination
                database_snapshot(origin / rel, target, rebase)
            for rel in files:
                target = stage / rel
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                if (source / rel).is_symlink():
                    os.symlink(os.readlink(source / rel), target)
                else:
                    shutil.copy2(source / rel, target)
            if state_source is not None:
                shutil.rmtree(stage / STATE_NAME, ignore_errors=True)
                copy_state(state_source, stage / STATE_NAME, rebase)
                for directory, _, names in os.walk(stage / STATE_NAME):
                    directories.append(Path(directory).relative_to(stage))
                    files.extend(
                        Path(directory, name).relative_to(stage) for name in names
                    )
                files = [
                    rel
                    for rel in files
                    if (stage / rel).is_file() or (stage / rel).is_symlink()
                ]
            if prefix is not None:
                (stage / PREFIX_NAME).write_text(json.dumps({"data": str(prefix)}))
                files.append(Path(PREFIX_NAME))
            for rel in directories:
                (destination / rel).mkdir(mode=0o700, parents=True, exist_ok=True)
            for name in AUTH_FILES:
                auth = destination / name
                if os.path.lexists(auth):
                    auth.unlink()
            state = destination / STATE_NAME
            if state.exists():
                for file in state.iterdir():
                    if file.name in AUTH_FILES or re.fullmatch(
                        r"service(?:-[A-Za-z0-9._-]+)?\.json", file.name
                    ):
                        file.unlink()
                locks = state / "locks"
                if locks.is_symlink():
                    locks.unlink()
                elif locks.exists():
                    shutil.rmtree(locks)
            ordered = sorted(set(files) - {Path(PREFIX_NAME)}) + sorted(
                set(databases + old_databases)
            )
            if (stage / PREFIX_NAME).exists():
                ordered.append(Path(PREFIX_NAME))
            for rel in ordered:
                target = destination / rel
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                publish(stage / rel, target)
            directory_fd = os.open(destination, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory_fd)
            finally:
                os.close(directory_fd)
    finally:
        os.close(lock)


def main():
    parser = argparse.ArgumentParser(
        description="Snapshot OpenCode data without saved credentials"
    )
    parser.add_argument("source")
    parser.add_argument("destination")
    parser.add_argument("--offline-destination", required=True, action="store_true")
    parser.add_argument("--database", default="opencode.db")
    args = parser.parse_args()
    try:
        snapshot(args.source, args.destination, args.database)
    except (SnapshotError, sqlite3.Error, OSError):
        print(
            "proveo: OpenCode credential-free snapshot failed; do not launch or reuse unsanitized state",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
