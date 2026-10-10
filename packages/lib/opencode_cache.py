# SPEC: _spec/_plans/opencode-versioned-history-storage.puml

import array
import contextlib
import ctypes
import hashlib
import hmac
import json
import math
import os
from pathlib import Path
import re
import select
import secrets
import shutil
import signal
import socket
import socketserver
import sqlite3
import struct
import stat
import subprocess
import sys
import tempfile
import threading
import time


class CacheError(Exception):
    def __init__(self, message, code=78):
        super().__init__(message)
        self.code = code


def process_identity(pid):
    text = Path(f"/proc/{pid}/stat").read_text()
    return f"{pid}:{text[text.rfind(')') + 2 :].split()[19]}"


def spawn_keeper(runtime, env):
    child = subprocess.run(
        [sys.executable, "-B", "-I", "-S", runtime.__file__, "--cache-server"],
        env=env,
        stdin=subprocess.DEVNULL,
        capture_output=True,
        text=True,
        timeout=2,
    )
    if child.returncode:
        raise CacheError("OpenCode cache owner could not start")
    return json.loads(child.stdout)["cache_owner"]


def protect_process():
    if ctypes.CDLL(None, use_errno=True).prctl(4, 0, 0, 0, 0) != 0:
        raise CacheError("cannot protect the OpenCode prepared-state authority")


def producer_version(env):
    path = Path(
        env.get(
            "PROVEO_OPENCODE_VERSION_FILE", "/usr/local/lib/proveo/opencode.version"
        )
    )
    try:
        version = path.read_text().strip()
    except OSError:
        raise CacheError(
            "OpenCode version metadata is missing; rebuild the image"
        ) from None
    match = re.fullmatch(
        r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z.-]+)?",
        version,
    )
    if match is None or match[1] != "2":
        raise CacheError(
            "this OpenCode runtime requires a stable 2.x producer; do not reuse another major's state"
        )
    return version


def native_version(executable, expected, env, budget=2):
    try:
        result = subprocess.run(
            [executable, "--version"],
            env=env,
            capture_output=True,
            text=True,
            timeout=max(0.01, min(2, budget)),
        )
    except (OSError, subprocess.TimeoutExpired):
        raise CacheError(
            "cannot verify the native OpenCode version within the startup budget"
        ) from None
    if result.returncode or result.stdout.strip() != "opencode v" + expected:
        raise CacheError(
            "native OpenCode does not match the image's producer version; rebuild or prepare the matching image"
        )


def context(runtime, env):
    version = producer_version(env)
    durable = runtime.durable_directory(env)
    name = runtime.database_name(env)
    base = Path(env.get("PROVEO_OPENCODE_CACHE_HOME", "/tmp"))
    key = hashlib.sha256(
        f"{durable}\0{name}\0v2\0{base.resolve()}".encode()
    ).hexdigest()[:24]
    root = base / f"proveo-opencode-runtime-cache-{os.geteuid()}-{key}"
    sockets = Path("/tmp") / f"proveo-opencode-control-{os.geteuid()}"
    return dict(
        version=version,
        durable=durable,
        name=name,
        root=root,
        socket=sockets / (key + ".sock"),
        engine=runtime.engine_identity(),
    )


def safe_directory(path):
    if path.is_symlink():
        raise CacheError("OpenCode private state contains a directory symlink")
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    if path.stat().st_uid != os.geteuid() or path.stat().st_mode & 0o077:
        raise CacheError(
            "OpenCode private state must be owned by the runtime user with mode 0700"
        )


def signature(root, deadline):
    result = []
    if not root.exists():
        return result
    for directory, dirs, files in os.walk(root, followlinks=False):
        for name in sorted(dirs + files):
            if time.monotonic() >= deadline:
                raise CacheError(
                    "prepared-state metadata exceeded the startup budget; run explicit preparation"
                )
            path = Path(directory, name)
            info = path.lstat()
            result.append(
                (
                    str(path.relative_to(root)),
                    info.st_dev,
                    info.st_ino,
                    info.st_mode,
                    info.st_size,
                    info.st_mtime_ns,
                    info.st_ctime_ns,
                )
            )
    return sorted(result)


def database_schema(helper, data, name):
    file = data / name
    if not file.exists():
        return None
    if file.is_symlink():
        raise CacheError("OpenCode database must not be a symlink")
    with contextlib.closing(
        sqlite3.connect(file.as_uri() + "?mode=ro", uri=True, timeout=0)
    ) as db:
        db.execute("PRAGMA query_only=ON")
        db.execute("PRAGMA trusted_schema=OFF")
        tables = {
            row[0]
            for row in db.execute("SELECT name FROM sqlite_schema WHERE type='table'")
        }
        if "session_v2" not in tables:
            raise CacheError(
                "history is not an adopted V2 schema; V1/unknown stores require separate explicit migration"
            )
        helper.credential_schema(db)
        rows = db.execute(
            "SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name"
        ).fetchall()
    return hashlib.sha256(json.dumps(rows).encode()).hexdigest()


def read_record(fd):
    os.lseek(fd, 0, os.SEEK_SET)
    data = os.read(fd, 1024 * 1024)
    os.lseek(fd, 0, os.SEEK_SET)
    if data:
        return json.loads(data)
    journal = Path(os.readlink(f"/proc/self/fd/{fd}") + ".journal")
    return json.loads(journal.read_text()) if journal.exists() else None


def write_record(fd, value):
    journal = Path(os.readlink(f"/proc/self/fd/{fd}") + ".journal")
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w", dir=journal.parent, prefix=".cache-journal-", delete=False
        ) as file:
            temporary = Path(file.name)
            json.dump(value, file, separators=(",", ":"))
            file.flush()
            os.fsync(file.fileno())
        os.replace(temporary, journal)
        directory = os.open(journal.parent, os.O_DIRECTORY | os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def file_digest(path):
    if path.is_symlink():
        return "link:" + hashlib.sha256(os.readlink(path).encode()).hexdigest()
    digest = hashlib.sha256()
    with path.open("rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def atomic_json(path, value):
    with tempfile.NamedTemporaryFile(
        mode="w", dir=path.parent, prefix=".publication-manifest-", delete=False
    ) as file:
        temporary = Path(file.name)
        try:
            json.dump(value, file, separators=(",", ":"))
            file.flush()
            os.fsync(file.fileno())
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)
    directory = os.open(path.parent, os.O_DIRECTORY | os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def append_intent(path, value):
    descriptor = os.open(
        path, os.O_APPEND | os.O_CREAT | os.O_WRONLY | os.O_NOFOLLOW, 0o600
    )
    try:
        data = (json.dumps(value, separators=(",", ":")) + "\n").encode()
        while data:
            written = os.write(descriptor, data)
            if written <= 0:
                raise OSError("publication intent write made no progress")
            data = data[written:]
        os.fsync(descriptor)
        directory = os.open(path.parent, os.O_DIRECTORY | os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        os.close(descriptor)


def child_environment(env, cfg):
    data = cfg["root"] / "data/opencode"
    return dict(
        env,
        XDG_DATA_HOME=str(data.parent),
        XDG_STATE_HOME=str(cfg["root"] / "state"),
        OPENCODE_DB=str(data / cfg["name"]),
        PROVEO_OPENCODE_RUNTIME_DATA=str(data),
        PROVEO_OPENCODE_DURABLE_DATA=str(cfg["durable"]),
        TMPDIR=str(cfg["root"] / "tmp"),
    )


def receive(connection):
    data = bytearray()
    while not data.endswith(b"\n"):
        part = connection.recv(1)
        if not part or len(data) >= 16384:
            raise CacheError("invalid OpenCode cache control request")
        data.extend(part)
    return json.loads(data)


def reply(connection, value, fd=None):
    ancillary = (
        []
        if fd is None
        else [(socket.SOL_SOCKET, socket.SCM_RIGHTS, array.array("i", [fd]))]
    )
    connection.sendmsg([(json.dumps(value) + "\n").encode()], ancillary)


class Keeper:
    def __init__(self, runtime, env):
        self.runtime, self.env = runtime, env
        self.cfg = context(runtime, env)
        self.helper = runtime.load_credentials()
        self.lock = threading.Lock()
        self.state = "unprepared"
        self.token = None
        self.lease = None
        self.cache_signature = None
        self.durable_signature = None
        self.schema = None

    def lease_store(self):
        self.cfg["durable"].mkdir(mode=0o700, parents=True, exist_ok=True)
        fd = self.runtime.try_exclusive(
            self.cfg["durable"] / self.helper.LIFECYCLE_LOCK_NAME
        )
        if fd is None:
            raise CacheError("another OpenCode run owns this history store", 75)
        return fd

    def durable_stamp(self):
        relevant = self.durable_rows()
        return hashlib.sha256(
            json.dumps(relevant, separators=(",", ":")).encode()
        ).hexdigest()

    def durable_rows(self):
        rows = signature(self.cfg["durable"], time.monotonic() + 4)
        excluded = (
            self.helper.LIFECYCLE_LOCK_NAME,
            self.helper.LIFECYCLE_LOCK_NAME + ".journal",
            self.helper.LOCK_NAME,
            self.helper.RECOVERY_NAME,
        )
        return [
            list(row)
            for row in rows
            if row[0].split("/", 1)[0] not in excluded
            and not Path(row[0]).name.startswith((".proveo-clean-", ".cache-journal-"))
        ]

    def record(self, expected, phase="active", transaction=None):
        value = dict(
            format=1,
            root=str(self.cfg["root"]),
            database=self.cfg["name"],
            engine=self.cfg["engine"],
            producer=self.cfg["version"],
            expected_durable=expected,
            phase=phase,
            transaction=transaction,
        )
        write_record(self.lease, value)
        return value

    def validate_record(self, value):
        if not isinstance(value, dict) or any(
            value.get(key) != expected
            for key, expected in (
                ("format", 1),
                ("root", str(self.cfg["root"])),
                ("database", self.cfg["name"]),
                ("engine", self.cfg["engine"]),
                ("producer", self.cfg["version"]),
            )
        ):
            raise CacheError(
                "pending history belongs to another engine, database or producer; preserve its recovery record",
                75,
            )
        if (
            not isinstance(value.get("expected_durable"), str)
            or re.fullmatch(r"[0-9a-f]{64}", value["expected_durable"]) is None
        ):
            raise CacheError(
                "pending history lacks a verifiable published baseline", 75
            )
        if (
            value["expected_durable"] != self.durable_stamp()
            and value.get("phase") != "publishing"
        ):
            raise CacheError(
                "published history changed; do not overwrite it during cache recovery"
            )
        transaction = value.get("transaction")
        if (
            transaction is not None
            and re.fullmatch(r"pub-[0-9a-f]{32}", transaction) is None
        ):
            raise CacheError("invalid private publication recovery record", 75)
        if value.get("phase") == "publishing":
            self.validate_publication(value)

    def validate_publication(self, record):
        if record.get("transaction") is None:
            raise CacheError("interrupted publication has no transaction manifest", 75)
        path = (
            self.cfg["root"] / "publications" / record["transaction"] / "manifest.json"
        )
        manifest = json.loads(path.read_text())
        if manifest["baseline"] != record["expected_durable"]:
            raise CacheError("publication baseline does not match its journal", 75)
        original = {row[0]: row for row in manifest["rows"]}
        current = {row[0]: row for row in self.durable_rows()}
        updates, created, deleted = {}, set(), set()
        intents = path.parent / "intents.jsonl"
        if intents.exists():
            for line in intents.read_bytes().splitlines(keepends=True):
                if not line.endswith(b"\n"):
                    continue
                intent = json.loads(line)
                if intent["op"] == "replace":
                    updates[intent["path"]] = intent["digest"]
                elif intent["op"] == "mkdir":
                    created.add(intent["path"])
                elif intent["op"] == "delete":
                    deleted.add(intent["path"])
        for name in original.keys() | current.keys():
            before, after = original.get(name), current.get(name)
            if before == after:
                continue
            if after is None and any(
                name == removed or name.startswith(removed + "/") for removed in deleted
            ):
                continue
            if after is not None and stat.S_ISDIR(after[3]):
                if before is not None and before[1:4] == after[1:4]:
                    continue
                if name in created or any(
                    candidate.startswith(name + "/") for candidate in updates
                ):
                    continue
            if (
                after is None
                or name not in updates
                or file_digest(self.cfg["durable"] / name) != updates[name]
            ):
                raise CacheError(
                    "published history has changes outside the interrupted transaction; preserve both histories"
                )

    def mark_prepared(self):
        self.schema = database_schema(
            self.helper, self.cfg["root"] / "data/opencode", self.cfg["name"]
        )
        self.cache_signature = signature(self.cfg["root"], time.monotonic() + 5)
        self.durable_signature = self.durable_stamp()
        self.state = "prepared"

    def prepare(self, source, native, empty=False):
        if self.state == "active":
            raise CacheError("another OpenCode run owns this cache", 75)
        source = Path(source).resolve()
        if not empty:
            if database_schema(self.helper, source, self.cfg["name"]) is None:
                raise CacheError(
                    "preparation source has no selected database; use --empty only for a genuinely empty store"
                )
        elif (
            any(self.cfg["durable"].iterdir())
            if self.cfg["durable"].exists()
            else False
        ):
            if any(
                p.name != self.helper.LIFECYCLE_LOCK_NAME
                for p in self.cfg["durable"].iterdir()
            ):
                raise CacheError("empty preparation cannot discard existing history")
        target = self.cfg["root"] / "data/opencode"
        replace = (
            target.exists()
            and self.cache_signature is not None
            and self.cache_signature
            == signature(self.cfg["root"], time.monotonic() + 5)
        )
        if target.exists() and not replace:
            raise CacheError(
                "unattested retained state requires explicit recovery, not replacement"
            )
        lease = self.lease_store()
        source_lease = None
        try:
            existing = read_record(lease)
            if existing is not None and existing.get("phase") != "prepared":
                raise CacheError(
                    "history has pending private state; recover it explicitly", 75
                )
            safe_directory(self.cfg["root"])
            if not empty and source != self.cfg["durable"]:
                source_lock = source / self.helper.LIFECYCLE_LOCK_NAME
                if source_lock.exists():
                    source_lease = os.open(source_lock, os.O_RDONLY | os.O_NOFOLLOW)
                    try:
                        self.helper.fcntl.flock(
                            source_lease,
                            self.helper.fcntl.LOCK_SH | self.helper.fcntl.LOCK_NB,
                        )
                    except BlockingIOError:
                        raise CacheError(
                            "preparation source has an active lifecycle writer", 75
                        ) from None
                source_stamp = signature(source, time.monotonic() + 5)
            with tempfile.TemporaryDirectory(
                prefix="proveo-opencode-prepare-", dir=self.cfg["root"].parent
            ) as temporary:
                data = Path(temporary) / "data"
                if empty:
                    data.mkdir(mode=0o700)
                else:
                    prefix = source / self.helper.PREFIX_NAME
                    origin = (
                        json.loads(prefix.read_text())["data"]
                        if prefix.exists()
                        else source
                    )
                    self.helper.snapshot(
                        source,
                        data,
                        self.cfg["name"],
                        rebase=(origin, self.cfg["durable"]),
                        prefix=self.cfg["durable"],
                    )
                    if source != self.cfg["durable"] and source_stamp != signature(
                        source, time.monotonic() + 5
                    ):
                        raise CacheError(
                            "preparation source changed during import; original history was not replaced",
                            75,
                        )
                    databases, _, _ = self.helper.inventory(data, self.cfg["name"])
                    for database in databases:
                        if str(database) != self.cfg["name"]:
                            (data / database).unlink()
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                if target.exists():
                    shutil.rmtree(target)
                os.rename(data, target)
            (self.cfg["root"] / "tmp").mkdir(mode=0o700, exist_ok=True)
            self.helper.copy_state(
                target / self.helper.STATE_NAME, self.cfg["root"] / "state/opencode"
            )
            binary = self.env.get("PROVEO_OPENCODE_NATIVE_BINARY")
            if binary is None:
                binary = (
                    Path("/usr/local/lib/proveo/opencode.native").read_text().strip()
                )
            native_version(binary, self.cfg["version"], self.env)
            schema_home = self.cfg["root"] / "schema-home"
            schema_home.mkdir(mode=0o700, exist_ok=True)
            child = child_environment(self.env, self.cfg)
            child.update(
                HOME=str(schema_home),
                OPENCODE_CONFIG_DIR=str(schema_home),
                OPENCODE_CONFIG_CONTENT='{"plugins":[],"mcp":{"servers":{}}}',
                PROVEO_OPENCODE_SCHEMA_PREP="1",
            )
            self.lease = lease
            self.record(self.durable_stamp(), "preparing")
            child["PROVEO_OPENCODE_SCHEMA_LEASE_FD"] = str(lease)
            worker = subprocess.Popen(
                [
                    sys.executable,
                    "-B",
                    "-I",
                    "-S",
                    self.runtime.__file__,
                    "--schema-command",
                    binary,
                    "session",
                    "list",
                    "--format",
                    "json",
                    "--standalone",
                ],
                env=child,
                cwd=schema_home,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                pass_fds=(lease,),
                start_new_session=True,
            )
            try:
                worker.wait(
                    timeout=float(self.env.get("PROVEO_OPENCODE_SCHEMA_TIMEOUT", "300"))
                )
            except subprocess.TimeoutExpired:
                worker.terminate()
                try:
                    worker.wait(timeout=6)
                except subprocess.TimeoutExpired:
                    worker.kill()
                    worker.wait()
                self.state = "pending-recovery"
                lease = None
                raise CacheError(
                    "native schema preparation timed out; descendants retain lifecycle ownership until shutdown",
                    74,
                ) from None
            result = worker
            if result.returncode:
                raise CacheError(
                    "native V2 schema preparation failed; private state remains available for recovery"
                )
            self.helper.normalize_offline(self.cfg["durable"], self.cfg["name"])
            self.record(self.durable_stamp(), "active")
            self.lease = lease
            lease = None
            self.publish()
        finally:
            if source_lease is not None:
                os.close(source_lease)
            if lease is not None:
                if self.lease == lease:
                    self.lease = None
                os.close(lease)

    def check(self, budget=4):
        if self.state == "active":
            raise CacheError("another OpenCode run owns this prepared cache", 75)
        if self.state != "prepared":
            raise CacheError(
                "OpenCode history is not prepared; run --proveo-prepare or --proveo-recover",
                75 if self.state in ("active", "pending-recovery") else 78,
            )
        if self.cache_signature != signature(
            self.cfg["root"], time.monotonic() + min(4, budget)
        ):
            self.state = "unprepared"
            raise CacheError(
                "prepared cache changed outside its owner; run explicit recovery"
            )
        if self.durable_signature != self.durable_stamp():
            self.state = "unprepared"
            raise CacheError(
                "published history changed; run explicit preparation in a fresh engine"
            )

    def checkout(self, budget=4):
        self.lease = self.lease_store()
        try:
            self.check(budget)
        except Exception:
            os.close(self.lease)
            self.lease = None
            raise
        existing = read_record(self.lease)
        if existing is not None:
            try:
                self.validate_record(existing)
                if existing.get("phase") != "prepared":
                    raise CacheError(
                        "history has pending private state; run explicit recovery", 75
                    )
            except Exception:
                os.close(self.lease)
                self.lease = None
                raise
        self.state = "active"
        self.token = secrets.token_hex(32)
        self.record(self.durable_signature)
        return self.token

    def publish(self):
        data = self.cfg["root"] / "data/opencode"
        transaction = "pub-" + secrets.token_hex(16)
        work = self.cfg["root"] / "publications" / transaction
        clean = work / "clean"
        recovery_root = self.cfg["durable"] / self.helper.RECOVERY_NAME
        recovery = recovery_root / transaction
        record = read_record(self.lease)
        baseline = record["expected_durable"] if record else self.durable_signature
        if baseline is None:
            raise CacheError("publication has no verified durable baseline", 75)
        attempted_publication = False
        try:
            self.helper.normalize_offline(data, self.cfg["name"])
            work.mkdir(mode=0o700, parents=True)
            recovery.mkdir(mode=0o700, parents=True)
            self.record(baseline, "building", transaction)
            self.runtime.checkpoint(
                self.helper,
                data,
                self.cfg["root"],
                clean,
                self.cfg["durable"],
                self.cfg["name"],
            )
            self.helper.snapshot(clean, recovery, self.cfg["name"])
            if baseline != self.durable_stamp():
                raise CacheError(
                    "published history changed; preserve both histories instead of overwriting"
                )
            manifest = dict(baseline=baseline, rows=self.durable_rows())
            manifest_path = work / "manifest.json"
            atomic_json(manifest_path, manifest)
            self.record(baseline, "publishing", transaction)

            def observe(prepared, target):
                relative = str(target.relative_to(self.cfg["durable"]))
                append_intent(
                    work / "intents.jsonl",
                    dict(path=relative, op="replace", digest=file_digest(prepared)),
                )

            def mutation(target, operation):
                append_intent(
                    work / "intents.jsonl",
                    dict(
                        path=str(target.relative_to(self.cfg["durable"])), op=operation
                    ),
                )

            attempted_publication = True
            self.helper.snapshot(
                clean,
                self.cfg["durable"],
                self.cfg["name"],
                publication_observer=observe,
                mutation_observer=mutation,
            )
            baseline = self.durable_stamp()
            self.durable_signature = baseline
            self.record(baseline, "published", transaction)
            old = self.cfg["root"] / "previous-data"
            if old.exists():
                raise CacheError(
                    "previous cache generation still requires reconciliation"
                )
            os.rename(data, old)
            os.rename(clean, data)
            state = self.cfg["root"] / "state/opencode"
            if state.exists():
                shutil.rmtree(state)
            self.helper.copy_state(data / self.helper.STATE_NAME, state)
            if (self.cfg["root"] / "tmp").exists():
                shutil.rmtree(self.cfg["root"] / "tmp")
            (self.cfg["root"] / "tmp").mkdir(mode=0o700)
            shutil.rmtree(old)
            for parent in (self.cfg["root"] / "publications", recovery_root):
                for directory in parent.iterdir():
                    if (
                        re.fullmatch(r"pub-[0-9a-f]{32}", directory.name)
                        and not directory.is_symlink()
                    ):
                        shutil.rmtree(directory)
            self.mark_prepared()
            self.record(self.durable_signature, "prepared")
            self.runtime.record_runtime(self.lease, None)
            os.close(self.lease)
            self.lease = None
        except Exception as error:
            self.state = "pending-recovery"
            if attempted_publication:
                self.validate_publication(
                    dict(transaction=transaction, expected_durable=manifest["baseline"])
                )
                baseline = self.durable_stamp()
                self.durable_signature = baseline
                self.record(baseline, "published", transaction)
            cause = (
                str(error) if isinstance(error, CacheError) else type(error).__name__
            )
            raise CacheError(
                "OpenCode publication failed; private state retained (not credential-free) for --proveo-recover; previous credential-free checkpoint remains published: "
                + cause,
                74,
            ) from None

    def recover(self):
        if self.state == "active":
            raise CacheError("another OpenCode run still owns the cache", 75)
        if self.lease is not None:
            os.close(self.lease)
            self.lease = None
        self.lease = self.lease_store()
        try:
            record = read_record(self.lease)
            if record is not None:
                self.validate_record(record)
                self.durable_signature = record["expected_durable"]
            elif (
                self.durable_signature is None
                or self.durable_signature != self.durable_stamp()
            ):
                raise CacheError(
                    "unattested recovery has no matching durable baseline; preserve private state",
                    75,
                )
            data = self.cfg["root"] / "data/opencode"
            previous = self.cfg["root"] / "previous-data"
            if previous.exists():
                if previous.is_symlink():
                    raise CacheError("recovery generation must not be a symlink")
                if not data.exists():
                    os.rename(previous, data)
                else:
                    if record is None or record.get("phase") != "published":
                        raise CacheError(
                            "cache generations require explicit reconciliation"
                        )
                    database_schema(self.helper, data, self.cfg["name"])
                    shutil.rmtree(previous)
            if database_schema(self.helper, data, self.cfg["name"]) is None:
                raise CacheError(
                    "private recovery database is unavailable; preserve the record", 75
                )
            if record is not None and record.get("phase") == "published":
                saved = (
                    self.cfg["root"]
                    / "publications"
                    / record["transaction"]
                    / "clean"
                    / self.helper.STATE_NAME
                )
                if not saved.exists():
                    saved = data / self.helper.STATE_NAME
                replacement = self.cfg["root"] / "restored-native-state"
                if replacement.exists():
                    shutil.rmtree(replacement)
                self.helper.copy_state(saved, replacement)
                state = self.cfg["root"] / "state/opencode"
                if state.exists():
                    shutil.rmtree(state)
                os.rename(replacement, state)
        except Exception:
            os.close(self.lease)
            self.lease = None
            raise
        self.durable_signature = self.durable_stamp()
        self.record(self.durable_signature)
        self.publish()


def serve(runtime, env, ready_fd=None):
    protect_process()
    if Path(runtime.__file__).resolve().parent == Path("/usr/local/bin"):
        for key in (
            "PROVEO_OPENCODE_CREDENTIAL_HELPER",
            "PROVEO_OPENCODE_VERSION_FILE",
            "PROVEO_OPENCODE_CACHE_HOME",
            "PROVEO_OPENCODE_NATIVE_BINARY",
        ):
            env.pop(key, None)
        os.environ.pop("PROVEO_OPENCODE_CREDENTIAL_HELPER", None)
    keeper = Keeper(runtime, env)
    safe_directory(keeper.cfg["socket"].parent)
    if keeper.cfg["socket"].exists():
        raise CacheError(
            "an OpenCode cache controller already exists; do not overwrite its socket"
        )

    class Handler(socketserver.BaseRequestHandler):
        def handle(self):
            connection = self.request
            connection.settimeout(5)
            peer = struct.unpack(
                "3i", connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12)
            )
            if peer[1] != os.geteuid():
                return
            held = False
            checked_out = False
            try:
                request = receive(connection)
                if (
                    request.get("version") != keeper.cfg["version"]
                    or request.get("engine") != keeper.cfg["engine"]
                ):
                    raise CacheError(
                        "cache producer or engine does not match; prepare the matching runtime",
                        75,
                    )
                held = keeper.lock.acquire(blocking=False)
                if not held:
                    raise CacheError("OpenCode cache maintenance is still running", 75)
                operation = request.get("op")
                if operation == "prepare":
                    keeper.prepare(
                        request["source"],
                        request.get("native"),
                        request.get("empty", False),
                    )
                elif operation == "recover":
                    keeper.recover()
                elif operation == "check":
                    keeper.check(float(request.get("budget", 4)))
                elif operation == "retire-safe":
                    if keeper.state == "prepared":
                        keeper.check()
                    elif (
                        keeper.state != "unprepared"
                        or (keeper.cfg["root"] / "data/opencode").exists()
                    ):
                        raise CacheError(
                            "engine contains active or unpublished state; do not retire it",
                            75,
                        )
                elif operation == "checkout":
                    token = keeper.checkout(float(request.get("budget", 4)))
                    checked_out = True
                    reply(
                        connection,
                        dict(ok=True, root=str(keeper.cfg["root"]), token=token),
                        keeper.lease,
                    )
                    keeper.lock.release()
                    held = False
                    connection.settimeout(None)
                    finish = receive(connection)
                    held = keeper.lock.acquire(blocking=False)
                    if (
                        not held
                        or finish.get("op") != "commit"
                        or not hmac.compare_digest(str(finish.get("token", "")), token)
                    ):
                        raise CacheError("invalid OpenCode cache completion")
                    keeper.publish()
                elif operation == "shutdown":
                    if keeper.state == "active":
                        raise CacheError("cache still contains private state", 75)
                    if keeper.lease is not None:
                        os.close(keeper.lease)
                        keeper.lease = None
                    threading.Thread(target=self.server.shutdown, daemon=True).start()
                else:
                    raise CacheError("unknown OpenCode cache operation")
                reply(
                    connection, dict(ok=True, state=keeper.state, schema=keeper.schema)
                )
            except Exception as error:
                if checked_out and keeper.state == "active":
                    keeper.state = "pending-recovery"
                with contextlib.suppress(OSError):
                    reply(
                        connection,
                        dict(
                            ok=False,
                            code=getattr(error, "code", 78),
                            message=str(error)
                            if isinstance(error, CacheError)
                            else "OpenCode cache operation failed; explicit recovery required",
                        ),
                    )
            finally:
                if held:
                    keeper.lock.release()

    class Server(socketserver.ThreadingUnixStreamServer):
        daemon_threads = True

    try:
        with Server(str(keeper.cfg["socket"]), Handler) as server:
            os.chmod(keeper.cfg["socket"], 0o600)
            if ready_fd is not None:
                os.write(ready_fd, b"1")
                os.close(ready_fd)
            server.serve_forever(poll_interval=0.1)
    finally:
        if keeper.lease is not None:
            os.close(keeper.lease)
        keeper.cfg["socket"].unlink(missing_ok=True)


def connect(
    runtime,
    env,
    operation,
    source=None,
    native=None,
    empty=False,
    start=False,
    owner=None,
    budget=4,
):
    cfg = context(runtime, env)
    safe_directory(cfg["socket"].parent)
    if start and cfg["socket"].exists():
        probe = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            probe.connect(str(cfg["socket"]))
        except ConnectionRefusedError:
            info = cfg["socket"].lstat()
            if info.st_uid != os.geteuid():
                raise CacheError("cache control socket has another owner")
            cfg["socket"].unlink()
        finally:
            probe.close()
    if start and not cfg["socket"].exists():
        owner = spawn_keeper(runtime, env)
    connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    connection.settimeout(max(0.01, min(4, budget)))
    deadline = time.monotonic() + (2 if start else 0)
    while True:
        try:
            connection.connect(str(cfg["socket"]))
            break
        except (FileNotFoundError, ConnectionRefusedError):
            if time.monotonic() >= deadline:
                connection.close()
                raise CacheError(
                    "OpenCode history is unprepared in this engine; run --proveo-prepare explicitly"
                ) from None
            time.sleep(0.025)
    peer = struct.unpack(
        "3i", connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12)
    )
    actual_owner = process_identity(peer[0])
    if peer[1] != os.geteuid() or owner is not None and owner != actual_owner:
        connection.close()
        raise CacheError(
            "OpenCode cache owner identity changed; do not trust replacement readiness records"
        )
    if operation == "checkout" and owner is None:
        connection.close()
        raise CacheError(
            "launch requires the cache owner identity issued by the host runtime"
        )
    connection.settimeout(
        None if operation in ("prepare", "recover") else max(0.01, min(4, budget))
    )
    connection.sendall(
        (
            json.dumps(
                dict(
                    op=operation,
                    version=cfg["version"],
                    engine=cfg["engine"],
                    source=str(source or cfg["durable"]),
                    native=native,
                    empty=empty,
                    budget=budget,
                )
            )
            + "\n"
        ).encode()
    )
    message, ancillary, _, _ = connection.recvmsg(
        16384, socket.CMSG_SPACE(array.array("i").itemsize)
    )
    fds = array.array("i")
    for level, kind, value in ancillary:
        if level == socket.SOL_SOCKET and kind == socket.SCM_RIGHTS:
            fds.frombytes(value[: len(value) - len(value) % fds.itemsize])
    while not message.endswith(b"\n"):
        part = connection.recv(16384)
        if not part or len(message) + len(part) > 16384:
            connection.close()
            raise CacheError("cache controller closed an incomplete response")
        message += part
    result = json.loads(message)
    if not result.get("ok"):
        connection.close()
        for fd in fds:
            os.close(fd)
        raise CacheError(
            result.get("message", "cache refused release"), result.get("code", 78)
        )
    result["cache_owner"] = actual_owner
    return connection, result, list(fds)


def run(runtime):
    env = dict(os.environ)
    begun = time.monotonic()
    started = float(env.get("PROVEO_OPENCODE_STARTUP_STARTED", begun))
    if not math.isfinite(started):
        raise CacheError("invalid OpenCode startup clock")
    deadline = min(started, begun) + 5
    protect_process()
    if sys.argv[1:] == ["--cache-server"]:
        ready_read, ready_write = os.pipe()
        pid = os.fork()
        if pid:
            os.close(ready_write)
            ready = select.select([ready_read], [], [], 1.8)[0]
            value = os.read(ready_read, 1) if ready else b""
            os.close(ready_read)
            if value != b"1":
                with contextlib.suppress(ProcessLookupError):
                    os.kill(pid, signal.SIGTERM)
                os.waitpid(pid, 0)
                raise CacheError(
                    "OpenCode keeper failed before its control socket was ready"
                )
            print(json.dumps(dict(cache_owner=process_identity(pid))), flush=True)
            return 0
        os.close(ready_read)
        os.setsid()
        with open(os.devnull, "w") as null:
            os.dup2(null.fileno(), 1)
            os.dup2(null.fileno(), 2)
        serve(runtime, env, ready_write)
        return 0
    if sys.argv[1:2] == ["--schema-command"]:
        lease = int(env["PROVEO_OPENCODE_SCHEMA_LEASE_FD"])
        return runtime.supervise(sys.argv[2:], env, lease)

    def stop(sig, _):
        raise SystemExit(128 + sig)

    for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(sig, stop)
    runtime.clear_stale_preflight(env)
    executable = sys.argv[1] if len(sys.argv) > 1 else ""
    args = sys.argv[2:]
    split = args.index("--") if "--" in args else len(args)
    deadline_flags = [
        arg for arg in args[:split] if arg.startswith("--proveo-startup-deadline=")
    ]
    if len(deadline_flags) > 1:
        raise CacheError("duplicate startup deadlines")
    if deadline_flags:
        absolute = float(deadline_flags[0].split("=", 1)[1])
        remaining = absolute - time.time()
        if not math.isfinite(absolute) or remaining <= 0:
            raise CacheError("shared startup deadline expired")
        deadline = min(deadline, begun + min(5, remaining))
        args = [arg for arg in args[:split] if arg not in deadline_flags] + args[split:]
        split = args.index("--") if "--" in args else len(args)
    budget_flags = [
        arg for arg in args[:split] if arg.startswith("--proveo-startup-budget=")
    ]
    if len(budget_flags) > 1:
        raise CacheError("duplicate startup budgets")
    if budget_flags:
        remaining = float(budget_flags[0].split("=", 1)[1])
        if not math.isfinite(remaining) or remaining <= 0 or remaining > 5:
            raise CacheError("invalid shared startup budget")
        deadline = min(deadline, begun + remaining)
        args = [arg for arg in args[:split] if arg not in budget_flags] + args[split:]
        split = args.index("--") if "--" in args else len(args)
    owner_flags = [
        arg for arg in args[:split] if arg.startswith("--proveo-cache-owner=")
    ]
    if len(owner_flags) > 1:
        raise CacheError("duplicate cache owner identities")
    owner = owner_flags[0].split("=", 1)[1] if owner_flags else None
    args = [arg for arg in args[:split] if arg not in owner_flags] + args[split:]
    control = executable in (
        "--prepare",
        "--check-prepared",
        "--sanitize",
        "--recover",
        "--shutdown-cache",
        "--bootstrap-cache",
        "--retire-safe",
    )
    prepared_command = executable == "--preflight-command"
    if prepared_command:
        executable, args = args[0], args[1:]
    cfg = context(runtime, env)
    if executable == "--bootstrap-cache":
        if cfg["socket"].exists():
            if owner is None:
                raise CacheError(
                    "a cache controller already exists without a trusted host owner record"
                )
            probe = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            try:
                probe.connect(str(cfg["socket"]))
                pid = struct.unpack(
                    "3i", probe.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12)
                )[0]
                if process_identity(pid) != owner:
                    raise CacheError(
                        "stored cache owner identity does not match this engine"
                    )
            except ConnectionRefusedError:
                old_pid = int(owner.split(":", 1)[0])
                try:
                    text = Path(f"/proc/{old_pid}/stat").read_text()
                    still_alive = process_identity(old_pid) == owner and text[
                        text.rfind(")") + 2 :
                    ].split()[0] not in ("Z", "X")
                except FileNotFoundError:
                    still_alive = False
                info = cfg["socket"].lstat()
                if (
                    still_alive
                    or info.st_uid != os.geteuid()
                    or not stat.S_ISSOCK(info.st_mode)
                ):
                    raise CacheError(
                        "cache socket is not safely attributable to the stopped keeper"
                    ) from None
                cfg["socket"].unlink()
                owner = spawn_keeper(runtime, env)
            finally:
                probe.close()
        else:
            owner = spawn_keeper(runtime, env)
        print(json.dumps(dict(cache_owner=owner)), flush=True)
        return 0
    helper = runtime.load_credentials()
    if not control and not prepared_command:
        if deadline <= time.monotonic():
            raise CacheError(
                "shared startup deadline expired before native version verification"
            )
        native_version(executable, cfg["version"], env, deadline - time.monotonic())
    if (
        not control
        and not prepared_command
        and env.get("PROVEO_OPENCODE_PREFLIGHT_READY") == "1"
    ):
        result = runtime.prepared_execution(
            executable, args, env, helper, cfg["durable"]
        )
        if result is not None:
            return result
    if not prepared_command and args[:1] in (
        ["--proveo-prepare"],
        ["--proveo-recover"],
    ):
        executable = "--prepare" if "--proveo-prepare" in args else "--recover"
        control = True
    if control:
        operation = {
            "--prepare": "prepare",
            "--recover": "recover",
            "--check-prepared": "check",
            "--sanitize": "check",
            "--shutdown-cache": "shutdown",
            "--retire-safe": "retire-safe",
        }[executable]
        source = (
            args[args.index("--source") + 1]
            if "--source" in args
            else str(cfg["durable"])
        )
        empty = "--empty" in args
        connection, result, fds = connect(
            runtime,
            env,
            operation,
            source=source,
            empty=empty,
            start=operation in ("prepare", "recover"),
            owner=owner,
        )
        connection.close()
        for fd in fds:
            os.close(fd)
        if operation in ("check", "prepare", "recover"):
            refused = Path(
                env.get("PROVEO_SEED_REFUSED", "/dev/shm/proveo-seed-refused")
            )
            with contextlib.suppress(OSError):
                if (
                    refused.read_text().strip()
                    == "OpenCode history requires explicit preparation"
                ):
                    refused.unlink()
        if operation in ("prepare", "recover"):
            print(json.dumps(dict(cache_owner=result["cache_owner"])), flush=True)
        return 0
    if (
        runtime.diagnostic(args) is not None
        or env.get("PROVEO_GIT_SYNC_MSG_INFLIGHT") == "1"
    ):
        with runtime.RuntimeDirectory() as temporary:
            child, _ = runtime.private_environment(env, Path(temporary), cfg["name"])
            runtime.diagnostic_config(child, Path(temporary))
            return runtime.supervise([executable, *args], child, None)
    runtime.check_service_environment(env)
    wait = min(runtime.lease_wait_seconds(env), 4.8)
    seed_deadline = min(time.monotonic() + wait, deadline - 0.05)
    announced = False
    from opencode_startup import SeedWait

    with SeedWait(env) as progress:
        while runtime.same_boot_seed_pending(env) and time.monotonic() < seed_deadline:
            if env.get("PROVEO_OPENCODE_WAITED_FOR_SEED") == "1":
                break
            if not announced and env.get("PROVEO_SEED_PROGRESS") != "1":
                print(
                    "proveo: waiting for this boot's OpenCode seed before taking the durable database",
                    file=sys.stderr,
                    flush=True,
                )
                announced = True
            progress.update()
            time.sleep(0.1)
    if runtime.launch_refused(env):
        raise CacheError(
            "the seed did not release the agent — " + runtime.refusal_text(env), 1
        )
    if runtime.same_boot_seed_pending(env):
        raise CacheError(
            "this boot's OpenCode seed still holds the durable database; startup budget exhausted",
            75,
        )
    connection = None
    lease = None
    try:
        budget = deadline - time.monotonic()
        if budget <= 0:
            raise CacheError(
                "OpenCode startup exceeded its five-second preparation budget"
            )
        connection, result, fds = connect(
            runtime, env, "checkout", owner=owner, budget=budget
        )
        if len(fds) != 1:
            raise CacheError("cache controller did not provide lifecycle ownership")
        lease = fds[0]
        child = child_environment(env, cfg)
        if prepared_command:
            child.update(
                PROVEO_OPENCODE_PREFLIGHT_READY="1", PROVEO_OPENCODE_LEASE_FD=str(lease)
            )
            command = [executable, *args]
        else:
            command = runtime.native_command(executable, args)
        if prepared_command:
            code = runtime.supervise(command, child, lease)
        else:
            native_lease = runtime.try_exclusive(
                cfg["root"] / "data/.native-client.lock"
            )
            if native_lease is None:
                raise CacheError("another native OpenCode client owns this cache", 75)
            try:
                code = runtime.supervise(command, child, (lease, native_lease))
            finally:
                os.close(native_lease)
        connection.settimeout(None)
        connection.sendall(
            (json.dumps(dict(op="commit", token=result["token"])) + "\n").encode()
        )
        completion = receive(connection)
        if not completion.get("ok"):
            raise CacheError(completion["message"], completion["code"])
        return code
    finally:
        if connection is not None:
            connection.close()
        if lease is not None:
            os.close(lease)
