# Polyglot 22 — one file per supported language

Fixture for `e2e/toolchain_test.go`. One trigger per workspace language the seed
classifies (`_dep_lang_class` in `packages/lib/entrypoint-lib.sh`). This file is
also the **markdown** trigger, which is why 22 languages need 22 files.

Rationale lives in `_spec/tests/43-toolchain-e2e.puml`.
