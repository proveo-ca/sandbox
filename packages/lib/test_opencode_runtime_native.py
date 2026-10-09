# SPEC: _spec/defs/opencode/native-v2-integration.puml, _spec/_paradigms/credential-boundary.puml

import contextlib
import json
import os
import pty
import fcntl
import select
import signal
import struct
import termios
import time
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
        self.durable = self.home / "opencode/share"
        self.env = {
            "PATH": os.environ["PATH"],
            "HOME": str(self.home),
            "PROVEO_HOME": str(self.home),
            "PROVEO_SEED_REFUSED": str(self.root / "seed-refused"),
            "PROVEO_OPENCODE_CREDENTIAL_HELPER": str(
                RUNTIME.with_name("opencode-credentials.py")
            ),
            "PROVEO_OPENCODE_HOME_MANIFEST": str(
                RUNTIME.parents[2] / "defs/opencode/harness.manifest"
            ),
            "OPENCODE_DISABLE_MODELS_FETCH": "1",
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
                supplied = os.environ.get("PROVEO_TEST_OPENCODE_VERSION")
                if supplied:
                    self.assertEqual(result.stdout.strip(), "opencode v" + supplied)
                else:
                    self.assertRegex(result.stdout.strip(), r"^opencode v2\.\d+\.\d+$")
            if args == ("debug", "agents"):
                self.assertIsInstance(json.loads(result.stdout), list)
        self.assertFalse(self.durable.exists())

    def test_native_account_schemas_and_control_tokens_are_scrubbed(self):
        self.initialize()
        with contextlib.closing(sqlite3.connect(self.durable / "opencode.db")) as db:
            for table, expected in credentials.TOKEN_SCHEMAS.items():
                actual = [
                    (row[1], row[2], row[3], row[5])
                    for row in db.execute(f"PRAGMA table_info({table})")
                ]
                self.assertEqual(actual, expected)
            db.execute(
                "INSERT INTO account (id, email, url, access_token, refresh_token, token_expiry, time_created, time_updated) "
                "VALUES ('account_fixture', 'fixture@example.test', 'https://example.test', 'SYNTHETIC_ACCOUNT_ACCESS', 'SYNTHETIC_ACCOUNT_REFRESH', 1, 1, 2)"
            )
            db.execute(
                "INSERT INTO control_account (email, url, access_token, refresh_token, token_expiry, active, time_created, time_updated) "
                "VALUES ('fixture@example.test', 'https://example.test', 'SYNTHETIC_CONTROL_ACCESS', 'SYNTHETIC_CONTROL_REFRESH', 1, 1, 1, 2)"
            )
            db.execute(
                "INSERT OR REPLACE INTO account_state VALUES (1, 'account_fixture', 'org_fixture')"
            )
            db.commit()
        (self.durable / "mcp-auth.json").write_text("SYNTHETIC_LEGACY_MCP_SECRET")
        result = self.run_native("session", "list", "--format", "json")
        self.assertEqual(result.returncode, 0, result.stderr)
        with contextlib.closing(sqlite3.connect(self.durable / "opencode.db")) as db:
            for table in ("credential", "account", "control_account"):
                self.assertEqual(
                    db.execute(f"SELECT count(*) FROM {table}").fetchone(), (0,)
                )
            self.assertEqual(
                db.execute(
                    "SELECT active_account_id, active_org_id FROM account_state WHERE id=1"
                ).fetchone(),
                (None, None),
            )
            self.assertEqual(db.execute("PRAGMA foreign_key_check").fetchall(), [])
        self.assertFalse((self.durable / "mcp-auth.json").exists())
        self.assertNotIn(
            b"SYNTHETIC_ACCOUNT", (self.durable / "opencode.db").read_bytes()
        )
        self.assertNotIn(
            b"SYNTHETIC_CONTROL", (self.durable / "opencode.db").read_bytes()
        )

    def test_actual_truncation_output_remains_accessible_after_resume(self):
        driver = RUNTIME.with_name("opencode-truncation-driver.mjs")
        result = subprocess.run(
            ["node", str(driver), sys.executable, str(RUNTIME), NATIVE, str(self.root)],
            env=self.env,
            capture_output=True,
            text=True,
            timeout=60,
        )
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        with contextlib.closing(sqlite3.connect(self.durable / "opencode.db")) as db:
            messages = [
                json.loads(row[0])
                for row in db.execute("SELECT data FROM session_message")
            ]
        paths = []

        def visit(value):
            if isinstance(value, dict):
                if "outputPath" in value:
                    paths.append(value["outputPath"])
                for item in value.values():
                    visit(item)
            elif isinstance(value, list):
                for item in value:
                    visit(item)

        visit(messages)
        self.assertEqual(len(set(paths)), 1, messages)
        output = Path(paths[0])
        self.assertTrue(output.is_relative_to(self.durable))
        self.assertGreater(output.stat().st_size, 50 * 1024)
        self.assertEqual(output.read_text().count("native truncation fixture"), 5000)
        self.assertIn(str(output), json.dumps(messages))
        resumed = self.run_native("session", "list", "--format", "json")
        self.assertEqual(resumed.returncode, 0, resumed.stderr)
        self.assertTrue(output.exists())
        moved_home = self.root / "host-visible-home"
        self.home.rename(moved_home)
        self.home = moved_home
        self.durable = moved_home / "opencode/share"
        self.env["HOME"] = str(moved_home)
        self.env["PROVEO_HOME"] = str(moved_home)
        self.env["PROVEO_STATE_HOME"] = str(moved_home)
        self.env["PROVEO_CONFIG_DIRS"] = (
            "opencode/share|.local/share/opencode|auth.json"
        )
        resumed = self.run_native("session", "list", "--format", "json")
        self.assertEqual(resumed.returncode, 0, resumed.stderr)
        with contextlib.closing(sqlite3.connect(self.durable / "opencode.db")) as db:
            messages = [
                json.loads(row[0])
                for row in db.execute("SELECT data FROM session_message")
            ]
        paths.clear()
        visit(messages)
        self.assertEqual(len(set(paths)), 1)
        moved_output = Path(paths[0])
        self.assertTrue(moved_output.is_relative_to(self.durable))
        self.assertEqual(
            moved_output.read_text().count("native truncation fixture"), 5000
        )

    def test_two_native_runs_and_resume_preserve_both_session_transcripts(self):
        driver = RUNTIME.with_name("opencode-truncation-driver.mjs")

        def run(session=None):
            command = [
                "node",
                str(driver),
                sys.executable,
                str(RUNTIME),
                NATIVE,
                str(self.root),
            ]
            if session is not None:
                command.append(session)
            result = subprocess.run(
                command, env=self.env, capture_output=True, text=True, timeout=60
            )
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)

        def transcripts():
            with contextlib.closing(
                sqlite3.connect(self.durable / "opencode.db")
            ) as db:
                return {
                    session_id: db.execute(
                        "SELECT data FROM session_message WHERE session_id = ? ORDER BY time_created",
                        (session_id,),
                    ).fetchall()
                    for (session_id,) in db.execute(
                        "SELECT id FROM session_v2 WHERE parent_id IS NULL ORDER BY time_created"
                    ).fetchall()
                }

        run()
        first = transcripts()
        self.assertEqual(len(first), 1)
        first_id = next(iter(first))
        self.assertTrue(first[first_id])
        run()
        both = transcripts()
        self.assertEqual(len(both), 2)
        self.assertEqual(both[first_id], first[first_id])
        second_id = next(session_id for session_id in both if session_id != first_id)
        self.assertTrue(both[second_id])
        run(first_id)
        resumed = transcripts()
        self.assertEqual(set(resumed), set(both))
        self.assertEqual(resumed[second_id], both[second_id])
        self.assertGreater(len(resumed[first_id]), len(both[first_id]))
        result = self.run_native("session", "list", "--format", "json")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            {session["id"] for session in json.loads(result.stdout)}, set(both)
        )

    def test_actual_native_direct_pty_suspend_resume_and_shutdown(self):
        process, terminal = pty.fork()
        if process == 0:
            env = dict(self.env, TERM="xterm-256color")
            os.execve(sys.executable, [sys.executable, "-B", str(RUNTIME), NATIVE], env)
        self.addCleanup(os.close, terminal)

        def cleanup():
            with contextlib.suppress(ProcessLookupError):
                os.kill(process, signal.SIGCONT)
                os.kill(process, signal.SIGTERM)
            with contextlib.suppress(ChildProcessError):
                os.waitpid(process, 0)

        self.addCleanup(cleanup)
        fcntl.ioctl(terminal, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 120, 0, 0))
        deadline = time.monotonic() + 10
        output = bytearray()
        while time.monotonic() < deadline:
            readable, _, _ = select.select([terminal], [], [], 0.1)
            if readable:
                output.extend(os.read(terminal, 65536))
                if b"Ask anything" in output or b"OpenCode" in output:
                    break
        self.assertEqual(os.getsid(process), process)
        os.write(terminal, b"\x1a")
        stopped = None
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            pid, status = os.waitpid(process, os.WNOHANG | os.WUNTRACED)
            if pid and os.WIFSTOPPED(status):
                stopped = status
                break
            readable, _, _ = select.select([terminal], [], [], 0.1)
            if readable:
                output.extend(os.read(terminal, 65536))
        self.assertIsNotNone(
            stopped, "native direct-PTY suspension did not stop its supervisor"
        )
        assert stopped is not None
        self.assertEqual(os.WSTOPSIG(stopped), signal.SIGSTOP)
        os.kill(process, signal.SIGCONT)
        time.sleep(0.15)
        os.kill(process, signal.SIGTERM)
        pid, status = os.waitpid(process, 0)
        self.assertEqual(pid, process)
        self.assertEqual(os.waitstatus_to_exitcode(status), 143)
        with contextlib.closing(sqlite3.connect(self.durable / "opencode.db")) as db:
            self.assertEqual(
                db.execute("SELECT count(*) FROM credential").fetchone(), (0,)
            )

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
