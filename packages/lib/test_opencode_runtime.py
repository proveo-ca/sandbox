# SPEC: _spec/defs/opencode/native-v2-integration.puml, _spec/_paradigms/credential-boundary.puml

import importlib.machinery
import importlib.util
import os
import unittest
from unittest import mock

from test_opencode_credentials import HELPER


RUNTIME = HELPER.with_name("proveo-opencode-runtime")


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


if __name__ == "__main__":
    unittest.main()
