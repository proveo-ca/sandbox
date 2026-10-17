# SPEC: _spec/defs/opencode/engine-local-history.puml, _spec/defs/opencode/native-v2-integration.puml

import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock


RUNTIME = Path(__file__).resolve().with_name("proveo-opencode-runtime")

FAKE_NATIVE = """#!{python}
import json, os, sys, time
keys = ("OPENCODE_DB", "XDG_DATA_HOME", "XDG_STATE_HOME", "TMPDIR", "OPENCODE_CONFIG_DIR")
config = os.environ.get("OPENCODE_CONFIG_DIR")
files = {{}}
if config and os.path.isdir(config):
    for name in sorted(os.listdir(config)):
        with open(os.path.join(config, name)) as f:
            files[name] = f.read()
database = os.environ.get("OPENCODE_DB") or os.path.join(
    os.environ.get("XDG_DATA_HOME") or os.path.expanduser("~/.local/share"), "opencode", "opencode.db")
os.makedirs(os.path.dirname(database), exist_ok=True)
with open(database, "a") as f:
    f.write("launch\\n")
report = {{"argv": sys.argv[1:], "pid": os.getpid(), "env": {{k: os.environ.get(k) for k in keys}},
          "config": files, "database": database}}
with open(os.environ["FAKE_REPORT"], "w") as f:
    json.dump(report, f)
if os.environ.get("FAKE_SLEEP"):
    time.sleep(float(os.environ["FAKE_SLEEP"]))
sys.exit(int(os.environ.get("FAKE_EXIT", "0")))
"""


def load_runtime():
    loader = importlib.machinery.SourceFileLoader("runtime_contract", str(RUNTIME))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    assert spec is not None
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


class NativeCommandAutoTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.runtime = load_runtime()

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

    def test_diagnostic_flags_count_only_before_the_separator(self):
        for args, expected in (
            (["--version"], True),
            (["-v"], True),
            (["run", "--help"], True),
            (["-h"], True),
            (["--completions", "bash"], True),
            (["run", "--", "--help"], False),
            (["debug", "agents"], False),
            (["session", "list"], False),
            ([], False),
        ):
            with self.subTest(args=args):
                self.assertIs(self.runtime.diagnostic(args), expected)


class FakeNativeLaunchTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.home = self.root / "home"
        self.history = self.home / ".local/share/opencode"
        self.history.mkdir(parents=True)
        (self.history / "opencode.db").write_text("engine history\n")
        config = self.home / ".config/opencode"
        config.mkdir(parents=True)
        (config / "opencode.json").write_text('{"model":"fixture/model"}')
        (config / "service.json").write_text('{"env":{"OPENCODE_DB":"/elsewhere"}}')
        (config / "service-extra.json").write_text("{}")
        self.native = self.root / "bin/opencode"
        self.native.parent.mkdir()
        self.native.write_text(FAKE_NATIVE.format(python=sys.executable))
        self.native.chmod(0o755)
        self.report = self.root / "report.json"
        self.scratch = self.root / "scratch"
        self.scratch.mkdir()
        self.env = {
            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
            "HOME": str(self.home),
            "TMPDIR": str(self.scratch),
            "FAKE_REPORT": str(self.report),
        }

    def history_snapshot(self):
        return {
            str(path.relative_to(self.history)): path.read_bytes()
            for path in sorted(self.history.rglob("*"))
            if path.is_file()
        }

    def launch(self, args, **extra):
        return subprocess.run(
            [sys.executable, "-B", str(RUNTIME), str(self.native), *args],
            env=dict(self.env, **extra),
            cwd=self.root,
            capture_output=True,
            text=True,
            timeout=30,
        )

    def assert_throwaway(self, args, **extra):
        before = self.history_snapshot()
        result = self.launch(args, **extra)
        report = json.loads(self.report.read_text())
        self.assertEqual(report["argv"], args)
        env = report["env"]
        throwaway = Path(env["XDG_DATA_HOME"]).parent
        self.assertEqual(throwaway.parent, self.scratch)
        self.assertEqual(env["OPENCODE_DB"], str(throwaway / "data/opencode/opencode.db"))
        self.assertEqual(report["database"], env["OPENCODE_DB"])
        self.assertEqual(env["XDG_STATE_HOME"], str(throwaway / "state"))
        self.assertEqual(env["TMPDIR"], str(throwaway / "tmp"))
        self.assertEqual(env["OPENCODE_CONFIG_DIR"], str(throwaway / "config/opencode"))
        self.assertEqual(
            report["config"],
            {"opencode.json": '{"model":"fixture/model"}', "service.json": '{"disabled": true}'},
        )
        self.assertFalse(throwaway.exists())
        self.assertEqual(list(self.scratch.iterdir()), [])
        self.assertEqual(self.history_snapshot(), before)
        return result

    def test_inflight_runs_argv_unchanged_in_a_throwaway_database(self):
        args = ["run", "--standalone", "-m", "fixture/model", "summarize this diff"]
        result = self.assert_throwaway(args, PROVEO_GIT_SYNC_MSG_INFLIGHT="1")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_diagnostics_run_argv_unchanged_in_a_throwaway_database(self):
        for args in (["--version"], ["-v"], ["--help"], ["run", "--help"], ["-h"], ["--completions", "bash"]):
            with self.subTest(args=args):
                result = self.assert_throwaway(args)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_throwaway_returns_the_native_exit_status(self):
        result = self.assert_throwaway(["--version"], FAKE_EXIT="7")
        self.assertEqual(result.returncode, 7, result.stderr)

    def test_throwaway_forwards_termination_and_removes_its_directory(self):
        process = subprocess.Popen(
            [sys.executable, "-B", str(RUNTIME), str(self.native), "--help"],
            env=dict(self.env, FAKE_SLEEP="30"),
            cwd=self.root,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        self.addCleanup(lambda: process.poll() is None and process.kill())
        deadline = time.monotonic() + 10
        while not self.report.exists() and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertTrue(self.report.exists(), "fake native never started")
        process.send_signal(signal.SIGTERM)
        _, stderr = process.communicate(timeout=10)
        self.assertEqual(process.returncode, 128 + signal.SIGTERM, stderr)
        self.assertEqual(list(self.scratch.iterdir()), [])

    def test_normal_launch_execs_native_command_against_the_engine_database(self):
        runtime = load_runtime()
        for args in (["run", "hello"], ["session", "list"], [], ["debug", "agents"]):
            with self.subTest(args=args):
                process = subprocess.Popen(
                    [sys.executable, "-B", str(RUNTIME), str(self.native), *args],
                    env=self.env,
                    cwd=self.root,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                )
                _, stderr = process.communicate(timeout=30)
                self.assertEqual(process.returncode, 0, stderr)
                report = json.loads(self.report.read_text())
                self.assertEqual(report["pid"], process.pid)
                self.assertEqual(
                    [str(self.native), *report["argv"]],
                    runtime.native_command(str(self.native), args),
                )
                self.assertEqual(
                    report["env"],
                    {
                        "OPENCODE_DB": None,
                        "XDG_DATA_HOME": None,
                        "XDG_STATE_HOME": None,
                        "TMPDIR": str(self.scratch),
                        "OPENCODE_CONFIG_DIR": None,
                    },
                )
                self.assertEqual(report["database"], str(self.history / "opencode.db"))
        self.assertEqual(
            (self.history / "opencode.db").read_text(),
            "engine history\n" + "launch\n" * 4,
        )


if __name__ == "__main__":
    unittest.main()
