#!/usr/bin/env python3
# SPEC: _spec/_paradigms/credential-boundary.puml, _spec/internal/sbx/state-sync.puml

import argparse
import contextlib
import fcntl
import os
from pathlib import Path
import shutil
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
SIDECARS = ("-wal", "-shm", "-journal")


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
            return False
        raise SnapshotError("unsupported credential schema")
    info = connection.execute("PRAGMA table_xinfo(credential)").fetchall()
    if any(row[6] != 0 for row in info):
        raise SnapshotError("unsupported credential schema")
    columns = [
        (name, kind.upper(), required, primary)
        for _, name, kind, required, _, primary, _ in info
    ]
    if columns != CREDENTIAL_COLUMNS:
        raise SnapshotError("unsupported credential schema")
    if any(kind == "trigger" and table == "credential" for _, kind, table in objects):
        raise SnapshotError("unsupported credential trigger")
    for table in tables:
        quoted = table.replace('"', '""')
        if any(
            row[2].lower() == "credential"
            for row in connection.execute('PRAGMA foreign_key_list("' + quoted + '")')
        ):
            raise SnapshotError("unsupported credential reference")
    return True


def database_snapshot(source, target):
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
            if dst.execute("PRAGMA journal_mode = DELETE").fetchone() != ("delete",):
                raise SnapshotError("cannot create standalone SQLite snapshot")
            dst.execute("PRAGMA secure_delete = ON")
            if has_credentials:
                dst.execute("DELETE FROM credential")
                dst.commit()
            dst.execute("VACUUM")
            check_database(dst)
            if has_credentials and dst.execute(
                "SELECT count(*) FROM credential"
            ).fetchone() != (0,):
                raise SnapshotError("credential removal failed")
    os.chmod(target, 0o600)


def inventory(root):
    databases, files, directories, sidecars = [], [], [], []
    if not root.exists():
        return databases, files, directories
    for directory, dirnames, filenames in os.walk(root, followlinks=False):
        if Path(directory) == root and RECOVERY_NAME in dirnames:
            if (root / RECOVERY_NAME).is_symlink():
                raise SnapshotError("recovery directory contains a symlink")
            dirnames.remove(RECOVERY_NAME)
        for name in dirnames:
            path = Path(directory, name)
            if path.relative_to(root) == Path("auth.json"):
                raise SnapshotError("legacy auth path is not a file")
            if path.is_symlink():
                raise SnapshotError("data directory contains a symlink")
            directories.append(path.relative_to(root))
        for name in filenames:
            path = Path(directory, name)
            rel = path.relative_to(root)
            if rel in (Path("auth.json"), Path(LOCK_NAME), Path(LIFECYCLE_LOCK_NAME)):
                continue
            if not stat.S_ISREG(path.lstat().st_mode):
                raise SnapshotError("data directory contains a non-regular file")
            if name.endswith(SIDECARS):
                sidecars.append(rel)
                continue
            with path.open("rb") as file:
                header = file.read(16)
            if header == b"SQLite format 3\x00" or path.suffix in (
                ".db",
                ".sqlite",
                ".sqlite3",
            ):
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
        with os.fdopen(fd, "wb") as output, source.open("rb") as input_file:
            shutil.copyfileobj(input_file, output)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(tmp, stat.S_IMODE(source.stat().st_mode))
        os.replace(tmp, target)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def normalize_offline(root):
    root = Path(root).resolve()
    databases, _, _ = inventory(root)
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


def snapshot(source, destination):
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
        databases, files, directories = inventory(source)
        old_databases, _, _ = inventory(destination)
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
                database_snapshot(origin / rel, target)
            for rel in files:
                target = stage / rel
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                shutil.copy2(source / rel, target)
            for rel in directories:
                (destination / rel).mkdir(mode=0o700, parents=True, exist_ok=True)
            for rel in set(databases + old_databases + files):
                target = destination / rel
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                publish(stage / rel, target)
            auth = destination / "auth.json"
            if os.path.lexists(auth):
                auth.unlink()
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
    args = parser.parse_args()
    try:
        snapshot(args.source, args.destination)
    except (SnapshotError, sqlite3.Error, OSError):
        print(
            "proveo: OpenCode credential-free snapshot failed; do not launch or reuse unsanitized state",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
