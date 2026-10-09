# SPEC: _spec/defs/opencode/native-v2-integration.puml

import contextlib
import json
import os
from pathlib import Path
import re
import shlex
import sqlite3
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
RUNTIME = ROOT / "packages/lib/proveo-opencode-runtime"
NATIVE = os.environ.get("PROVEO_TEST_OPENCODE_NATIVE", "")
DRIVER = Path(__file__).with_suffix(".mjs")
EXPECTED = json.loads(Path(__file__).with_suffix(".json").read_text())
DECLARATIONS = re.findall(
    r"(?m)^\s*(?:ENV\s+)?OPENCODE_CLI_CONFIG_CONTENT=(.+?)(?:\s+\\)?$",
    (ROOT / "defs/opencode/Dockerfile").read_text(),
)
assert len(DECLARATIONS) == 1, "expected one Docker inline CLI preference declaration"
PREFERENCES = shlex.split(DECLARATIONS[0])[0]
assert json.loads(PREFERENCES) == EXPECTED, (
    "Docker preferences differ from the requested contract"
)


@unittest.skipUnless(
    NATIVE, "supply PROVEO_TEST_OPENCODE_NATIVE inside the offline image"
)
class NativePresentationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.home = self.root / "home"
        config = self.home / ".config/opencode"
        config.mkdir(parents=True)
        self.global_preferences = {
            "session": {
                "permissions": "prompt",
                "markdown": "rendered",
                "thinking": "hide",
                "grouping": "auto",
                "verbosity": "low",
                "sidebar": "hide",
            },
            "tabs": {"mode": "off", "indicators": "numbers"},
            "mini": {key: "hide" for key in EXPECTED["mini"]},
        }
        self.global_file = config / "cli.json"
        self.global_file.write_text(json.dumps(self.global_preferences))
        (config / "service.json").write_text('{"disabled":true}')
        self.env = {
            "PATH": os.environ["PATH"],
            "HOME": str(self.home),
            "PROVEO_HOME": str(self.home),
            "PROVEO_OPENCODE_CREDENTIAL_HELPER": str(
                RUNTIME.with_name("opencode-credentials.py")
            ),
            "PROVEO_OPENCODE_HOME_MANIFEST": str(
                ROOT / "defs/opencode/harness.manifest"
            ),
            "OPENCODE_DISABLE_MODELS_FETCH": "1",
            "OPENCODE_DISABLE_AUTOUPDATE": "1",
            "OPENCODE_CONFIG_PROJECT_DISABLE": "1",
            "PYTHONDONTWRITEBYTECODE": "1",
            "XDG_CACHE_HOME": "/home/agent/.cache",
            "TERM": "xterm-256color",
            "OPENCODE_CLI_CONFIG_CONTENT": PREFERENCES,
        }

    def drive(self, mode, value):
        result = subprocess.run(
            [
                "node",
                str(DRIVER),
                mode,
                value,
                sys.executable,
                str(RUNTIME),
                NATIVE,
                str(self.root),
            ],
            env=self.env,
            capture_output=True,
            text=True,
            timeout=45,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        report = json.loads(result.stdout)
        print(json.dumps(report), flush=True)
        self.assertEqual(
            json.loads(self.global_file.read_text()), self.global_preferences
        )
        return report

    def test_native_tui_global_preferences_control(self):
        self.env["OPENCODE_CLI_CONFIG_CONTENT"] = ""
        self.drive("presentation", "global")

    def test_native_tui_inline_preferences_override_global(self):
        self.drive("presentation", "inline")

    def test_native_cli_schema_validates_every_mini_preference(self):
        for key in EXPECTED["mini"]:
            with self.subTest(key=key):
                invalid = json.loads(PREFERENCES)
                invalid["mini"][key] = "invalid-contract-value"
                self.env["OPENCODE_CLI_CONFIG_CONTENT"] = json.dumps(invalid)
                self.drive("presentation", "global")

    def test_run_automatically_approves_shell_ask(self):
        report = self.drive("permission", "ask")
        self.assertEqual(report["shellOutput"], "offline shell permission fixture\n")
        self.assertEqual(
            (self.root / "shell-executed").read_text(), report["shellOutput"]
        )
        database = self.home / "opencode/share/opencode.db"
        with contextlib.closing(sqlite3.connect(database)) as db:
            messages = [
                json.loads(row[0])
                for row in db.execute("SELECT data FROM session_message")
            ]
        shell_parts = [
            part
            for message in messages
            for part in message.get("content", [])
            if part.get("type") == "tool" and part.get("name") == "shell"
        ]
        self.assertEqual(len(shell_parts), 1, messages)
        state = shell_parts[0]["state"]
        self.assertEqual(state["status"], "completed")
        self.assertEqual(state["metadata"]["exit"], 0)
        self.assertEqual(
            state["content"], [{"type": "text", "text": report["shellOutput"]}]
        )

    def test_run_auto_preserves_explicit_shell_deny(self):
        self.drive("permission", "deny")
        self.assertFalse((self.root / "shell-executed").exists())

    def test_direct_run_without_auto_rejects_shell_ask(self):
        self.drive("permission", "prompt")
        self.assertFalse((self.root / "shell-executed").exists())

    def test_native_diagnostics_remain_compatible(self):
        for args in (
            ("--version",),
            ("--help",),
            ("run", "--help"),
            ("debug", "agents"),
            ("session", "list", "--format", "json"),
        ):
            with self.subTest(args=args):
                result = subprocess.run(
                    [sys.executable, "-B", str(RUNTIME), NATIVE, *args],
                    env=self.env,
                    cwd=self.root,
                    capture_output=True,
                    text=True,
                    timeout=20,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                if args == ("--version",):
                    supplied = os.environ.get("PROVEO_TEST_OPENCODE_VERSION")
                    if supplied:
                        self.assertEqual(result.stdout.strip(), "opencode v" + supplied)
                    else:
                        self.assertRegex(
                            result.stdout.strip(), r"^opencode v2\.\d+\.\d+$"
                        )
                if args == ("run", "--help"):
                    self.assertIn("--auto", result.stdout)
                    self.assertIn("not explicitly denied", result.stdout)


if __name__ == "__main__":
    unittest.main()
