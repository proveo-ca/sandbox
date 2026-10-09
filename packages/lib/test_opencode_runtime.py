# SPEC: _spec/defs/opencode/native-v2-integration.puml, _spec/_paradigms/credential-boundary.puml

import contextlib
import errno
import importlib.machinery
import importlib.util
import json
import os
import pty
import select
import shlex
import shutil
from pathlib import Path
import signal
import sqlite3
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

from test_opencode_credentials import (
    FAKE_VALUE,
    HELPER,
    SCHEMA,
    credentials,
)


RUNTIME = HELPER.with_name("proveo-opencode-runtime")
CHILD = HELPER.with_name("opencode-runtime-fixture.py")


class NativeCommandAutoTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        loader = importlib.machinery.SourceFileLoader(
            "runtime_auto_contract", str(RUNTIME)
        )
        spec = importlib.util.spec_from_loader(loader.name, loader)
        assert spec is not None
        cls.runtime = importlib.util.module_from_spec(spec)
        loader.exec_module(cls.runtime)

    def test_run_auto_injection_preserves_native_arguments_and_is_evidence_independent(
        self,
    ):
        for evidence in ("", "default", "verbose"):
            for args, expected in (
                (["run", "hello"], ["run", "hello", "--auto", "--standalone"]),
                (
                    ["--log-level", "debug", "run", "--thinking", "hello"],
                    [
                        "--log-level",
                        "debug",
                        "run",
                        "--thinking",
                        "hello",
                        "--auto",
                        "--standalone",
                    ],
                ),
                (
                    ["run", "--auto", "hello"],
                    ["run", "--auto", "hello", "--standalone"],
                ),
                (
                    ["run", "--", "hello world"],
                    ["run", "--auto", "--standalone", "--", "hello world"],
                ),
                (
                    ["run", "--server=http://127.0.0.1:4096", "hello"],
                    ["run", "--server=http://127.0.0.1:4096", "hello", "--auto"],
                ),
            ):
                with self.subTest(evidence=evidence, args=args):
                    original = args.copy()
                    with mock.patch.dict(
                        os.environ, {"PROVEO_AGENT_EVIDENCE": evidence}
                    ):
                        self.assertEqual(
                            self.runtime.native_command("native", args),
                            ["native", *expected],
                        )
                    self.assertEqual(args, original)

    def test_other_native_commands_never_receive_auto(self):
        for args in (
            [],
            ["--continue"],
            ["--prompt", "run"],
            ["/workspace/run"],
            ["mini"],
            ["session", "list"],
            ["models"],
            ["stats"],
            ["auth", "list"],
            ["api"],
            ["reload"],
            ["debug", "agents"],
            ["debug", "config"],
            ["serve"],
            ["service", "status"],
            ["acp"],
            ["mcp", "list"],
            ["plugin", "list"],
            ["upgrade"],
            ["pair"],
        ):
            with self.subTest(args=args):
                self.assertNotIn("--auto", self.runtime.native_command("native", args))


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.home = self.root / "home"
        self.home.mkdir()
        self.durable = self.home / "opencode/share"
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
            PROVEO_OPENCODE_ENGINE_ID="fixture-engine",
            PROVEO_SEED_REFUSED=str(self.root / "seed-refused"),
            PROVEO_OPENCODE_HOME_MANIFEST=str(
                HELPER.parents[2] / "defs/opencode/harness.manifest"
            ),
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

    def test_native_preferences_history_stashes_and_pins_round_trip_in_order(self):
        result = self.run_cli("preferences")
        self.assertEqual(result.returncode, 0, result.stderr)
        saved = self.durable / credentials.STATE_NAME
        expected = {
            name: (saved / name).read_bytes()
            for name in (
                "prompt-history.jsonl",
                "prompt-stash.jsonl",
                "model.json",
                "session.json",
            )
        }
        self.assertFalse((saved / "service.json").exists())
        proc = self.start("hold")
        ready = self.wait_event("ready", count=2)
        root = Path(ready["database"]).parents[2]
        for name, content in expected.items():
            self.assertEqual((root / "state/opencode" / name).read_bytes(), content)
        proc.terminate()
        proc.communicate(timeout=10)
        for name, content in expected.items():
            self.assertEqual((saved / name).read_bytes(), content)

    def test_legacy_docker_history_migrates_to_the_same_sbx_store_and_lease(self):
        shutil.rmtree(self.durable)
        legacy = self.home / ".local/share/opencode"
        self.fixture(legacy)
        first = self.start("hold")
        self.wait_event("ready")
        self.assert_clean(legacy, messages=1)
        guest = self.root / "guest"
        guest.mkdir()
        sbx_env = dict(
            self.env,
            HOME=str(guest),
            PROVEO_HOME=str(guest),
            PROVEO_STATE_HOME=str(self.home),
            PROVEO_CONFIG_DIRS="opencode/share|.local/share/opencode|auth.json",
        )
        rejected = self.run_cli("append", env=sbx_env)
        self.assertEqual(rejected.returncode, 75, rejected.stderr)
        first.terminate()
        first.communicate(timeout=10)
        result = self.run_cli("append", env=sbx_env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_clean(messages=3)
        result = self.run_cli("append")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_clean(messages=4)

    def test_divergent_legacy_and_canonical_histories_are_not_overwritten(self):
        legacy = self.home / ".local/share/opencode"
        self.fixture(legacy)
        with contextlib.closing(sqlite3.connect(legacy / "opencode.db")) as db:
            db.execute(
                "INSERT INTO session_message VALUES ('legacy-only', 'session-fixture', 2, 'keep legacy history')"
            )
            db.commit()
        before = (self.durable / "opencode.db").read_bytes()
        result = self.run_cli("append")
        self.assertEqual(result.returncode, 78, result.stderr)
        self.assertEqual((self.durable / "opencode.db").read_bytes(), before)
        self.assertEqual(self.events(), [])
        with contextlib.closing(sqlite3.connect(legacy / "opencode.db")) as db:
            self.assertEqual(
                db.execute("SELECT count(*) FROM session_message").fetchone(), (2,)
            )

    def test_orphan_disk_full_preserves_source_record_and_then_recovers(self):
        first = self.start("hold")
        ready = self.wait_event("ready")
        first.kill()
        os.kill(ready["pid"], signal.SIGTERM)
        first.communicate(timeout=10)
        loader = importlib.machinery.SourceFileLoader("runtime_fixture", str(RUNTIME))
        spec = importlib.util.spec_from_loader(loader.name, loader)
        assert spec is not None
        runtime = importlib.util.module_from_spec(spec)
        loader.exec_module(runtime)
        engine = mock.patch.object(
            runtime, "engine_identity", return_value="fixture-engine"
        )
        engine.start()
        self.addCleanup(engine.stop)
        fd = os.open(self.durable / credentials.LIFECYCLE_LOCK_NAME, os.O_RDWR)
        self.addCleanup(os.close, fd)
        credentials.fcntl.flock(
            fd, credentials.fcntl.LOCK_EX | credentials.fcntl.LOCK_NB
        )
        record = (self.durable / credentials.LIFECYCLE_LOCK_NAME).read_bytes()
        original = credentials.snapshot
        for phase in ("create", "backup", "publication"):
            with self.subTest(phase=phase):

                def snapshot(source, destination, *args, **kwargs):
                    destination = Path(destination)
                    if (
                        phase == "backup"
                        and destination.parent.name == credentials.RECOVERY_NAME
                        or phase == "publication"
                        and destination == self.durable
                    ):
                        raise OSError(errno.ENOSPC, "injected disk full")
                    return original(source, destination, *args, **kwargs)

                with mock.patch.object(credentials, "snapshot", side_effect=snapshot):
                    if phase == "create":
                        with mock.patch.object(
                            runtime.tempfile,
                            "mkdtemp",
                            side_effect=OSError(errno.ENOSPC, "injected disk full"),
                        ):
                            with self.assertRaises(OSError):
                                runtime.recover_orphan(fd, credentials, self.durable)
                    else:
                        with self.assertRaises(OSError):
                            runtime.recover_orphan(fd, credentials, self.durable)
                self.assertTrue(Path(ready["database"]).exists())
                self.assertEqual(
                    (self.durable / credentials.LIFECYCLE_LOCK_NAME).read_bytes(),
                    record,
                )
                with contextlib.closing(sqlite3.connect(ready["database"])) as db:
                    self.assertEqual(
                        db.execute("SELECT count(*) FROM session_message").fetchone(),
                        (2,),
                    )
        runtime.recover_orphan(fd, credentials, self.durable)
        self.assert_clean(messages=2)
        self.assertFalse(Path(ready["database"]).exists())

    def test_controlling_pty_suspend_returns_shell_job_and_foreground_resumes(self):
        shell, terminal = pty.fork()
        if shell == 0:
            os.execve("/bin/bash", ["bash", "--noprofile", "--norc", "-i"], self.env)
        self.addCleanup(os.close, terminal)

        def cleanup():
            with contextlib.suppress(ProcessLookupError):
                os.kill(shell, signal.SIGKILL)
            with contextlib.suppress(ChildProcessError):
                os.waitpid(shell, 0)

        self.addCleanup(cleanup)
        captured = bytearray()

        def until(marker):
            deadline = time.monotonic() + 10
            while marker not in captured and time.monotonic() < deadline:
                readable, _, _ = select.select([terminal], [], [], 0.1)
                if readable:
                    captured.extend(os.read(terminal, 65536))
            self.assertIn(marker, bytes(captured))

        os.write(terminal, b"PS1='PTY_READY> '\n")
        until(b"PTY_READY> ")
        captured.clear()
        os.write(terminal, (shlex.join(self.command("hold")) + "\n").encode())
        ready = self.wait_event("ready")
        os.write(terminal, b"\x1a")
        until(b"Stopped")
        until(b"PTY_READY> ")
        self.assertEqual(self.run_cli("append").returncode, 75)
        captured.clear()
        os.write(terminal, b"fg\n")
        time.sleep(0.2)
        self.assertEqual(os.tcgetpgrp(terminal), os.getpgid(ready["pid"]))
        while select.select([terminal], [], [], 0)[0]:
            os.read(terminal, 65536)
        captured.clear()
        os.write(terminal, b"\x03")
        until(b"PTY_READY> ")
        self.assert_clean(messages=2)

    def test_direct_controlling_pty_session_leader_suspends_and_resumes(self):
        process, terminal = pty.fork()
        if process == 0:
            os.execve(sys.executable, self.command("hold"), self.env)
        self.addCleanup(os.close, terminal)

        def cleanup():
            with contextlib.suppress(ProcessLookupError):
                os.kill(process, signal.SIGCONT)
                os.kill(process, signal.SIGTERM)
            with contextlib.suppress(ChildProcessError):
                os.waitpid(process, 0)

        self.addCleanup(cleanup)
        ready = self.wait_event("ready")
        self.assertEqual(os.getsid(process), process)
        os.write(terminal, b"\x1a")
        deadline = time.monotonic() + 10
        status = None
        while time.monotonic() < deadline:
            pid, value = os.waitpid(process, os.WNOHANG | os.WUNTRACED)
            if pid and os.WIFSTOPPED(value):
                status = value
                break
            time.sleep(0.025)
        self.assertIsNotNone(status, "direct-PTY supervisor did not stop")
        assert status is not None
        self.assertEqual(os.WSTOPSIG(status), signal.SIGSTOP)
        self.assertEqual(self.run_cli("append").returncode, 75)
        os.kill(process, signal.SIGCONT)
        time.sleep(0.15)
        self.assertEqual(os.tcgetpgrp(terminal), os.getpgid(ready["pid"]))
        os.write(terminal, b"\x03")
        pid, status = os.waitpid(process, 0)
        self.assertEqual(pid, process)
        self.assertEqual(os.waitstatus_to_exitcode(status), 130)
        self.assert_clean(messages=2)

    def test_final_checkpoint_disk_full_retains_latest_private_state_until_retry(self):
        proxy = self.root / "failing-helper.py"
        proxy.write_text(
            "import runpy, os, errno\nfrom pathlib import Path\n"
            f'globals().update({{k:v for k,v in runpy.run_path({str(HELPER)!r}).items() if not k.startswith("__")}})\n'
            "_snapshot=snapshot\ndef snapshot(source, destination, *args, **kwargs):\n"
            '    if os.environ.get("FAIL_FINAL_CHECKPOINT") == "1" and (Path(source)/"latest-native-write").exists():\n'
            '        raise OSError(errno.ENOSPC,"injected final snapshot disk full")\n'
            "    return _snapshot(source,destination,*args,**kwargs)\n"
        )
        env = dict(
            self.env,
            PROVEO_OPENCODE_CREDENTIAL_HELPER=str(proxy),
            FAIL_FINAL_CHECKPOINT="1",
        )
        result = self.run_cli("final-disk-full", env=env)
        self.assertEqual(result.returncode, 74, result.stderr)
        ready = self.wait_event("ready")
        database = Path(ready["database"])
        root = database.parents[2]
        self.addCleanup(shutil.rmtree, root, True)
        self.assertTrue(database.exists())
        record = json.loads(
            (self.durable / credentials.LIFECYCLE_LOCK_NAME).read_text()
        )
        self.assertEqual(record["root"], str(root))
        with contextlib.closing(sqlite3.connect(database)) as db:
            self.assertEqual(
                db.execute("SELECT count(*) FROM session_message").fetchone(), (2,)
            )
            self.assertEqual(
                db.execute("SELECT count(*) FROM credential").fetchone(), (1,)
            )
        self.assertIn("not credential-free", result.stderr)
        self.assert_clean(messages=1)
        result = self.run_cli("append")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_clean(messages=3)
        self.assertFalse(database.exists())

    def test_preflight_sanitizes_host_stores_before_provisioning_commands(self):
        legacy = self.home / ".local/share/opencode"
        self.fixture(legacy)
        provision = self.root / "provision"
        provision.write_text(
            "#!/usr/bin/env python3\nimport os, sqlite3, sys\nfrom pathlib import Path\n"
            f"for p in ({str(self.durable)!r},{str(legacy)!r}):\n"
            '    db=sqlite3.connect(Path(p)/"opencode.db")\n'
            '    assert db.execute("SELECT count(*) FROM credential").fetchone()==(0,)\n'
            "    db.close()\n"
            f'Path({str(self.root / "provisioned")!r}).write_text("ran after cleanup")\n'
            f'os.execve(sys.executable,[sys.executable,"-B",{str(RUNTIME)!r},{str(self.native)!r},"append"],dict(os.environ))\n'
        )
        provision.chmod(0o700)
        result = subprocess.run(
            [sys.executable, "-B", str(RUNTIME), "--preflight-command", str(provision)],
            env=self.env,
            capture_output=True,
            text=True,
            timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.root / "provisioned").exists())
        self.assert_clean(messages=2)

    def test_foreign_engine_cannot_clear_a_missing_pending_private_directory(self):
        lock = self.durable / credentials.LIFECYCLE_LOCK_NAME
        pending = {
            "root": "/tmp/proveo-opencode-runtime-retained-elsewhere",
            "database": "opencode.db",
            "engine": "old-container",
            "container": "retained-fixture",
        }
        lock.write_text(json.dumps(pending))
        before = lock.read_bytes()
        result = self.run_cli(
            "append", env=dict(self.env, PROVEO_OPENCODE_ENGINE_ID="new-container")
        )
        self.assertEqual(result.returncode, 75, result.stderr)
        self.assertEqual(lock.read_bytes(), before)
        self.assertIn("docker start -ai retained-fixture", result.stderr)
        self.assertEqual(self.events(), [])
        self.assertTrue((self.root / "seed-refused").exists())

    def test_custom_database_selection_change_recovers_and_publishes_without_auth(self):
        with contextlib.closing(
            sqlite3.connect(self.durable / "opencode.db")
        ) as source:
            with contextlib.closing(
                sqlite3.connect(self.durable / "custom-database")
            ) as destination:
                source.backup(destination)
        env = dict(self.env, OPENCODE_DB="custom-database")
        first = self.start("hold", env=env)
        ready = self.wait_event("ready")
        self.assertEqual(Path(ready["database"]).name, "custom-database")
        first.kill()
        os.kill(ready["pid"], signal.SIGTERM)
        first.communicate(timeout=10)
        result = self.run_cli("append")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_clean(messages=2)
        with contextlib.closing(
            sqlite3.connect(self.durable / "custom-database")
        ) as db:
            self.assertEqual(
                db.execute("SELECT count(*) FROM session_message").fetchone(), (2,)
            )
            self.assertEqual(
                db.execute("SELECT count(*) FROM credential").fetchone(), (0,)
            )
            self.assertEqual(db.execute("PRAGMA quick_check").fetchone(), ("ok",))
        self.assertFalse(Path(ready["database"]).exists())

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
        self.assertEqual(result.returncode, 78)
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
        self.assertTrue(Path(ready["database"]).parent.exists())
        self.addCleanup(shutil.rmtree, Path(ready["database"]).parents[2], True)
        self.assertIn("previous credential-free checkpoint", result.stderr)

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
        self.assertTrue(Path(ready["database"]).parent.exists())
        self.addCleanup(shutil.rmtree, Path(ready["database"]).parents[2], True)

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
