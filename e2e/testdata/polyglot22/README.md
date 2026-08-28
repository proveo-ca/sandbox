# Polyglot 22 — one file per supported language

Fixture for `e2e/toolchain_test.go`. One trigger per language the shared
LSP detector knows about (`packages/lib/entrypoint-lib.sh` §8). This file is
also the **markdown** trigger, which is why 22 languages need 22 files.

Expected outcomes live in `expectedLanguages` in `toolchain_test.go`; rationale
lives in `_spec/tests/43-toolchain-e2e.puml` and
`_spec/packages/lib/language-server-provisioning.puml`.
