# SPEC: _spec/defs/opencode/native-v2-integration.puml, _spec/_paradigms/credential-boundary.puml

import contextlib
import json
import os
from pathlib import Path
import signal
import sqlite3
import subprocess
import sys
import tempfile
import time
import unittest

from test_opencode_credentials import (
    FAKE_VALUE,
    HELPER,
    SCHEMA,
)


RUNTIME = HELPER.with_name("proveo-opencode-runtime")
CHILD = HELPER.with_name("opencode-runtime-fixture.py")


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.home = self.root / "home"
        self.home.mkdir()
        self.durable = self.home / ".local/share/opencode"
        self.durable.mkdir(parents=True)
        self.fixture(self.durable)
        self.native = self.root / "native-opencode"
        self.native.write_text("#!/usr/bin/env python3\n" + CHILD.read_text())
        self.native.chmod(0o700)
        self.env = dict(
            PATH=os.environ["PATH"],
            HOME=str(self.home),
            PROVEO_HOME=str(self.home),
            PROVEO_STATE_HOME="",
            PROVEO_CONFIG_DIRS="",
            XDG_DATA_HOME="",
            OPENCODE_DB="",
            PROVEO_OPENCODE_DURABLE_DATA="",
            PROVEO_OPENCODE_RUNTIME_DATA="",
            PROVEO_GIT_SYNC_MSG_INFLIGHT="",
            PROVEO_OPENCODE_CREDENTIAL_HELPER=str(HELPER),
            PROVEO_RUNTIME_EVENTS=str(self.root / "events.jsonl"),
            OPENAI_API_KEY="SYNTHETIC_ENV_KEY",
            PYTHONDONTWRITEBYTECODE="1",
            PYTHONPATH=str(HELPER.parent),
        )
        self.env.pop("OPENCODE_DB")

    def fixture(self, directory):
        directory.mkdir(parents=True, exist_ok=True)
        with contextlib.closing(sqlite3.connect(directory / "opencode.db")) as db:
            db.executescript(SCHEMA)
            db.execute(
                "INSERT INTO credential VALUES ('before', 'fixture', 'before', ?, NULL, NULL, 1, 1, 2)",
                (FAKE_VALUE,),
            )
            db.commit()
        (directory / "auth.json").write_text("SYNTHETIC_LEGACY_AUTH")

    def command(self, *args):
        return [sys.executable, "-B", str(RUNTIME), str(self.native), *args]

    def run_cli(self, *args, env=None):
        return subprocess.run(
            self.command(*args),
            env=env or self.env,
            capture_output=True,
            text=True,
            timeout=30,
        )

    def start(self, *args, env=None):
        proc = subprocess.Popen(
            self.command(*args),
            env=env or self.env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        self.addCleanup(self.stop, proc)
        return proc

    def stop(self, proc):
        if proc.poll() is None:
            proc.terminate()
        with contextlib.suppress(subprocess.TimeoutExpired):
            proc.communicate(timeout=10)
        if proc.poll() is None:
            proc.kill()
            proc.communicate(timeout=5)

    def events(self):
        file = self.root / "events.jsonl"
        return (
            [json.loads(line) for line in file.read_text().splitlines()]
            if file.exists()
            else []
        )

    def wait_event(self, name, count=1):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            events = [event for event in self.events() if event["event"] == name]
            if len(events) >= count:
                return events[-1]
            time.sleep(0.025)
        self.fail("runtime fixture did not reach " + name)

    def assert_clean(self, directory=None, messages=1):
        directory = directory or self.durable
        with contextlib.closing(sqlite3.connect(directory / "opencode.db")) as db:
            self.assertEqual(
                db.execute("SELECT count(*) FROM credential").fetchone(), (0,)
            )
            self.assertEqual(
                db.execute("SELECT count(*) FROM session_message").fetchone(),
                (messages,),
            )
            self.assertEqual(
                db.execute("SELECT permission FROM session_v2").fetchone(),
                ('[{"permission":"bash","action":"ask"}]',),
            )
            self.assertEqual(db.execute("PRAGMA quick_check").fetchone(), ("ok",))
        self.assertFalse((directory / "auth.json").exists())
        self.assertNotIn(b"SYNTHETIC", (directory / "opencode.db").read_bytes())

    def test_two_runs_reject_overlap_and_restart_preserves_sessions(self):
        first = self.start("hold")
        ready = self.wait_event("ready")
        self.assert_clean()
        self.assertTrue(Path(ready["database"]).is_relative_to("/tmp"))
        self.assertFalse(Path(ready["database"]).is_relative_to(self.home))
        overlap = self.run_cli("append")
        self.assertEqual(overlap.returncode, 75, overlap.stderr)
        self.assertIn("another OpenCode run", overlap.stderr)
        first.send_signal(signal.SIGTERM)
        first.communicate(timeout=10)
        self.assertEqual(first.returncode, 143)
        self.assert_clean(messages=2)
        self.assertFalse(Path(ready["database"]).parent.exists())
        restart = self.run_cli("append")
        self.assertEqual(restart.returncode, 0, restart.stderr)
        self.assert_clean(messages=3)

    def test_interrupted_supervisor_keeps_child_lease_then_recovers_without_saved_auth(
        self,
    ):
        first = self.start("hold")
        ready = self.wait_event("ready")
        first.kill()
        overlap = self.run_cli("append")
        self.assertEqual(overlap.returncode, 75, overlap.stderr)
        os.kill(ready["pid"], signal.SIGTERM)
        first.communicate(timeout=10)
        self.assertEqual(first.returncode, -signal.SIGKILL)
        self.assert_clean(messages=1)
        restart = self.run_cli("append")
        self.assertEqual(restart.returncode, 0, restart.stderr)
        self.assert_clean(messages=3)
        self.assertFalse(Path(ready["database"]).parent.exists())

    def test_sbx_maps_manifest_store_and_never_uses_live_home_data(self):
        state = self.root / "host-state"
        store = state / "opencode/share"
        self.fixture(store)
        env = dict(
            self.env,
            PROVEO_STATE_HOME=str(state),
            PROVEO_CONFIG_DIRS="opencode/config|.config/opencode|;opencode/share|.local/share/opencode|auth.json",
        )
        result = self.run_cli("append", env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_clean(store, messages=2)
        with contextlib.closing(sqlite3.connect(self.durable / "opencode.db")) as db:
            self.assertEqual(
                db.execute("SELECT count(*) FROM credential").fetchone(), (1,)
            )

    def test_version_help_and_debug_do_not_contend_with_active_run(self):
        first = self.start("hold")
        self.wait_event("ready")
        for args in (("--version",), ("--help",), ("debug", "agents")):
            result = self.run_cli(*args)
            self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNone(first.poll())
        self.assert_clean()

    def test_commit_subject_recursion_uses_disposable_state(self):
        first = self.start("hold")
        ready = self.wait_event("ready")
        env = dict(
            self.env,
            PROVEO_GIT_SYNC_MSG_INFLIGHT="1",
            PROVEO_OPENCODE_DURABLE_DATA=str(self.durable),
            PROVEO_OPENCODE_RUNTIME_DATA=str(Path(ready["database"]).parent),
            XDG_DATA_HOME=str(Path(ready["database"]).parent.parent),
            OPENCODE_DB=ready["database"],
        )
        helper = self.run_cli("append", env=env)
        self.assertEqual(helper.returncode, 0, helper.stderr)
        self.assert_clean()
        first.terminate()
        first.communicate(timeout=10)
        self.assert_clean(messages=2)

    def test_external_database_overrides_are_rejected_without_launch(self):
        for value in (
            str(self.root / "external.db"),
            "../outside.db",
            "nested/db",
            ":memory:",
            "auth.json",
        ):
            result = self.run_cli("append", env=dict(self.env, OPENCODE_DB=value))
            self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.events(), [])
        self.assertFalse((self.root / "external.db").exists())

    def test_stateful_session_commands_contend_and_keep_the_native_arguments(self):
        first = self.start("hold")
        self.wait_event("ready")
        for args in (
            ("session", "list"),
            ("--continue",),
            ("--session", "session-fixture"),
        ):
            result = self.run_cli(*args)
            self.assertEqual(result.returncode, 75, result.stderr)
        first.terminate()
        first.communicate(timeout=10)
        self.assert_clean(messages=2)

        resumed = self.run_cli("--continue")
        self.assertEqual(resumed.returncode, 0, resumed.stderr)
        self.assertEqual(
            self.wait_event("ready", count=2)["arguments"],
            ["--continue", "--standalone"],
        )
        self.assert_clean(messages=3)

    def test_service_configuration_cannot_redirect_the_private_database(self):
        config = self.home / ".config/opencode"
        config.mkdir(parents=True)
        (config / "service.json").write_text(
            json.dumps({"env": {"OPENCODE_DB": str(self.root / "outside.db")}})
        )
        result = self.run_cli("append")
        self.assertEqual(result.returncode, 74)
        self.assertEqual(self.events(), [])
        self.assertFalse((self.root / "outside.db").exists())

    def test_root_option_values_do_not_turn_a_session_into_a_diagnostic(self):
        result = self.run_cli("--prompt", "debug")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_clean(messages=2)

    def test_interrupt_and_hangup_publish_after_child_shutdown(self):
        for index, sig in enumerate((signal.SIGINT, signal.SIGHUP), start=1):
            proc = self.start("hold")
            self.wait_event("ready", count=index)
            proc.send_signal(sig)
            proc.communicate(timeout=10)
            self.assertEqual(proc.returncode, 128 + sig)
            self.assert_clean(messages=index + 1)

    def test_wal_source_is_checkpointed_only_after_offline_exclusion(self):
        with contextlib.closing(sqlite3.connect(self.durable / "opencode.db")) as live:
            live.execute("PRAGMA journal_mode = WAL")
            live.execute(
                "INSERT INTO session_message VALUES ('live', 'session-fixture', 2, 'live')"
            )
            live.commit()
            before = (self.durable / "opencode.db-wal").read_bytes()
            refused = self.run_cli("append")
            self.assertNotEqual(refused.returncode, 0)
            self.assertEqual((self.durable / "opencode.db-wal").read_bytes(), before)
            self.assertEqual(
                live.execute("SELECT count(*) FROM credential").fetchone(), (1,)
            )
        result = self.run_cli("append")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_clean(messages=3)

    def test_detached_child_finishes_before_publication_and_lease_release(self):
        proc = self.start("detach")
        self.wait_event("detached")
        overlap = self.run_cli("append")
        self.assertEqual(overlap.returncode, 75, overlap.stderr)
        proc.communicate(timeout=15)
        self.assertEqual(proc.returncode, 0)
        self.assert_clean(messages=2)
        self.assertTrue(
            any(event["event"] == "detached-stopped" for event in self.events())
        )

    def test_failed_save_retains_only_credential_free_recovery(self):
        result = self.run_cli("damage")
        self.assertEqual(result.returncode, 74, result.stderr)
        self.assert_clean()
        recoveries = list((self.durable / ".proveo-opencode-recovery").glob("run-*"))
        self.assertEqual(len(recoveries), 1)
        self.assert_clean(recoveries[0])
        ready = self.wait_event("ready")
        self.assertFalse(Path(ready["database"]).parent.exists())
        self.assertIn("credential-free recovery", result.stderr)

    def test_failed_publication_preserves_latest_clean_history_and_existing_store(self):
        result = self.run_cli("obstruct")
        self.assertEqual(result.returncode, 74, result.stderr)
        self.assertEqual(
            (self.durable / "opencode.db-wal").read_bytes(),
            b"synthetic publication obstruction",
        )
        (self.durable / "opencode.db-wal").unlink()
        self.assert_clean(messages=1)
        recoveries = list((self.durable / ".proveo-opencode-recovery").glob("run-*"))
        self.assertEqual(len(recoveries), 1)
        self.assert_clean(recoveries[0], messages=2)
        ready = self.wait_event("ready")
        self.assertFalse(Path(ready["database"]).parent.exists())

    def test_config_and_state_sync_skip_runtime_data_and_keep_other_artifacts(self):
        state = self.root / "sync-state"
        store = state / "opencode/share"
        self.fixture(store)
        before = (store / "opencode.db").read_bytes()
        (self.home / "settings.json").write_text("keep unrelated config file")
        sessions = self.home / ".claude/sessions"
        sessions.mkdir(parents=True)
        (sessions / "fixture").write_text("keep unrelated session")
        env = dict(
            self.env,
            PROVEO_STATE_HOME=str(state),
            PROVEO_CONFIG_DIRS="opencode/share|.local/share/opencode|auth.json",
            PROVEO_CONFIG_FILES="settings.json",
            PROVEO_CONFIG_SYNC="on",
        )
        script = """set -eu
source "$1"
_proveo_volume_state_dirs() { printf '%s\\n' "$HOME/.local/share/opencode" "$HOME/.claude/sessions"; }
proveo_sync_config restore
proveo_sync_state restore
proveo_sync_config save
proveo_sync_state save
"""
        result = subprocess.run(
            ["bash", "-c", script, "bash", str(HELPER.with_name("entrypoint-lib.sh"))],
            env=env,
            capture_output=True,
            text=True,
            timeout=10,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((store / "opencode.db").read_bytes(), before)
        self.assertFalse((state / ".local/share/opencode/opencode.db").exists())
        self.assertEqual(
            (state / "settings.json").read_text(), "keep unrelated config file"
        )
        self.assertEqual(
            (state / ".claude/sessions/fixture").read_text(), "keep unrelated session"
        )


if __name__ == "__main__":
    unittest.main()
