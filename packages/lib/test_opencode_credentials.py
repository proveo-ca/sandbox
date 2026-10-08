# SPEC: _spec/_paradigms/credential-boundary.puml, _spec/internal/sbx/state-sync.puml

import contextlib
import importlib.util
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import unittest


HELPER = Path(__file__).with_name("opencode-credentials.py")
spec = importlib.util.spec_from_file_location("opencode_credentials", HELPER)
assert spec is not None and spec.loader is not None
credentials = importlib.util.module_from_spec(spec)
spec.loader.exec_module(credentials)
FAKE_VALUE = "SYNTHETIC_FIXTURE_CREDENTIAL_" + "x" * 20000
SCHEMA = """
CREATE TABLE credential (
 id text PRIMARY KEY, integration_id text, label text NOT NULL, value text NOT NULL,
 connector_id text, method_id text, active integer,
 time_created integer NOT NULL, time_updated integer NOT NULL
);
CREATE TABLE project (id text PRIMARY KEY);
CREATE TABLE session_v2 (
 id text PRIMARY KEY, project_id text NOT NULL REFERENCES project(id),
 title text, permission text, time_created integer NOT NULL, time_updated integer NOT NULL
);
CREATE TABLE session_message (
 id text PRIMARY KEY, session_id text NOT NULL REFERENCES session_v2(id),
 seq integer NOT NULL, data text NOT NULL
);
CREATE UNIQUE INDEX session_message_session_seq_idx ON session_message(session_id, seq);
CREATE TABLE permission (project_id text PRIMARY KEY REFERENCES project(id), data text NOT NULL);
CREATE TABLE migration (id text PRIMARY KEY);
INSERT INTO project VALUES ('project-fixture');
INSERT INTO session_v2 VALUES ('session-fixture', 'project-fixture', 'keep session',
 '[{"permission":"bash","action":"ask"}]', 1, 2);
INSERT INTO session_message VALUES ('message-fixture', 'session-fixture', 1, '{"text":"keep message"}');
INSERT INTO permission VALUES ('project-fixture', '{"edit":"allow","bash":"ask"}');
INSERT INTO migration VALUES ('20260805200742_import_legacy_credentials');
"""


class CredentialSnapshotTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "source"
        self.destination = self.root / "destination"
        self.source.mkdir()

    def fixture(self, directory=None, name="opencode.db"):
        directory = directory or self.source
        directory.mkdir(parents=True, exist_ok=True)
        connection = sqlite3.connect(directory / name)
        connection.executescript(SCHEMA)
        connection.executemany(
            "INSERT INTO credential VALUES (?, ?, ?, ?, NULL, NULL, ?, 1, 2)",
            [
                ("key-fixture", "fixture", "key", FAKE_VALUE, 1),
                ("oauth-fixture", "fixture", "oauth", FAKE_VALUE, 0),
            ],
        )
        connection.commit()
        return connection

    def assert_clean(self, directory=None, name="opencode.db", messages=1):
        directory = directory or self.destination
        path = directory / name
        with contextlib.closing(sqlite3.connect(path)) as db:
            self.assertEqual(db.execute("PRAGMA quick_check").fetchall(), [("ok",)])
            self.assertEqual(db.execute("PRAGMA foreign_key_check").fetchall(), [])
            self.assertEqual(
                db.execute("SELECT count(*) FROM credential").fetchone(), (0,)
            )
            self.assertEqual(
                db.execute("SELECT title, permission FROM session_v2").fetchone(),
                ("keep session", '[{"permission":"bash","action":"ask"}]'),
            )
            self.assertEqual(
                db.execute("SELECT count(*) FROM session_message").fetchone(),
                (messages,),
            )
            self.assertEqual(
                db.execute("SELECT data FROM permission").fetchone(),
                ('{"edit":"allow","bash":"ask"}',),
            )
            self.assertEqual(
                db.execute("SELECT id FROM migration").fetchone(),
                ("20260805200742_import_legacy_credentials",),
            )
            self.assertEqual(db.execute("PRAGMA journal_mode").fetchone(), ("delete",))
        self.assertNotIn(FAKE_VALUE.encode(), path.read_bytes())
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        for suffix in credentials.SIDECARS:
            self.assertFalse(Path(str(path) + suffix).exists())

    def test_snapshot_preserves_sessions_permissions_and_source_auth(self):
        with contextlib.closing(self.fixture()) as source:
            (self.source / "auth.json").write_text("SYNTHETIC_AUTH_FILE")
            (self.source / "log").mkdir()
            (self.source / "log" / "fixture.log").write_text("keep log")
            before = (self.source / "opencode.db").read_bytes()
            credentials.snapshot(self.source, self.destination)
            self.assert_clean()
            self.assertEqual(
                source.execute("SELECT count(*) FROM credential").fetchone(), (2,)
            )
            self.assertEqual((self.source / "opencode.db").read_bytes(), before)
            self.assertTrue((self.source / "auth.json").exists())
            self.assertFalse((self.destination / "auth.json").exists())
            self.assertEqual(
                (self.destination / "log" / "fixture.log").read_text(), "keep log"
            )

    def test_live_wal_backup_includes_committed_state_without_mutating_auth(self):
        with contextlib.closing(self.fixture()) as writer:
            writer.execute("PRAGMA journal_mode = WAL")
            writer.execute("PRAGMA wal_autocheckpoint = 0")
            writer.execute(
                "INSERT INTO session_message VALUES ('wal-message', 'session-fixture', 2, 'keep WAL')"
            )
            writer.commit()
            with contextlib.closing(
                sqlite3.connect(
                    (self.source / "opencode.db").as_uri() + "?immutable=1", uri=True
                )
            ) as main:
                self.assertEqual(
                    main.execute("SELECT count(*) FROM session_message").fetchone(),
                    (1,),
                )
            self.assertGreater((self.source / "opencode.db-wal").stat().st_size, 0)
            credentials.snapshot(self.source, self.destination)
            self.assert_clean(messages=2)
            self.assertEqual(
                writer.execute("SELECT count(*) FROM credential").fetchone(), (2,)
            )
            writer.execute(
                "INSERT INTO session_message VALUES ('still-live', 'session-fixture', 3, 'keep live')"
            )
            writer.commit()
            self.assertEqual(writer.execute("PRAGMA quick_check").fetchone(), ("ok",))

    def test_repeat_and_next_run_are_idempotent(self):
        self.fixture().close()
        credentials.snapshot(self.source, self.destination)
        first = (self.destination / "opencode.db").read_bytes()
        credentials.snapshot(self.source, self.destination)
        self.assert_clean()
        self.assertEqual((self.destination / "opencode.db").read_bytes(), first)
        next_run = self.root / "next-run"
        credentials.snapshot(self.destination, next_run)
        self.assert_clean(next_run)

    def test_channel_custom_and_destination_only_databases_are_clean(self):
        self.fixture(name="opencode-next.db").close()
        self.fixture(name="custom-database").close()
        self.fixture(directory=self.destination, name="opencode.db").close()
        credentials.snapshot(self.source, self.destination)
        for name in ("opencode-next.db", "custom-database", "opencode.db"):
            self.assert_clean(name=name)

    def test_malformed_unsupported_and_orphan_wal_leave_previous_snapshot_intact(self):
        self.fixture().close()
        credentials.snapshot(self.source, self.destination)
        before = (self.destination / "opencode.db").read_bytes()
        for kind in ("malformed", "unsupported", "trigger", "reference", "orphan"):
            with self.subTest(kind=kind):
                bad = self.root / kind
                bad.mkdir()
                path = bad / "opencode.db"
                if kind == "malformed":
                    path.write_bytes(b"not sqlite")
                elif kind == "orphan":
                    (bad / "opencode.db-wal").write_bytes(b"orphan")
                else:
                    with contextlib.closing(self.fixture(directory=bad)) as db:
                        if kind == "unsupported":
                            db.execute(
                                "ALTER TABLE credential ADD COLUMN secret_v3 text"
                            )
                        elif kind == "trigger":
                            db.execute(
                                "CREATE TRIGGER copy_auth AFTER DELETE ON credential BEGIN UPDATE permission SET data = old.value; END"
                            )
                        else:
                            db.execute(
                                "CREATE TABLE auth_reference (id text REFERENCES credential(id) ON DELETE CASCADE)"
                            )
                        db.commit()
                with self.assertRaises((credentials.SnapshotError, sqlite3.Error)):
                    credentials.snapshot(bad, self.destination)
                self.assertEqual(
                    (self.destination / "opencode.db").read_bytes(), before
                )
                self.assert_clean()

    def test_destination_with_wal_is_refused_without_disturbing_active_session(self):
        self.fixture().close()
        with contextlib.closing(self.fixture(directory=self.destination)) as writer:
            writer.execute("PRAGMA journal_mode = WAL")
            writer.execute(
                "INSERT INTO session_message VALUES ('live', 'session-fixture', 2, 'live destination')"
            )
            writer.commit()
            before = (self.destination / "opencode.db-wal").read_bytes()
            with self.assertRaises(credentials.SnapshotError):
                credentials.snapshot(self.source, self.destination)
            self.assertEqual(
                (self.destination / "opencode.db-wal").read_bytes(), before
            )
            self.assertEqual(
                writer.execute("SELECT count(*) FROM credential").fetchone(), (2,)
            )
            self.assertEqual(
                writer.execute("SELECT count(*) FROM session_message").fetchone(), (2,)
            )

    def test_same_directory_nested_destination_and_symlinks_are_refused(self):
        self.fixture().close()
        for destination in (self.source, self.source / "nested", self.root):
            with self.assertRaises(credentials.SnapshotError):
                credentials.snapshot(self.source, destination)
        outside = self.root / "outside"
        outside.write_text("keep outside")
        (self.source / "link").symlink_to(outside)
        with self.assertRaises(credentials.SnapshotError):
            credentials.snapshot(self.source, self.destination)
        self.assertEqual(outside.read_text(), "keep outside")

    def test_legacy_session_database_without_credential_table_survives(self):
        with contextlib.closing(sqlite3.connect(self.source / "opencode.db")) as db:
            db.executescript(
                "CREATE TABLE project(id text); CREATE TABLE session(id text); "
                "CREATE TABLE message(id text); CREATE TABLE part(id text); "
                "INSERT INTO session VALUES ('legacy-session');"
            )
        credentials.snapshot(self.source, self.destination)
        with contextlib.closing(
            sqlite3.connect(self.destination / "opencode.db")
        ) as db:
            self.assertEqual(
                db.execute("SELECT id FROM session").fetchone(), ("legacy-session",)
            )

    def test_cli_does_not_disclose_fixture_auth_and_leaves_environment_untouched(self):
        (self.source / "opencode.db").write_bytes(b"SYNTHETIC_INVALID_DATABASE")
        env = dict(os.environ, OPENAI_API_KEY="SYNTHETIC_ENV_KEY")
        result = subprocess.run(
            [
                "python3",
                str(HELPER),
                "--offline-destination",
                str(self.source),
                str(self.destination),
            ],
            env=env,
            capture_output=True,
        )
        self.assertEqual(result.returncode, 1)
        self.assertNotIn(b"SYNTHETIC", result.stdout + result.stderr)
        self.assertEqual(env["OPENAI_API_KEY"], "SYNTHETIC_ENV_KEY")

    def test_shell_state_round_trip_preserves_environment_auth(self):
        agent = self.root / "agent"
        data = agent / ".local" / "share" / "opencode"
        self.fixture(directory=data).close()
        (data / "auth.json").write_text("SYNTHETIC_AUTH_FILE")
        state = self.root / "state"
        state.mkdir()
        next_agent = self.root / "next-agent"
        next_agent.mkdir()
        library = HELPER.with_name("entrypoint-lib.sh")
        script = """set -eu
source "$1"
_proveo_volume_state_dirs() { printf '%s\\n' "$HOME/.local/share/opencode"; }
proveo_sync_state save
test "$OPENAI_API_KEY" = SYNTHETIC_ENV_KEY
HOME="$2"
proveo_sync_state restore
test "$OPENAI_API_KEY" = SYNTHETIC_ENV_KEY
"""
        env = dict(
            os.environ,
            HOME=str(agent),
            PROVEO_HOME="",
            PROVEO_STATE_HOME=str(state),
            PROVEO_OPENCODE_CREDENTIAL_HELPER=str(HELPER),
            OPENAI_API_KEY="SYNTHETIC_ENV_KEY",
        )
        result = subprocess.run(
            ["bash", "-c", script, "bash", str(library), str(next_agent)],
            env=env,
            capture_output=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assert_clean(state / ".local" / "share" / "opencode")
        self.assert_clean(next_agent / ".local" / "share" / "opencode")
        self.assertTrue((data / "auth.json").exists())

    def test_shell_missing_helper_cannot_copy_raw_database(self):
        agent = self.root / "agent"
        data = agent / ".local" / "share" / "opencode"
        self.fixture(directory=data).close()
        state = self.root / "state"
        state.mkdir()
        env = dict(
            os.environ,
            HOME=str(agent),
            PROVEO_HOME="",
            PROVEO_STATE_HOME=str(state),
            PROVEO_OPENCODE_CREDENTIAL_HELPER=str(self.root / "missing-helper"),
        )
        script = """source "$1"
_proveo_volume_state_dirs() { printf '%s\\n' "$HOME/.local/share/opencode"; }
proveo_sync_state save
"""
        result = subprocess.run(
            ["bash", "-c", script, "bash", str(HELPER.with_name("entrypoint-lib.sh"))],
            env=env,
            capture_output=True,
        )
        self.assertEqual(result.returncode, 1)
        self.assertFalse(
            (state / ".local" / "share" / "opencode" / "opencode.db").exists()
        )


if __name__ == "__main__":
    unittest.main()
