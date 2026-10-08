# SPEC: _spec/defs/opencode/native-v2-integration.puml, _spec/_paradigms/credential-boundary.puml

import contextlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest

from test_opencode_credentials import credentials


RUNTIME = Path(__file__).with_name("proveo-opencode-runtime")
NATIVE = os.environ.get("PROVEO_TEST_OPENCODE_NATIVE", "")


@unittest.skipUnless(NATIVE, "native OpenCode image executable was not supplied")
class NativeRuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.home = self.root / "home"
        self.home.mkdir()
        self.durable = self.home / ".local/share/opencode"
        self.env = {
            "PATH": os.environ["PATH"],
            "HOME": str(self.home),
            "PROVEO_HOME": str(self.home),
            "PROVEO_OPENCODE_CREDENTIAL_HELPER": str(
                RUNTIME.with_name("opencode-credentials.py")
            ),
            "OPENCODE_DISABLE_AUTOUPDATE": "1",
            "PYTHONDONTWRITEBYTECODE": "1",
            "XDG_CACHE_HOME": "/home/agent/.cache",
            "TERM": "dumb",
            "OPENCODE_CONFIG_CONTENT": '{"plugins":[],"mcp":{"servers":{}}}',
        }

    def run_native(self, *args):
        return subprocess.run(
            [sys.executable, "-B", str(RUNTIME), NATIVE, *args],
            env=self.env,
            cwd=self.root,
            capture_output=True,
            text=True,
            timeout=60,
        )

    def initialize(self):
        result = self.run_native("session", "list", "--format", "json")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), [])
        self.assertTrue((self.durable / "opencode.db").exists())

    def test_tagged_schema_credentials_sessions_and_real_session_list(self):
        self.initialize()
        with contextlib.closing(sqlite3.connect(self.durable / "opencode.db")) as db:
            self.assertTrue(credentials.credential_schema(db))
            project_id = db.execute(
                "SELECT id FROM project WHERE worktree = ?", (str(self.root),)
            ).fetchone()[0]
            db.execute(
                "INSERT INTO session_v2 (id, project_id, slug, directory, title, version, permission, time_created, time_updated) "
                "VALUES ('ses_fixture_native', ?, 'fixture', ?, 'native fixture session', '2.0.25', ?, 1, 2)",
                (
                    project_id,
                    str(self.root),
                    '[{"action":"shell","resource":"*","effect":"ask"}]',
                ),
            )
            db.execute(
                "INSERT INTO credential (id, integration_id, label, value, time_created, time_updated) "
                "VALUES ('cred_fixture', 'fixture', 'fixture', ?, 1, 2)",
                ('{"type":"key","key":"SYNTHETIC_NATIVE_KEY"}',),
            )
            db.commit()
        result = self.run_native("session", "list", "--format", "json")
        self.assertEqual(result.returncode, 0, result.stderr)
        sessions = json.loads(result.stdout)
        self.assertTrue(
            any(session["id"] == "ses_fixture_native" for session in sessions), sessions
        )
        with contextlib.closing(sqlite3.connect(self.durable / "opencode.db")) as db:
            self.assertEqual(
                db.execute("SELECT count(*) FROM credential").fetchone(), (0,)
            )
            self.assertEqual(
                db.execute(
                    "SELECT permission FROM session_v2 WHERE id = 'ses_fixture_native'"
                ).fetchone(),
                ('[{"action":"shell","resource":"*","effect":"ask"}]',),
            )
            self.assertEqual(db.execute("PRAGMA quick_check").fetchone(), ("ok",))
        self.assertNotIn(
            b"SYNTHETIC_NATIVE_KEY", (self.durable / "opencode.db").read_bytes()
        )

    def test_real_diagnostics_and_native_agent_listing(self):
        for args in (("--version",), ("--help",), ("debug", "agents")):
            result = self.run_native(*args)
            self.assertEqual(result.returncode, 0, result.stderr)
            if args == ("--version",):
                self.assertEqual(result.stdout.strip(), "opencode v2.0.25")
            if args == ("debug", "agents"):
                self.assertIsInstance(json.loads(result.stdout), list)
        self.assertFalse(self.durable.exists())

    def test_await_seed_and_runtime_shim_cover_docker_and_sbx(self):
        shim = self.root / "opencode"
        await_seed = self.root / "proveo-await-seed"
        await_seed.write_text(RUNTIME.with_name("proveo-await-seed").read_text())
        await_seed.chmod(0o700)
        shim.write_text(f'#!/bin/sh\nexec "{await_seed}" "{RUNTIME}" "{NATIVE}" "$@"\n')
        shim.chmod(0o700)
        docker = subprocess.run(
            [str(shim), "session", "list", "--format", "json"],
            env=self.env,
            cwd=self.root,
            capture_output=True,
            text=True,
            timeout=60,
        )
        self.assertEqual(docker.returncode, 0, docker.stderr)
        self.assertEqual(json.loads(docker.stdout), [])
        self.assertTrue((self.durable / "opencode.db").exists())
        state = self.root / "host-state"
        state.mkdir()
        marker = self.root / "seeded"
        marker.touch()
        env = dict(
            self.env,
            SANDBOX_VM_ID="synthetic-kit",
            PROVEO_WORKDIR=str(self.root),
            PROVEO_INSTRUCTIONS_MARKER=str(marker),
            PROVEO_STATE_HOME=str(state),
            PROVEO_CONFIG_DIRS="opencode/share|.local/share/opencode|auth.json",
        )
        sbx = subprocess.run(
            [str(shim), "session", "list", "--format", "json"],
            env=env,
            cwd=self.root,
            capture_output=True,
            text=True,
            timeout=60,
        )
        self.assertEqual(sbx.returncode, 0, sbx.stderr)
        self.assertEqual(json.loads(sbx.stdout), [])
        with contextlib.closing(
            sqlite3.connect(state / "opencode/share/opencode.db")
        ) as db:
            self.assertTrue(credentials.credential_schema(db))
            self.assertEqual(
                db.execute("SELECT count(*) FROM credential").fetchone(), (0,)
            )


if __name__ == "__main__":
    unittest.main()
