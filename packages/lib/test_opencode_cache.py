# SPEC: _spec/_plans/opencode-versioned-history-storage.puml

import ast
import contextlib
import inspect
import json
import os
import signal
from pathlib import Path
import sqlite3
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

import test_opencode_runtime as support
from test_opencode_runtime import RUNTIME
import opencode_cache


class PreparedCacheTests(unittest.TestCase):
    def setUp(self):
        self.fixture = support.RuntimeTests("runTest")
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)

    def control(self, flag, *args, env=None):
        f = self.fixture
        return subprocess.run(
            [
                sys.executable,
                "-B",
                str(RUNTIME),
                flag,
                *args,
                "--proveo-cache-owner=" + f.owner,
            ],
            env=env or f.env,
            capture_output=True,
            text=True,
            timeout=10,
        )

    def test_unprepared_history_fails_without_scanning_or_mutating_it(self):
        f = self.fixture
        env = dict(f.env, PROVEO_OPENCODE_CACHE_HOME=str(f.root / "other-engine"))
        before = (f.durable / "opencode.db").read_bytes()
        begun = time.monotonic()
        result = f.run_cli("append", env=env)
        self.assertEqual(result.returncode, 78, result.stderr)
        self.assertLess(time.monotonic() - begun, 5)
        self.assertEqual((f.durable / "opencode.db").read_bytes(), before)
        self.assertEqual(f.events(), [])

    def test_wrong_major_is_rejected_before_creating_or_opening_history(self):
        f = self.fixture
        version = f.root / "v1-version"
        version.write_text("1.18.35\n")
        target = f.root / "must-not-create"
        env = dict(
            f.env,
            PROVEO_OPENCODE_VERSION_FILE=str(version),
            PROVEO_OPENCODE_DURABLE_DATA=str(target),
        )
        result = f.run_cli("append", env=env)
        self.assertEqual(result.returncode, 78, result.stderr)
        self.assertFalse(target.exists())
        self.assertIn("stable 2.x", result.stderr)

    def test_v1_source_is_preserved_and_not_imported(self):
        f = self.fixture
        source = f.root / "v1"
        source.mkdir()
        with contextlib.closing(sqlite3.connect(source / "opencode.db")) as db:
            db.executescript(
                "CREATE TABLE session(id TEXT); CREATE TABLE message(id TEXT); CREATE TABLE part(id TEXT);"
            )
            db.execute("INSERT INTO session VALUES ('keep')")
            db.commit()
        before = (source / "opencode.db").read_bytes()
        result = self.control("--prepare", "--source", str(source))
        self.assertEqual(result.returncode, 78, result.stderr)
        self.assertIn("V1/unknown", result.stderr)
        self.assertEqual((source / "opencode.db").read_bytes(), before)
        self.assertEqual(f.run_cli("append").returncode, 0)

    def test_missing_source_database_cannot_replace_existing_history(self):
        f = self.fixture
        source = f.root / "missing-database"
        source.mkdir()
        before = (f.durable / "opencode.db").read_bytes()
        result = self.control("--prepare", "--source", str(source))
        self.assertEqual(result.returncode, 78, result.stderr)
        self.assertEqual((f.durable / "opencode.db").read_bytes(), before)
        self.assertEqual(f.run_cli("append").returncode, 0)

    def test_source_lifecycle_writer_blocks_import(self):
        f = self.fixture
        source = f.root / "locked-source"
        f.fixture(source)
        descriptor = support.credentials.os.open(
            source / support.credentials.LIFECYCLE_LOCK_NAME,
            support.credentials.os.O_CREAT | support.credentials.os.O_RDWR,
            0o600,
        )
        try:
            support.credentials.fcntl.flock(
                descriptor, support.credentials.fcntl.LOCK_EX
            )
            before = (f.durable / "opencode.db").read_bytes()
            result = self.control("--prepare", "--source", str(source))
            self.assertEqual(result.returncode, 75, result.stderr)
            self.assertEqual((f.durable / "opencode.db").read_bytes(), before)
        finally:
            support.credentials.os.close(descriptor)

    def test_all_durable_artifact_changes_invalidate_prepared_state(self):
        f = self.fixture
        (f.durable / "artifact.txt").write_text("outside edit")
        result = f.run_cli("append")
        self.assertEqual(result.returncode, 78, result.stderr)
        self.assertEqual(f.events(), [])

    def direct_keeper(self):
        f = self.fixture
        result = self.control("--shutdown-cache")
        self.assertEqual(result.returncode, 0, result.stderr)
        import importlib.machinery, importlib.util

        loader = importlib.machinery.SourceFileLoader(
            "repair_cache_runtime", str(RUNTIME)
        )
        spec = importlib.util.spec_from_loader(loader.name, loader)
        runtime = importlib.util.module_from_spec(spec)
        loader.exec_module(runtime)
        runtime.engine_identity = lambda: "fixture-engine"
        keeper = opencode_cache.Keeper(runtime, f.env)
        keeper.helper = support.credentials
        keeper.mark_prepared()
        self.addCleanup(
            lambda: support.credentials.os.close(keeper.lease)
            if keeper.lease is not None
            else None
        )
        return keeper

    def test_checkout_checks_generation_after_acquiring_lease(self):
        keeper = self.direct_keeper()
        acquire = keeper.lease_store

        def changed():
            descriptor = acquire()
            (keeper.cfg["durable"] / "artifact.txt").write_text(
                "intervening publication"
            )
            return descriptor

        with mock.patch.object(keeper, "lease_store", side_effect=changed):
            with self.assertRaises(opencode_cache.CacheError):
                keeper.checkout()
        self.assertIsNone(keeper.lease)

    def test_recovery_rejects_foreign_engine_record(self):
        keeper = self.direct_keeper()
        keeper.checkout()
        record = opencode_cache.read_record(keeper.lease)
        record["engine"] = "another-engine"
        opencode_cache.write_record(keeper.lease, record)
        before = (keeper.cfg["durable"] / keeper.cfg["name"]).read_bytes()
        keeper.state = "pending-recovery"
        with self.assertRaises(opencode_cache.CacheError):
            keeper.recover()
        self.assertEqual(
            (keeper.cfg["durable"] / keeper.cfg["name"]).read_bytes(), before
        )
        self.assertEqual(
            record,
            opencode_cache.read_record(
                support.credentials.os.open(
                    keeper.cfg["durable"] / keeper.helper.LIFECYCLE_LOCK_NAME,
                    support.credentials.os.O_RDONLY,
                )
            ),
        )

    def test_recovery_survives_failure_between_cache_renames(self):
        keeper = self.direct_keeper()
        keeper.checkout()
        data = keeper.cfg["root"] / "data/opencode"
        rename = opencode_cache.os.rename

        def fail_clean(source, destination):
            if Path(source).name == "clean" and Path(destination) == data:
                raise OSError("injected cache rotation failure")
            return rename(source, destination)

        with mock.patch.object(opencode_cache.os, "rename", side_effect=fail_clean):
            with self.assertRaises(opencode_cache.CacheError):
                keeper.publish()
        self.assertFalse(data.exists())
        self.assertTrue((keeper.cfg["root"] / "previous-data").exists())
        keeper.recover()
        self.assertTrue(data.exists())
        self.assertEqual(keeper.state, "prepared")
        self.assertEqual(list((keeper.cfg["root"] / "publications").iterdir()), [])

    def test_restart_recovery_checks_persisted_durable_baseline(self):
        keeper = self.direct_keeper()
        keeper.checkout()
        support.credentials.os.close(keeper.lease)
        keeper.lease = None
        (keeper.cfg["durable"] / "artifact.txt").write_text("new published work")
        restarted = opencode_cache.Keeper(keeper.runtime, keeper.env)
        restarted.helper = keeper.helper
        with self.assertRaises(opencode_cache.CacheError):
            restarted.recover()
        self.assertEqual(
            (keeper.cfg["durable"] / "artifact.txt").read_text(), "new published work"
        )

    def test_stale_keeper_socket_can_be_bootstrapped_for_explicit_recovery(self):
        f = self.fixture
        process = f.start("hold")
        ready = f.wait_event("ready")
        process.kill()
        os.kill(ready["pid"], signal.SIGTERM)
        process.communicate(timeout=5)
        os.kill(int(f.owner.split(":", 1)[0]), signal.SIGKILL)
        time.sleep(0.1)
        result = self.control("--bootstrap-cache")
        self.assertEqual(result.returncode, 0, result.stderr)
        import json

        f.owner = json.loads(result.stdout)["cache_owner"]
        recovered = self.control("--recover")
        self.assertEqual(recovered.returncode, 0, recovered.stderr)
        f.assert_clean(messages=2)

    def test_idle_keeper_restart_recovers_published_prepared_cache(self):
        f = self.fixture
        os.kill(int(f.owner.split(":", 1)[0]), signal.SIGKILL)
        time.sleep(0.1)
        result = self.control("--bootstrap-cache")
        self.assertEqual(result.returncode, 0, result.stderr)
        import json

        f.owner = json.loads(result.stdout)["cache_owner"]
        recovered = self.control("--recover")
        self.assertEqual(recovered.returncode, 0, recovered.stderr)
        self.assertEqual(f.run_cli("append").returncode, 0)
        f.assert_clean(messages=2)

    def test_atomic_journal_failure_preserves_complete_old_record(self):
        keeper = self.direct_keeper()
        keeper.checkout()
        old = opencode_cache.read_record(keeper.lease)
        with mock.patch.object(
            opencode_cache.os, "replace", side_effect=OSError("atomic journal failure")
        ):
            with self.assertRaises(OSError):
                opencode_cache.write_record(keeper.lease, dict(old, phase="broken"))
        self.assertEqual(opencode_cache.read_record(keeper.lease), old)

    def test_snapshot_never_copies_or_overwrites_runtime_journal(self):
        f = self.fixture
        target = f.root / "journal-snapshot"
        journal = f.durable / (support.credentials.LIFECYCLE_LOCK_NAME + ".journal")
        before = journal.read_bytes()
        support.credentials.snapshot(f.durable, target)
        self.assertFalse((target / journal.name).exists())
        support.credentials.snapshot(target, f.durable)
        self.assertEqual(journal.read_bytes(), before)

    def test_disposable_runtime_refuses_pending_cache_journal(self):
        f = self.fixture
        pending = f.start("hold")
        f.wait_event("ready")
        pending.kill()
        os.kill(f.wait_event("ready")["pid"], signal.SIGTERM)
        pending.communicate(timeout=5)
        env = dict(f.env, PROVEO_OPENCODE_HISTORY_MODE="disposable")
        before = (f.durable / "opencode.db").read_bytes()
        result = subprocess.run(
            [sys.executable, "-B", str(RUNTIME), str(f.native), "append"],
            env=env,
            capture_output=True,
            text=True,
            timeout=10,
        )
        self.assertEqual(result.returncode, 75, result.stderr)
        self.assertEqual((f.durable / "opencode.db").read_bytes(), before)

    def test_interrupted_durable_publication_can_recover_its_own_files(self):
        keeper = self.direct_keeper()
        keeper.checkout()
        cached = keeper.cfg["root"] / "data/opencode"
        (cached / "new-artifact.txt").write_text("checkpoint artifact")
        publish = keeper.helper.publish

        def interrupted(source, destination, observer=None):
            publish(source, destination, observer)
            if (
                destination.parent == keeper.cfg["durable"]
                and destination.name == "new-artifact.txt"
            ):
                raise KeyboardInterrupt()

        with mock.patch.object(keeper.helper, "publish", side_effect=interrupted):
            with self.assertRaises(KeyboardInterrupt):
                keeper.publish()
        keeper.state = "pending-recovery"
        keeper.recover()
        self.assertEqual(
            (keeper.cfg["durable"] / "new-artifact.txt").read_text(),
            "checkpoint artifact",
        )
        self.assertEqual(keeper.state, "prepared")

    def test_rotation_failure_restores_complete_saved_native_preferences(self):
        keeper = self.direct_keeper()
        keeper.checkout()
        state = keeper.cfg["root"] / "state/opencode"
        (state / "prompt-history.jsonl").write_text("FULL_HISTORY\n")
        copy = keeper.helper.copy_state

        def partial(source, destination, *args, **kwargs):
            if destination == state:
                destination.mkdir(parents=True, exist_ok=True)
                (destination / "prompt-history.jsonl").write_text("PARTIAL")
                raise OSError("partial preference restore")
            return copy(source, destination, *args, **kwargs)

        with mock.patch.object(keeper.helper, "copy_state", side_effect=partial):
            with self.assertRaises(opencode_cache.CacheError):
                keeper.publish()
        keeper.recover()
        self.assertEqual((state / "prompt-history.jsonl").read_text(), "FULL_HISTORY\n")

    def test_literal_owner_option_after_separator_is_not_stripped(self):
        f = self.fixture
        result = f.run_cli("append", "--", "--proveo-cache-owner=literal")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(
            "--proveo-cache-owner=literal", f.wait_event("ready")["arguments"]
        )

    def test_ready_file_does_not_override_the_runtime_owned_state(self):
        f = self.fixture
        env = dict(f.env, PROVEO_OPENCODE_CACHE_HOME=str(f.root / "unprepared"))
        (f.durable / ".prepared.json").write_text(
            '{"state":"prepared","credential_free":true}'
        )
        result = f.run_cli("append", env=env)
        self.assertEqual(result.returncode, 78, result.stderr)
        self.assertEqual(f.events(), [])

    def test_kernel_owner_identity_prevents_socket_impersonation(self):
        f = self.fixture
        import importlib.machinery, importlib.util

        loader = importlib.machinery.SourceFileLoader(
            "cache_context_fixture", str(RUNTIME)
        )
        spec = importlib.util.spec_from_loader(loader.name, loader)
        runtime = importlib.util.module_from_spec(spec)
        loader.exec_module(runtime)
        result = f.run_cli("append", env=f.env | {})
        self.assertEqual(result.returncode, 0, result.stderr)
        command = f.command("append")
        command[command.index("--proveo-cache-owner=" + f.owner)] = (
            "--proveo-cache-owner=1:0"
        )
        result = subprocess.run(
            command, env=f.env, capture_output=True, text=True, timeout=5
        )
        self.assertEqual(result.returncode, 78, result.stderr)
        self.assertIn("identity changed", result.stderr)

    def test_cache_changes_require_maintenance(self):
        f = self.fixture
        first = f.run_cli("append")
        self.assertEqual(first.returncode, 0, first.stderr)
        cache = Path(f.wait_event("ready")["database"])
        with contextlib.closing(sqlite3.connect(cache)) as db:
            db.execute("UPDATE session_v2 SET title='unmanaged edit'")
            db.commit()
        result = f.run_cli("append")
        self.assertEqual(result.returncode, 78, result.stderr)
        self.assertIn("changed outside its owner", result.stderr)
        recovered = self.control("--recover")
        self.assertEqual(recovered.returncode, 0, recovered.stderr)

    def test_warm_handoff_preserves_cache_and_starts_within_five_seconds(self):
        f = self.fixture
        first = f.run_cli("append")
        self.assertEqual(first.returncode, 0, first.stderr)
        before = Path(f.wait_event("ready")["database"])
        begun = time.monotonic()
        second = f.start("hold")
        ready = f.wait_event("ready", count=2)
        self.assertLess(time.monotonic() - begun, 5)
        self.assertEqual(Path(ready["database"]), before)
        second.terminate()
        _, stderr = second.communicate(timeout=10)
        self.assertEqual(second.returncode, 143, stderr)
        f.assert_clean(messages=3)

    def test_post_exit_keeps_exactly_three_snapshot_publications(self):
        f = self.fixture
        import textwrap

        tree = ast.parse(
            textwrap.dedent(inspect.getsource(opencode_cache.Keeper.publish))
        )
        calls = [
            node.func.attr
            for node in ast.walk(tree)
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute)
        ]
        self.assertEqual(calls.count("snapshot"), 2)
        self.assertEqual(calls.count("checkpoint"), 1)
        with (f.durable / "opencode.db").open("rb") as db:
            self.assertEqual(db.read(16), b"SQLite format 3\x00")
        result = f.run_cli("append")
        self.assertEqual(result.returncode, 0, result.stderr)
        f.assert_clean(messages=2)
        cache = Path(f.wait_event("ready")["database"])
        with contextlib.closing(sqlite3.connect(cache)) as db:
            self.assertEqual(
                db.execute("SELECT count(*) FROM credential").fetchone(), (0,)
            )
        self.assertNotIn(b"SYNTHETIC_FIXTURE_CREDENTIAL", cache.read_bytes())

    def test_explicit_docker_disposable_mode_remains_functional(self):
        f = self.fixture
        env = dict(f.env, PROVEO_OPENCODE_HISTORY_MODE="disposable")
        result = subprocess.run(
            [sys.executable, "-B", str(RUNTIME), str(f.native), "append"],
            env=env,
            capture_output=True,
            text=True,
            timeout=20,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        f.assert_clean(messages=2)


class IntentLogTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.path = Path(self.temporary.name) / "intents.jsonl"

    def test_intents_sync_file_then_directory_before_returning(self):
        synced = []
        fsync = os.fsync

        def observe(descriptor):
            synced.append(stat.S_ISDIR(os.fstat(descriptor).st_mode))
            fsync(descriptor)

        values = [
            dict(path="opencode.db", op="replace", digest="first"),
            dict(path="artifact.txt", op="replace", digest="second"),
        ]
        with mock.patch.object(opencode_cache.os, "fsync", side_effect=observe):
            for value in values:
                opencode_cache.append_intent(self.path, value)
        self.assertEqual(synced, [False, True, False, True])
        self.assertEqual(
            [json.loads(line) for line in self.path.read_text().splitlines()], values
        )

    def test_directory_sync_failure_never_authorizes_publication(self):
        fsync = os.fsync

        def fail_directory(descriptor):
            if stat.S_ISDIR(os.fstat(descriptor).st_mode):
                raise OSError("injected intent directory sync failure")
            fsync(descriptor)

        with mock.patch.object(opencode_cache.os, "fsync", side_effect=fail_directory):
            with self.assertRaisesRegex(OSError, "directory sync failure"):
                opencode_cache.append_intent(
                    self.path, dict(path="opencode.db", op="replace", digest="pending")
                )

    def test_runtime_keeps_dead_history_merge_functions_deleted(self):
        functions = {
            node.name
            for node in ast.parse(RUNTIME.read_text()).body
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        }
        self.assertTrue(
            {
                "legacy_directory",
                "migrate_legacy",
                "canonical_identity",
                "migration_digest",
                "legacy_main",
            }.isdisjoint(functions)
        )
        self.assertIn("disposable_main", functions)


if __name__ == "__main__":
    unittest.main()
