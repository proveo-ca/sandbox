# Go Testing Standards

The conventions all Go tests in this repo follow. Sourced from the official
[Go Wiki: Go Test Comments](https://go.dev/wiki/TestComments) and
[Go Wiki: TableDrivenTests](https://go.dev/wiki/TableDrivenTests), adjusted for
Go 1.26 (this module's toolchain). Integration coverage follows
[Coverage profiling support for integration tests](https://go.dev/doc/build-cover).

## Rules

1. **No assertion libraries.** Use plain `if got != want { t.Errorf(...) }`. Do not add
   testify/assert. *(Go Wiki: "Avoid the use of 'assert' libraries.")*
2. **Compare with `go-cmp`, not `reflect.DeepEqual`.** Use `cmp.Diff(want, got)` for structs/slices
   and print it as `t.Errorf("... mismatch (-want +got):\n%s", diff)`. Compare whole structs in one
   shot, never field-by-field.
3. **Failure messages: got before want, name the function, include the input.**
   `t.Errorf("Detect(%v) = %v, want %v", in, got, want)` — never a bare `got %v, want %v`, and never
   the table index as a stand-in for the input.
4. **Table-driven + subtests.** Cases are a slice of structs with a `name`; run each under
   `t.Run(tc.name, ...)`. On Go 1.22+ the loop variable is per-iteration — **no `tc := tc`**.
5. **`t.Error` over `t.Fatal`** for assertions, so one run reports every failure. `t.Fatal` only
   when setup failed and the test cannot continue (and inside a subtest, where it ends only that
   subtest).
6. **`t.Parallel()`** on independent tests/subtests. **Cannot** be combined with `t.Setenv` (the
   runtime panics) — pick one per test.
7. **Use the testing lifecycle helpers:** `t.TempDir()` (auto-cleaned), `t.Setenv()` (auto-restored,
   serial), `t.Cleanup()`, `t.Chdir()` (Go 1.24+). Don't hand-roll teardown.
8. **Mark helpers with `t.Helper()`** so failures point at the call site.
9. **Golden files** for large/structured output (e.g. generated config): compare against a
   `testdata/*.golden`, refreshable with a `-update` flag. Annotate the diff direction.
10. **Test error identity, not error strings.** Use `errors.Is`/`errors.As`, not substring matching.
11. **Run with `-race`** in CI; keep tests deterministic (no `Date.now()`-style nondeterminism).

## Integration & E2E

Docker-backed suites follow Testcontainers *practices* without adopting the library for
Squid/proxy (orchestration stays `internal/egress.BuildPlan`).

1. **Build tags.** Layer 3 files: `//go:build integration`. Layer 4 files: `//go:build e2e`.
   Default `go test ./...` must not compile them.
2. **Skip on missing prerequisites.** Layer 3 still requires `PROVEO_EGRESS_INTEGRATION=1`.
   Layer 4 has NO env gate — the `e2e` build tag is the opt-in — so every test `t.Skip`s
   itself when `docker`, `tmux`, the harness image, Ollama or the credential it spends is
   missing (`e2e/preconditions_test.go`) — never fail CI for absent infra. "Missing"
   includes a credential that is *present but unspendable* — refused, or out of balance:
   the agent exits at once and the boundary under test never runs, so the watcher
   (`instrument_test.go`) classifies that pane as a skip. Keep that pattern narrow enough
   to name the credential; widened to any early exit, it would let real defects pass.
3. **Wait strategies.** Poll for a condition (CA file size > 0, HTTP 200, log substring) with a
   deadline. Do not use sleep-only synchronization; bounded retry-with-check is fine.
4. **`t.Cleanup` teardown.** Register `plan.Teardown`, inject-dir wipe, and tmux kill on every
   path — including failure and signal.
5. **No testify.** Same assertion rules as unit tests; prefer `go-cmp` for structured diffs.
6. **Isolation.** Unique session IDs and `t.TempDir()` state dirs per test; no shared Docker
   network names across parallel packages.
7. **Never pin the backend off.** `PROVEO_SBX=off` is banned in this suite. One launch is
   resolved host-side and rendered twice, and the pin made every such test certify the
   rendering the host does *not* take — on a machine with `sbx` installed, the backend the
   operator actually gets was the one nothing asserted. Assert the behaviour instead of the
   rendering; where a probe truly needs the resolved shape, branch on it the way
   `assertPlanIsolation` branches on `isSbxArgv`, or skip with the backend named. A wait
   that hangs unpinned is a wait matching the wrong shape — `waitForContainerShell` matches
   the prompt's shape, not a literal `/app`, because sbx runs the shell at the workspace's
   own host path.
8. **Derive image references; never restate a tag.** `mise run build` tags a local build
   `:local` and only a publish moves `:latest`, so a hardcoded `proveo/x:latest` skips the
   suite on a machine that just built `x` — and on one that has also pulled, silently tests
   the *published* image. Read it from proveo's own plan (`harnessImageRef`) or resolve
   through `maintain.LocalTag` / `maintain.PublishTag`.
9. **Give credential tests their own `PROVEO_HOME`.** A login *file* under the proveo home
   is itself a credential, and proveo declines to inject any auth variable for that
   provider over it. Against a developer's real `~/.proveo` the credential under test
   therefore reaches the agent as nothing at all — and the run writes into their home.

```bash
# Layer 3
PROVEO_EGRESS_INTEGRATION=1 go test -tags=integration -race ./internal/egress/ -v -timeout 120s

# Layer 4
go test -tags=e2e ./e2e/ -run PromptfulE2E -v -timeout 300s
```

## Coverage

| Lane | How |
|------|-----|
| Unit + contract | `go test -race -cover -covermode=atomic ./... -args -test.gocoverdir=cov/unit` |
| Merge / report | `go tool covdata merge -i=cov/unit -o=cov/merged` then `percent` / `textfmt` |
| Binary (Stage 0b) | `go build -cover -o proveo-egress ./cmd/proveo-egress` + `GOCOVERDIR=…` in the proxy container |

Use `mise run test-go` and `mise run coverage` (see `scripts/go-test-coverage.sh`). Publish
percent/HTML as CI artifacts; **no hard % gate** until a baseline exists.

In-process `internal/egressproxy` / `internal/broker` tests count toward coverage in the unit
lane. Containerized `proveo-egress` statement coverage is optional Stage 0b
(`proveo/egress-proxy:cover`).

## Canonical shape

```go
func TestDetect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{name: "anthropic api key", env: map[string]string{"ANTHROPIC_API_KEY": "x"}, want: []string{"anthropic"}},
		{name: "none", env: nil, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Detect(lookupFrom(tc.env))
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Detect(%v) mismatch (-want +got):\n%s", tc.env, diff)
			}
		})
	}
}
```

**The full sweep is `mise run test-e2e`.** `test-go-e2e` alone is the Go agent suite with its opt-in gates OFF, so it skips the idle-survival, signal and toolchain checks and never climbs the sbx ladder; `test-images` alone is the image and consumer-CLI suites. Only `test-e2e` runs everything this repo considers e2e, and it is the run a release claim has to cite.

**sbx is the runtime under test — nothing else is a subject.** The mitmproxy egress sidecar, the `-egress` container and its `broker.env`, the review-tier consent gate, raw `docker run` replicas, and the docker+egress fallback all still exist in the product, and none of them is asserted here any more. A test whose subject is that rendering calls `skipOutsideSbx(t, subject)` first and says so; it is not deleted, so the record of what it measured survives, and it is not run, so a docker-only pass can never stand in for the runtime the operator actually gets. `mise run test-e2e` leaves the mitmproxy image suite and the docker-detached smoke out for the same reason, and Layer 3 (`test-go-integration`) stays outside the sweep.
