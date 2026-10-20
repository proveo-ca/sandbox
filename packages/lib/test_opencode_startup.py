# SPEC: _spec/packages/lib/opencode-seed-progress.puml

import os
from pathlib import Path
import select
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


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

    def test_shell_publisher_is_opencode_only(self):
        script = 'source "$1/packages/lib/entrypoint-lib.sh"; proveo_seed_step setup "seeding configuration folders" "$2"'
        for target in ("claudecode", "opencode"):
            result = subprocess.run(
                ["bash", "-c", script, "bash", str(ROOT), target],
                env=self.env,
                cwd=self.root,
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
            cwd=self.root,
            capture_output=True,
            text=True,
            timeout=5,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "hello world\n")
        self.assertIn("Waiting for seed", result.stderr)
        self.assertIn("launching anyway", result.stderr)

    @staticmethod
    def stop(process):
        if process.poll() is None:
            process.terminate()
        process.communicate(timeout=5)


if __name__ == "__main__":
    unittest.main()
