# SPEC: _spec/packages/lib/opencode-seed-progress.puml

import contextlib
import io
import os
from pathlib import Path
import select
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

from opencode_startup import SeedWait, StartupSteps


ROOT = Path(__file__).resolve().parents[2]


class Terminal(io.StringIO):
    def isatty(self):
        return True


class SeedProgressTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.status = self.root / "status"
        self.env = dict(
            PATH=os.environ["PATH"],
            HOME=str(self.root),
            TERM="xterm-256color",
            PROVEO_SEED_PROGRESS="1",
            PROVEO_SEED_STATUS_FILE=str(self.status),
            SANDBOX_VM_ID="synthetic-opencode",
            PROVEO_WORKDIR=str(self.root),
            PROVEO_SEED_DONE_MARKER=str(self.root / "done"),
            PROVEO_SEED_REFUSED=str(self.root / "refused"),
        )

    def test_pipe_reports_steps_and_heartbeat_without_terminal_escapes(self):
        stream = io.StringIO()
        self.status.write_text("credentials: staging history\n")
        with mock.patch("opencode_startup.time.monotonic", return_value=100) as clock:
            with SeedWait(self.env, stream=stream) as progress:
                progress.update()
                first = stream.getvalue()
                clock.return_value = 100.5
                progress.update()
                self.assertEqual(stream.getvalue(), first)
                self.status.write_text("workspace: preparing folders\n")
                clock.return_value = 100.7
                progress.update()
                clock.return_value = 106
                progress.update()
        lines = stream.getvalue().splitlines()
        self.assertEqual(len(lines), 3)
        self.assertIn("credentials: staging history (0s)", lines[0])
        self.assertIn("workspace: preparing folders (0s)", lines[1])
        self.assertIn("workspace: preparing folders (6s)", lines[2])
        self.assertNotIn("\x1b", stream.getvalue())
        self.assertNotIn("\r", stream.getvalue())

    def test_terminal_rotates_and_clears_before_handoff(self):
        stream = Terminal()
        with mock.patch("opencode_startup.time.monotonic", return_value=100) as clock:
            with SeedWait(self.env, stream=stream) as progress:
                progress.update()
                clock.return_value = 100.3
                progress.update()
                clock.return_value = 100.6
                progress.update()
        output = stream.getvalue()
        for frame in ("|", "/", "-"):
            self.assertIn("(0s) " + frame, output)
        self.assertTrue(output.endswith("\r\x1b[2K"))

    def test_dumb_terminal_uses_plain_output(self):
        stream = Terminal()
        with SeedWait(dict(self.env, TERM="dumb"), stream=stream) as progress:
            progress.update()
        self.assertIn("Waiting for seed", stream.getvalue())
        self.assertNotIn("\x1b", stream.getvalue())

    def test_terminal_clears_when_wait_is_interrupted(self):
        stream = Terminal()
        with self.assertRaises(SystemExit):
            with SeedWait(self.env, stream=stream) as progress:
                progress.update()
                raise SystemExit(143)
        self.assertTrue(stream.getvalue().endswith("\r\x1b[2K"))

    def test_invalid_utf8_status_uses_generic_progress_label(self):
        self.status.write_bytes(b"\xff\xfe")
        stream = io.StringIO()
        with SeedWait(self.env, stream=stream) as progress:
            progress.update()
        self.assertIn("starting: waiting for instruction seed", stream.getvalue())

    def test_disabled_progress_is_silent(self):
        stream = Terminal()
        with SeedWait({}, stream=stream) as progress:
            progress.update()
        with contextlib.redirect_stderr(stream):
            with StartupSteps({}, publish=True).step("credentials", "staging history"):
                pass
        self.assertEqual(stream.getvalue(), "")
        self.assertFalse(self.status.exists())

    def test_publisher_uses_same_label_and_reports_failure_without_secrets(self):
        stream = io.StringIO()
        with contextlib.redirect_stderr(stream):
            with self.assertRaises(ValueError):
                with StartupSteps(self.env, publish=True).step(
                    "credentials", "staging history"
                ):
                    self.assertEqual(
                        self.status.read_text(), "credentials: staging history\n"
                    )
                    raise ValueError("SYNTHETIC_SECRET")
        self.assertIn("credentials: staging history", stream.getvalue())
        self.assertIn("s, failed)", stream.getvalue())
        self.assertNotIn("SYNTHETIC_SECRET", stream.getvalue())
        self.assertEqual(list(self.root.glob(".proveo-seed-status-*")), [])

    def test_unwritable_status_does_not_abort_seed_step(self):
        env = dict(self.env, PROVEO_SEED_STATUS_FILE=str(self.root / "missing/status"))
        with contextlib.redirect_stderr(io.StringIO()):
            with StartupSteps(env, publish=True).step("credentials", "staging history"):
                pass

    def test_shell_publisher_is_opencode_only(self):
        script = 'source "$1/packages/lib/entrypoint-lib.sh"; proveo_seed_step setup "seeding configuration folders" "$2"'
        for target in ("claudecode", "opencode"):
            result = subprocess.run(
                ["bash", "-c", script, "bash", str(ROOT), target],
                env=self.env,
                capture_output=True,
                text=True,
                timeout=5,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            if target == "opencode":
                self.assertEqual(
                    self.status.read_text(), "setup: seeding configuration folders\n"
                )
                self.assertIn(self.status.read_text().strip(), result.stderr)
            else:
                self.assertFalse(self.status.exists())
                self.assertEqual(result.stderr, "")

    def test_shell_wait_reports_immediately_and_preserves_cwd_stdout(self):
        self.status.write_text("workspace: preparing shared folders\n")
        marker = self.root / "instructions"
        env = dict(
            self.env,
            PROVEO_INSTRUCTIONS_MARKER=str(marker),
            PROVEO_INSTRUCTIONS_WAIT="5",
        )
        process = subprocess.Popen(
            ["sh", str(ROOT / "packages/lib/proveo-await-seed"), "--cwd"],
            env=env,
            cwd=self.root,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        self.addCleanup(self.stop, process)
        assert process.stderr is not None
        self.assertTrue(
            select.select([process.stderr], [], [], 3)[0],
            "shell seed wait stayed blank",
        )
        first = process.stderr.readline()
        self.assertIn("workspace: preparing shared folders", first)
        self.assertNotIn("\x1b", first)
        marker.touch()
        stdout, stderr = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 0, stderr)
        self.assertEqual(stdout, str(self.root) + "\n")

    def test_shell_wait_timeout_keeps_native_launch_and_arguments(self):
        env = dict(
            self.env,
            PROVEO_INSTRUCTIONS_MARKER=str(self.root / "instructions"),
            PROVEO_INSTRUCTIONS_WAIT="1",
        )
        result = subprocess.run(
            [
                "sh",
                str(ROOT / "packages/lib/proveo-await-seed"),
                "printf",
                "%s\\n",
                "hello world",
            ],
            env=env,
            capture_output=True,
            text=True,
            timeout=5,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "hello world\n")
        self.assertIn("Waiting for seed", result.stderr)
        self.assertIn("launching anyway", result.stderr)

    def test_preflight_shows_current_step_before_refusal(self):
        self.status.write_text("credentials: checking prepared V2 history\n")
        process = subprocess.Popen(
            [
                sys.executable,
                "-B",
                str(ROOT / "packages/lib/proveo-opencode-preflight"),
                "/entrypoint.sh",
            ],
            env=self.env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        self.addCleanup(self.stop, process)
        assert process.stderr is not None
        self.assertTrue(
            select.select([process.stderr], [], [], 3)[0], "preflight stayed blank"
        )
        first = process.stderr.readline()
        self.assertIn("Waiting for seed", first)
        self.assertIn("credentials: checking prepared V2 history", first)
        (self.root / "refused").write_text("shares: missing workspace\n")
        stdout, stderr = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 78, stderr)
        self.assertEqual(stdout, "")
        self.assertIn("seed did not complete", stderr)

    def test_preflight_released_seed_is_silent_and_preserves_arguments(self):
        (self.root / "done").touch()
        wrapper = self.root / "runtime"
        wrapper.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
        wrapper.chmod(0o700)
        env = dict(self.env, PROVEO_OPENCODE_RUNTIME_WRAPPER=str(wrapper))
        result = subprocess.run(
            [
                sys.executable,
                "-B",
                str(ROOT / "packages/lib/proveo-opencode-preflight"),
                "/entrypoint.sh",
                "hello world",
            ],
            env=env,
            capture_output=True,
            text=True,
            timeout=5,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        self.assertEqual(
            result.stdout.splitlines(),
            ["--preflight-command", "/entrypoint.sh", "hello world"],
        )

    def test_preflight_signal_ends_wait_without_traceback(self):
        process = subprocess.Popen(
            [
                sys.executable,
                "-B",
                str(ROOT / "packages/lib/proveo-opencode-preflight"),
                "/entrypoint.sh",
            ],
            env=self.env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        self.addCleanup(self.stop, process)
        assert process.stderr is not None
        self.assertTrue(select.select([process.stderr], [], [], 3)[0])
        process.stderr.readline()
        process.terminate()
        stdout, stderr = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 143, stderr)
        self.assertEqual(stdout, "")
        self.assertNotIn("Traceback", stderr)

    @staticmethod
    def stop(process):
        if process.poll() is None:
            process.terminate()
        process.communicate(timeout=5)


if __name__ == "__main__":
    unittest.main()
