# Q&A

**Why isolate agents?** Agents execute generated commands against code, credentials, networks, tools, and mutable state. Isolation limits a mistaken or adversarial action to a disposable guest, makes network and credential grants explicit, and keeps the operator's checkout out of the default write path. It reduces blast radius; it does not make an allowed credential, host, mount, browser bridge, or daemon harmless.

**Why adapt sbx Kits?** proveo began with plain Docker plus proveo-managed egress proxies, then added a privileged Docker-in-Docker sidecar so agents could use a daemon. That accumulated two responsibilities proveo should not own: building the security boundary and handing an agent a privileged, reachable daemon. sbx supplies the VM boundary, policy, credential proxy, lifecycle, and optional in-sandbox daemon. proveo's Kit should therefore adapt each published harness image into sbx's contract: select the image, declare a complete `kind: sandbox` agent when sbx has no built-in, seed it, and express only the required network, credential, environment, and resource capabilities. Docker remains relevant as the image-to-sbx-template adapter; it is no longer the agent's containment thesis.

**Why use `sbx --clone`?** It keeps the main checkout off the writable workspace path and gives the agent a Git-materialized filesystem inside the sandbox. The input is the committed Git state reachable from the main worktree; staged, unstaged, untracked, and ignored host files do not cross. It does not clone a linked worktree's private state, and proveo may disable the default for non-repositories, linked worktrees, or an unforced monorepo sub-scope. At teardown proveo snapshots changes left in the clone, then fetches sandbox branch output into the host repository under `refs/proveo/<sid>/*`. That preserves commits for inspection; it does not merge them, update the checked-out branch, or guarantee that non-Git state was returned.

## Adversarial review

### `apps/cli/public/cli/install.sh`

- Release identity is split. `apps/cli/public/cli/latest.json` and `apps/cli/public/cli/checksums.txt` currently contain different hashes for every platform. The installer reads `latest.json` only to display a version, then downloads an unversioned binary and trusts the separately fetched `checksums.txt`. A mixed CDN publication can therefore print version A while installing checksum-valid binary B.
- Failure is not transactional. `uninstall.sh` is downloaded directly to its final path before binary verification, and the final binary is installed with `cp` followed by `chmod`. Replacement is non-atomic, can leave a partial/old mode state, and follows an existing destination symlink.
- A no-TTY install invokes `proveo init --yes`. That converts a binary installer into an unattended sbx bootstrap and host-readiness workflow; `PROVEO_SKIP_INIT` is an escape hatch, but the default broadens installer scope and side effects.
- `check_docker` says proveo “runs published Docker images” and that Docker is needed “before running containers.” Under the current thesis Docker's role is narrower and more precise: it supplies/builds the image that sbx converts to a template. The wording makes Docker sound like the runtime boundary and obscures the sbx adapter.

### Current paradigm diagrams

- `_spec/_paradigms/capability-ladder.puml`: the diagnostic method is sound, but its special shell-agent rung is stale as the default architecture. Non-built-in harnesses now default to complete `kind: sandbox` Kits; borrowing `shell` is the `PROVEO_SBX_AGENT_KIT=0` opt-out.
- `_spec/_paradigms/credential-boundary.puml`: valuable Docker MITM history, but too universal. sbx normally uses its own credential proxy and Kit declarations, not proveo's MITM route table. Its “current run” posture also implies brokering where `Forwards` can still deliberately pass a credential.
- `_spec/_paradigms/git-identity.puml`: correctly rejects mounting host gitconfig, but presents Docker `run -e` and container config-env rendering as universal. sbx has a distinct Kit/environment path and clone output refs.
- `_spec/_paradigms/harness-paradigms.puml`: useful harness intent, but saying dangerous posture is made acceptable by “the egress boundary” overstates protection when the host baseline is allow-all. An allow-all baseline is isolation from the host, not meaningful destination restriction.
- `_spec/_paradigms/retire-dind.puml`: accurately records retirement, but its account of non-built-in harnesses using the shell agent is stale. Complete custom sandbox Kits are now default, with shell only as fallback/opt-out.
- `_spec/_paradigms/runtime-user-boundary.puml`: its arbitrary-UID and home-symlink failures are real Docker renderings, not a universal runtime contract. sbx template creation and its agent user/home ownership need separate claims and evidence.
- `_spec/_paradigms/workspace-boundary.puml`: correctly identifies the shared writable proveo-home mount as cross-run transcript confidentiality and integrity exposure. It overstates clone as “tracked files only”: the accurate boundary is committed main-worktree state only, excluding dirty and staged changes as well as untracked/ignored files.

## `internal/` value map

Count: **219 files**, including Go source, tests, and six testdata goldens.

Value measures product leverage, not effort: **5** is core sbx-Kit, isolation, credential, workspace, release, or posture trust; **4** is major runtime correctness; **3** is supporting behavior and operator UX; **2** is peripheral or legacy Docker+egress behavior; **1** is mechanical or documentation hygiene. Complexity measures reasoning, state, and integration surface, not LOC: **1** is declarative/snapshot-like and **5** is cross-system orchestration. Tests inherit the value of the invariant they protect, but a narrow assertion can have lower complexity than its implementation. Legacy Docker+egress code is intentionally lower thesis value even where its proxy, PTY, or lifecycle logic remains complex.

| File | Value | Complexity | Judgment |
|---|---:|---:|---|
| `internal/sbx/version.go` | 5 | 1 | Compares the required sbx version; hand-rolled version rules may diverge from upstream semantics. |
| `internal/backend/sandbox/domain_form_test.go` | 5 | 2 | Pins domain normalization into Kit form; examples may miss new sbx grammar. |
| `internal/backend/sandbox/launch_config_test.go` | 5 | 2 | Pins launch environment composition; unit fixtures cannot prove live template behavior. |
| `internal/backend/sandbox/memory_evidence_test.go` | 5 | 2 | Guards resource and evidence propagation; assertions can lag sbx defaults. |
| `internal/backend/sandbox/provider_gate_test.go` | 5 | 2 | Prevents undeclared provider grants; registry drift can create untested routes. |
| `internal/credentials/auth_choice_test.go` | 5 | 2 | Pins credential-choice precedence; synthetic stores miss vendor login mutations. |
| `internal/credentials/broker_binding_test.go` | 5 | 2 | Guards secret-file binding and routes; does not exercise a real proxy. |
| `internal/credentials/storename.go` | 5 | 2 | Maps credentials to sbx storage classes; vendor additions can silently misclassify. |
| `internal/credentials/storename_test.go` | 5 | 2 | Pins storage classification; table coverage is only as current as the registry. |
| `internal/posture/posture_test.go` | 5 | 2 | Pins trust-facing posture text; strings can remain green while behavior drifts. |
| `internal/provider/bareid_test.go` | 5 | 2 | Guards bare model/provider ID parsing; aliases may create ambiguous future IDs. |
| `internal/provider/consistency_test.go` | 5 | 2 | Cross-checks provider registries; external vendor drift is outside the fixture. |
| `internal/provider/models_test.go` | 5 | 2 | Pins model resolution; published model catalogs change faster than releases. |
| `internal/run/auth_row_test.go` | 5 | 2 | Guards credential-choice UI state; row correctness does not prove delivery. |
| `internal/run/choices_test.go` | 5 | 2 | Pins normalized run choices; cached-state migrations remain a risk. |
| `internal/run/clone_test.go` | 5 | 2 | Pins clone eligibility decisions; mocks cannot prove sbx Git transport. |
| `internal/run/outcome_test.go` | 5 | 2 | Guards run outcome classification; novel teardown failures may be mislabeled. |
| `internal/run/params_test.go` | 5 | 2 | Pins flag and environment precedence; combinatorial option interactions remain. |
| `internal/run/topology_test.go` | 5 | 2 | Guards network topology selection; topology labels can outlive implementation. |
| `internal/sbx/baseline_test.go` | 5 | 2 | Pins baseline-policy interpretation; host policy can differ from fixtures. |
| `internal/sbx/domain_test.go` | 5 | 2 | Guards domain canonicalization; unusual ports and IDNs remain sharp edges. |
| `internal/sbx/running_names_test.go` | 5 | 2 | Parses proveo-owned sandbox names; upstream listing format can drift. |
| `internal/sbx/secret_test.go` | 5 | 2 | Pins built-in and custom secret commands; argv exposure remains for custom values. |
| `internal/sbx/shell_launch_test.go` | 5 | 2 | Guards flag-leading shell fallback; it does not validate complete custom Kits. |
| `internal/workspace/mount_canonical_test.go` | 5 | 2 | Pins one canonical mount root; platform path semantics remain under-sampled. |
| `internal/broker/broker_test.go` | 5 | 3 | Exercises route injection and stripping; HTTP tests cannot prove every TLS client. |
| `internal/cdn/cdn_test.go` | 5 | 3 | Guards release metadata and checksum handling; publication atomicity is not proved. |
| `internal/credentials/hints.go` | 5 | 3 | Produces safe login guidance; hints can overstate credential usability. |
| `internal/credentials/hints_test.go` | 5 | 3 | Pins auth guidance; text assertions cannot verify remediation succeeds. |
| `internal/posture/posture.go` | 5 | 3 | States the run's trust posture; derived prose can claim broker despite forwarding. |
| `internal/proveohome/configset.go` | 5 | 3 | Computes persisted config subsets; omission can silently lose or expose state. |
| `internal/proveohome/configset_test.go` | 5 | 3 | Pins scrub/save symmetry; new config paths can escape the set. |
| `internal/proveohome/proveohome_test.go` | 5 | 3 | Guards home preparation and mounts; it underrepresents cross-run hostile writes. |
| `internal/provider/models.go` | 5 | 3 | Resolves portable model IDs and roles; stale catalog data breaks portability. |
| `internal/provider/provider_test.go` | 5 | 3 | Exercises detection and host mapping; ambient multi-provider hosts are harder. |
| `internal/provider/roles_test.go` | 5 | 3 | Guards role-to-provider requirements; runtime harness interpretation may differ. |
| `internal/run/choices.go` | 5 | 3 | Builds security-sensitive choice rows; UI defaults can become accidental consent. |
| `internal/run/helpers.go` | 5 | 3 | Supplies run orchestration helpers; shared helpers hide cross-phase coupling. |
| `internal/run/params.go` | 5 | 3 | Defines the run's control surface; many booleans permit invalid combinations. |
| `internal/run/spec.go` | 5 | 3 | Carries resolved launch state; mutable aggregate state obscures ownership. |
| `internal/sbx/agents.go` | 5 | 3 | Selects built-in, custom-Kit, or shell launch; fallback can mask a broken image entrypoint. |
| `internal/sbx/domain.go` | 5 | 3 | Converts hosts to sbx policy domains; lossy normalization can overgrant. |
| `internal/sbx/exec.go` | 5 | 3 | Centralizes sbx process execution seams; global command hooks can hide concurrency assumptions. |
| `internal/sbx/kit.go` | 5 | 3 | Models and writes sbx Kit YAML; schema drift can invalidate trusted output. |
| `internal/sbx/limits.go` | 5 | 3 | Resolves sandbox resource limits; weak defaults can enable host exhaustion. |
| `internal/sbx/policy.go` | 5 | 3 | Builds baseline and domain policy commands; host allow-all weakens stated egress. |
| `internal/sbx/secret.go` | 5 | 3 | Stores built-in/custom sbx secrets; custom setup still places values on argv. |
| `internal/sbx/template.go` | 5 | 3 | Tracks image-to-template receipts; cache identity can go stale across builders. |
| `internal/secretref/secretref.go` | 5 | 3 | Resolves file/keychain secret references; fallback paths can broaden custody. |
| `internal/secretref/secretref_test.go` | 5 | 3 | Pins reference safety and no-shell behavior; platform keychains remain lightly exercised. |
| `internal/backend/sandbox/own_agent_test.go` | 5 | 4 | Guards complete custom sandbox Kits; tests still rely on inferred sbx semantics. |
| `internal/backend/sandbox/sandbox_stale_test.go` | 5 | 4 | Exercises stale sandbox recovery; races with real daemon state remain possible. |
| `internal/backend/sandbox/sandbox_test.go` | 5 | 4 | Covers adapter assembly and teardown; command capture is not end-to-end isolation proof. |
| `internal/broker/broker.go` | 5 | 4 | Injects secrets only on provider routes and strips off-route; body re-encoding bypasses header controls. |
| `internal/cdn/cdn.go` | 5 | 4 | Updates binaries from release metadata; split channel artifacts undermine identity. |
| `internal/credentials/credentials_test.go` | 5 | 4 | Exercises lookup, suppression, and custody; host-specific stores still create gaps. |
| `internal/credentials/keychain.go` | 5 | 4 | Reads supported host credential stores; OS tooling and formats are unstable dependencies. |
| `internal/credentials/keychain_test.go` | 5 | 4 | Pins Keychain interpretation and targeting; mocks cannot reproduce machine-global state. |
| `internal/proveohome/proveohome.go` | 5 | 4 | Prepares persistent harness homes; mounting the whole root rw exposes every transcript. |
| `internal/provider/provider.go` | 5 | 4 | Owns provider detection, keys, and hosts; centralized tables are high-blast-radius drift points. |
| `internal/provider/roles.go` | 5 | 4 | Resolves model roles and missing keys; aliases can make diagnostics confidently wrong. |
| `internal/run/run_test.go` | 5 | 4 | Pins broad orchestration choices and warnings; large fixture coupling can bless stale architecture. |
| `internal/run/topology.go` | 5 | 4 | Selects backend and network shape; legacy and sbx branches invite semantic divergence. |
| `internal/sbx/baseline_live_test.go` | 5 | 4 | Measures a live sbx policy baseline; environment gating can turn regressions into skips. |
| `internal/sbx/clone_test.go` | 5 | 4 | Exercises real Git bundle/fetch scripts; does not prove daemon transport or teardown reliability. |
| `internal/sbx/install.go` | 5 | 4 | Plans unprivileged sbx installation and readiness checks; pinned assets and host probes age quickly. |
| `internal/sbx/install_test.go` | 5 | 4 | Guards platform plans, provenance, and diagnostics; only some assets have verifiable digests. |
| `internal/sbx/sbx.go` | 5 | 4 | Centralizes sbx versions, agents, and process hooks; global mutable hooks complicate concurrency. |
| `internal/sbx/sbx_test.go` | 5 | 4 | Broadly pins CLI adaptation; captured argv cannot guarantee upstream behavior. |
| `internal/workspace/mount_test.go` | 5 | 4 | Exercises mounts, links, env masking, and worktrees; filesystem races and exotic links remain. |
| `internal/backend/sandbox/sandbox.go` | 5 | 5 | Renders and runs the full sbx Kit lifecycle; one large adapter couples policy, secrets, mounts, and teardown. |
| `internal/credentials/credentials.go` | 5 | 5 | Orchestrates credential discovery and precedence; ambient, stored, and mounted sources can conflict. |
| `internal/run/run.go` | 5 | 5 | Orchestrates the product's security decisions and lifecycle; monolithic phase coupling makes claims hard to isolate. |
| `internal/sbx/run.go` | 5 | 5 | Builds sbx lifecycle, clone, bundle, browser, and auth commands; handwritten shell and upstream CLI drift are critical risks. |
| `internal/workspace/mount.go` | 5 | 5 | Defines what host filesystem state crosses; symlink, worktree, and secret masking complexity invites boundary holes. |
| `internal/contract/agent_pin_test.go` | 4 | 1 | Pins agent versions in images; exact pins can become stale security liabilities. |
| `internal/contract/browser_layer_test.go` | 4 | 1 | Guards browser image composition; static Dockerfile checks do not launch Chromium. |
| `internal/contract/bun_toolchain_test.go` | 4 | 1 | Pins Bun provisioning contracts; textual checks miss runtime architecture faults. |
| `internal/contract/cecli_providers_test.go` | 4 | 1 | Guards cecli provider declarations; upstream provider support can drift first. |
| `internal/contract/defaults_test.go` | 4 | 1 | Checks shipped default file modes; it cannot prove runtime enforcement. |
| `internal/contract/go_provisioning_test.go` | 4 | 1 | Guards Go toolchain provisioning; static assertions miss network/install failures. |
| `internal/contract/image_size_test.go` | 4 | 1 | Prevents redundant browser payloads; size proxies do not measure startup cost. |
| `internal/contract/mcp_class_test.go` | 4 | 1 | Pins MCP package classification; naming heuristics may misclassify new servers. |
| `internal/contract/seed_command_shipped_test.go` | 4 | 1 | Ensures the seed command ships; presence does not prove successful seeding. |
| `internal/contract/subagent_roster_test.go` | 4 | 1 | Pins shipped subagent roles; static roster checks cannot enforce permissions. |
| `internal/contract/tool_home_test.go` | 4 | 1 | Guards tool-home placement; paths can be correct but unwritable at runtime. |
| `internal/contract/tool_sync_test.go` | 4 | 1 | Pins tool state sync declarations; new state can remain unsynchronized. |
| `internal/contract/toolchain_test.go` | 4 | 1 | Guards shipped toolchain declarations; static inventory does not prove tools execute. |
| `internal/agentsettings/agentsettings_test.go` | 4 | 2 | Guards settings discovery and merge rules; malformed vendor files remain risky. |
| `internal/backend/backend_test.go` | 4 | 2 | Pins backend interface behavior; mocks can conceal lifecycle asymmetry. |
| `internal/clean/sandbox_liveness_test.go` | 4 | 2 | Prevents deletion of live sandbox state; process-name evidence can be stale. |
| `internal/contract/agent_launch_guard_test.go` | 4 | 2 | Ensures entrypoints use the launch disambiguator; static parsing can miss indirect bypasses. |
| `internal/contract/agents_shared_test.go` | 4 | 2 | Guards shared agent definitions; equality can preserve a shared mistake. |
| `internal/contract/base_template_test.go` | 4 | 2 | Pins daemon-capable base templates; labels do not prove daemon isolation. |
| `internal/contract/claude_lsp_plugins_test.go` | 4 | 2 | Cross-checks Claude LSP plugin lists; textual parity does not prove activation. |
| `internal/contract/config_files_test.go` | 4 | 2 | Guards declared config files; omissions are invisible until a new file appears. |
| `internal/contract/config_set_test.go` | 4 | 2 | Pins scrub and sync config sets; duplicated declarations can drift together. |
| `internal/contract/config_sync_test.go` | 4 | 2 | Guards config persistence wiring; end-to-end save/restore is not exercised. |
| `internal/contract/contract_test.go` | 4 | 2 | Enforces manifest/image invariants; broad static rules can encode yesterday's architecture. |
| `internal/contract/cwd_guard_test.go` | 4 | 2 | Pins workspace escape detection; shell path edge cases remain. |
| `internal/contract/deps_install_test.go` | 4 | 2 | Guards dependency rebuild safety; fixture installers miss ecosystem side effects. |
| `internal/contract/deps_table_test.go` | 4 | 2 | Cross-checks dependency-tree declarations; synchronized tables may share wrong assumptions. |
| `internal/contract/dockerfile_correctness_test.go` | 4 | 2 | Blocks known Dockerfile injection and ownership faults; regex cannot model shell execution. |
| `internal/contract/home_symlink_test.go` | 4 | 2 | Guards real `/home/agent` ownership; it treats Docker rendering as the main oracle. |
| `internal/contract/image_resolution_test.go` | 4 | 2 | Cross-checks image resolvers; mirrored logic can agree on a stale tag. |
| `internal/contract/import_boundary_test.go` | 4 | 2 | Enforces package layering; allowed imports can still create runtime coupling. |
| `internal/contract/lsp_config_parity_test.go` | 4 | 2 | Pins LSP config parity; matching files do not prove server compatibility. |
| `internal/contract/model_bridging_retired_test.go` | 4 | 2 | Prevents retired model rewriting from returning; static scans may miss equivalent behavior. |
| `internal/contract/seed_abort_test.go` | 4 | 2 | Guards nonfatal seed failures; shell probes cover only known abort shapes. |
| `internal/agentsettings/agentsettings.go` | 4 | 3 | Discovers and merges harness settings; vendor schema changes can corrupt intent. |
| `internal/backend/backend.go` | 4 | 3 | Defines backend lifecycle abstractions; a thin interface can hide unequal guarantees. |
| `internal/engine/engine_test.go` | 4 | 3 | Pins command execution sequencing; fakes omit OS process edge cases. |
| `internal/entrypoint/entrypoint_test.go` | 4 | 3 | Exercises seed and launch decisions; test shells differ from image entrypoints. |
| `internal/entrypoint/parity_test.go` | 4 | 3 | Cross-checks Go and shell entrypoint behavior; parity can preserve a mutual defect. |
| `internal/gitidentity/gitidentity_test.go` | 4 | 3 | Guards identity precedence without host config mounts; signing and worktree cases remain. |
| `internal/manifest/manifest_test.go` | 4 | 3 | Pins schema validation and capabilities; permissive defaults can still overgrant. |
| `internal/runner/pids_test.go` | 4 | 3 | Guards process ownership tracking; PID reuse and abrupt host death remain. |
| `internal/runner/runner_test.go` | 4 | 3 | Exercises container argv and cleanup; it cannot prove kernel confinement. |
| `internal/verify/verify_test.go` | 4 | 3 | Pins static verification commands; green commands can omit meaningful checks. |
| `internal/workspace/deps_test.go` | 4 | 3 | Guards dependency isolation plans; language ecosystems add unmodeled trees. |
| `internal/workspace/discover_test.go` | 4 | 3 | Pins repository and scope discovery; nested repos and worktrees remain tricky. |
| `internal/workspace/platform_test.go` | 4 | 3 | Guards platform path conversion; CI coverage may omit native Windows semantics. |
| `internal/workspace/workspace_test.go` | 4 | 3 | Pins workspace classification; classification does not itself enforce mounts. |
| `internal/wsscan/wsscan_test.go` | 4 | 3 | Guards early workspace risk findings; scanners inevitably miss semantic secrets. |
| `internal/engine/engine.go` | 4 | 4 | Sequences execution and cleanup; partial failure ordering can leak resources. |
| `internal/entrypoint/entrypoint.go` | 4 | 4 | Implements shared seed/prep/launch behavior; subprocess environment boundaries are easy to misstate. |
| `internal/gitidentity/gitidentity.go` | 4 | 4 | Resolves commit identity without mounting gitconfig; sbx and Docker renderings can diverge. |
| `internal/manifest/manifest.go` | 4 | 4 | Defines capabilities and validates defs; empty/default semantics can become broad authority. |
| `internal/runner/pids.go` | 4 | 4 | Persists process ownership for cleanup; stale records can target reused PIDs. |
| `internal/runner/runner.go` | 4 | 4 | Constructs hardened Docker execution; it is fallback infrastructure, not the sbx boundary. |
| `internal/verify/verify.go` | 4 | 4 | Runs configured static verification; command trust and timeout coverage are limited. |
| `internal/workspace/deps.go` | 4 | 4 | Models foreign dependency-tree isolation; direct mode can still rewrite operator state. |
| `internal/workspace/discover.go` | 4 | 4 | Resolves repository, app, and scope roots; symlink and nested-repo ambiguity can choose wrong trees. |
| `internal/workspace/platform.go` | 4 | 4 | Translates host paths across platforms; path normalization can weaken containment checks. |
| `internal/workspace/workspace.go` | 4 | 4 | Defines workspace modes and scope; abstractions may imply guarantees only mounts enforce. |
| `internal/wsscan/wsscan.go` | 4 | 4 | Scans workspaces for risky inputs; heuristic detection is advisory, never prevention. |
| `internal/agentio/agentio_test.go` | 3 | 1 | Pins TTY detection seams; tests cannot reproduce every pipe/console arrangement. |
| `internal/choiceui/anim_test.go` | 3 | 1 | Pins animation timing behavior; timing tests can be platform-fragile. |
| `internal/choiceui/consent_test.go` | 3 | 1 | Guards consent selection basics; it cannot prove informed consent. |
| `internal/choiceui/gated_reason_test.go` | 3 | 1 | Pins disabled-choice explanations; wording can conceal policy causes. |
| `internal/choiceui/hover_test.go` | 3 | 1 | Guards hover state; terminal variance can change interaction. |
| `internal/choiceui/layout_test.go` | 3 | 1 | Pins layout calculations; narrow terminal and Unicode widths remain. |
| `internal/choiceui/pane_test.go` | 3 | 1 | Guards pane rendering; snapshots do not test usability. |
| `internal/choiceui/pen_test.go` | 3 | 1 | Pins styling helpers; color capability detection may disagree. |
| `internal/choiceui/region_order_test.go` | 3 | 1 | Guards stable region ordering; order correctness does not ensure clarity. |
| `internal/choiceui/topology_test.go` | 3 | 1 | Pins topology view composition; displayed topology may lag runtime. |
| `internal/choiceui/viewport_render_test.go` | 3 | 1 | Guards viewport output; golden-like assertions are terminal-specific. |
| `internal/choiceui/viewport_test.go` | 3 | 1 | Pins scrolling state; resize races remain unmodeled. |
| `internal/shell/shell_test.go` | 3 | 1 | Guards shell rc syntax; sourcing behavior varies by invocation mode. |
| `internal/tmux/tmux_test.go` | 3 | 1 | Pins tmux command construction; live server/session behavior is not exercised. |
| `internal/ui/ui_test.go` | 3 | 1 | Guards plain and styled messages; text correctness can outpace factual correctness. |
| `internal/choiceui/anim.go` | 3 | 2 | Drives UI animation; cosmetic state can complicate deterministic automation. |
| `internal/choiceui/canvas.go` | 3 | 2 | Provides terminal drawing primitives; width assumptions can corrupt output. |
| `internal/choiceui/consent.go` | 3 | 2 | Implements consent interactions; defaults remain a security-sensitive choice. |
| `internal/choiceui/field_test.go` | 3 | 2 | Guards field editing behavior; paste and Unicode edge cases remain. |
| `internal/choiceui/pen.go` | 3 | 2 | Encapsulates terminal styles; capability fallback may reduce signal. |
| `internal/choiceui/topology.go` | 3 | 2 | Renders topology summaries; visualization is only as honest as supplied state. |
| `internal/choiceui/viewport.go` | 3 | 2 | Manages scrollable terminal content; asynchronous resize can desynchronize it. |
| `internal/chromebridge/chromebridge_test.go` | 3 | 2 | Guards browser bridge addresses and lifecycle; mocked sockets miss browser auth risks. |
| `internal/clean/clean_test.go` | 3 | 2 | Pins cleanup selection and safety; stale external resources may evade discovery. |
| `internal/maintain/maintain_test.go` | 3 | 2 | Guards image maintenance selection; registry freshness is external. |
| `internal/runlog/runlog_test.go` | 3 | 2 | Pins transcript fields and redaction; novel secret forms may still leak. |
| `internal/choiceui/choiceui.go` | 3 | 3 | Coordinates interactive choices; UI state and policy state can diverge. |
| `internal/choiceui/choiceui_test.go` | 3 | 3 | Exercises choice flows; scripted keys miss human misunderstanding. |
| `internal/choiceui/layout.go` | 3 | 3 | Computes responsive terminal layout; complex width rules are brittle. |
| `internal/chromebridge/chromebridge.go` | 3 | 3 | Bridges sandbox agents to host Chrome; publishing CDP expands host authority. |
| `internal/clean/clean.go` | 3 | 3 | Removes proveo resources and stale state; ownership heuristics risk over- or under-cleaning. |
| `internal/maintain/maintain.go` | 3 | 3 | Resolves and refreshes harness images; mutable tags weaken reproducibility. |
| `internal/shell/shell.go` | 3 | 3 | Edits PATH configuration per shell; rc-file mutation is broad and hard to roll back perfectly. |
| `internal/tmux/tmux.go` | 3 | 3 | Drives detached session interaction; parsing terminal output is inherently fragile. |
| `internal/ui/ui.go` | 3 | 3 | Centralizes operator messaging; consistent styling cannot cure misleading claims. |
| `internal/agentio/agentio.go` | 3 | 4 | Mediates agent TTY input/output; terminal ownership and cancellation have race surfaces. |
| `internal/runlog/runlog.go` | 3 | 4 | Persists run evidence and transcripts; shared writable proveo-home permits cross-run tampering. |
| `internal/egress/testdata/allowlist.golden` | 2 | 1 | Snapshots legacy allowlist topology; proves text, not enforcement. |
| `internal/egress/testdata/allowlist_inject.golden` | 2 | 1 | Snapshots legacy injected-secret topology; stale output can normalize obsolete custody. |
| `internal/egress/testdata/open_broker.golden` | 2 | 1 | Snapshots legacy open/broker plan; open egress weakens the security thesis. |
| `internal/egress/testdata/open_forward.golden` | 2 | 1 | Snapshots open forwarding; it documents deliberate full credential exposure. |
| `internal/egress/testdata/open_forward_local_model.golden` | 2 | 1 | Snapshots local-model forwarding; environment-specific routes may differ. |
| `internal/egress/testdata/review.golden` | 2 | 1 | Snapshots retired review behavior; a golden can keep dead concepts looking current. |
| `internal/egress/allowlist_top_test.go` | 2 | 2 | Pins legacy allowlist ordering; ordering tests do not prove packet isolation. |
| `internal/egress/harness_hosts_test.go` | 2 | 2 | Guards harness host inclusion; static hosts miss dynamic vendor endpoints. |
| `internal/egresspolicy/open_network_test.go` | 2 | 2 | Pins open-network semantics; this is intentionally weak containment. |
| `internal/egresspolicy/secrets_test.go` | 2 | 2 | Tests legacy secret-pattern detection; encoding and request bodies evade patterns. |
| `internal/egressproxy/recorder_test.go` | 2 | 2 | Guards legacy flow records; records lack full request meaning. |
| `internal/ptyproxy/drain_test.go` | 2 | 2 | Pins PTY drain behavior; scheduler timing can still truncate output. |
| `internal/ptyproxy/inputfilter_test.go` | 2 | 2 | Guards duplicate-input filtering; terminal escape variants remain. |
| `internal/reviewgate/reviewgate_test.go` | 2 | 2 | Pins legacy ask-before-connect decisions; the sbx backend cannot provide this gate. |
| `internal/backend/dockeregress/dockeregress.go` | 2 | 3 | Runs the Docker+egress fallback; complex compatibility code no longer carries the main thesis. |
| `internal/egress/egress_test.go` | 2 | 3 | Guards legacy mode normalization; tests can preserve obsolete vocabulary. |
| `internal/egress/exec_test.go` | 2 | 3 | Pins sidecar command execution; mocks miss daemon/network races. |
| `internal/egress/stage_test.go` | 2 | 3 | Guards legacy staging artifacts; filesystem checks do not prove proxy startup. |
| `internal/egresspolicy/defaults.go` | 2 | 3 | Defines legacy default hosts and headers; defaults age into overbroad grants. |
| `internal/egresspolicy/policy_test.go` | 2 | 3 | Exercises legacy policy matching; URL and DNS ambiguities remain. |
| `internal/egressproxy/proxy_test.go` | 2 | 3 | Tests proxy request handling; synthetic traffic misses real TLS/client variation. |
| `internal/ptyproxy/inputfilter.go` | 2 | 3 | Filters PTY replies for review prompts; terminal parsing is not a security boundary. |
| `internal/ptyproxy/overlaytty.go` | 2 | 3 | Overlays interactive review UI on a PTY; no-TTY behavior changes the control path. |
| `internal/ptyproxy/ptyproxy_test.go` | 2 | 3 | Exercises PTY multiplexing; race-heavy live terminals remain underrepresented. |
| `internal/ptyproxy/ptyproxy_windows.go` | 2 | 3 | Supplies Windows PTY compatibility; platform-specific behavior is lightly exercised. |
| `internal/reviewgate/reviewgate.go` | 2 | 3 | Implements legacy per-request approval; unavailable on sbx and easy to overstate. |
| `internal/egress/egress.go` | 2 | 4 | Defines legacy egress modes and environment; names can imply stronger enforcement than delivered. |
| `internal/egress/exec.go` | 2 | 4 | Starts legacy proxy processes; partial startup and teardown can leave gaps. |
| `internal/egress/integration_test.go` | 2 | 4 | Exercises integrated legacy egress; environment skips can hide regressions. |
| `internal/egress/plan_test.go` | 2 | 4 | Broadly pins legacy topology plans; snapshots can fossilize retired architecture. |
| `internal/egress/stage.go` | 2 | 4 | Stages legacy proxy configs and secrets; disk custody and cleanup are failure-prone. |
| `internal/egresspolicy/f1_exploit_test.go` | 2 | 4 | Regresses a known policy bypass; one exploit does not establish completeness. |
| `internal/egresspolicy/policy.go` | 2 | 4 | Evaluates legacy host and route policy; parsing mismatches can under-enforce. |
| `internal/egresspolicy/secrets.go` | 2 | 4 | Detects secret-like egress data; pattern DLP has structural false negatives. |
| `internal/egressproxy/integration_test.go` | 2 | 4 | Exercises MITM integration; local certificates differ from production clients. |
| `internal/egressproxy/recorder.go` | 2 | 4 | Records legacy proxy flows; logs add sensitive metadata without full forensic fidelity. |
| `internal/ptyproxy/ptyproxy.go` | 2 | 4 | Multiplexes agent and approval terminal streams; concurrency failures can bypass usable review. |
| `internal/egress/plan.go` | 2 | 5 | Assembles Docker+proxy topology and credential flow; high complexity serves a fallback thesis. |
| `internal/egressproxy/proxy.go` | 2 | 5 | Terminates TLS, brokers, strips, scans, and forwards; a large attack surface is legacy-only. |
| `internal/contract/egress_vocabulary_test.go` | 1 | 1 | Polices retired egress words; vocabulary hygiene can lag actual semantics. |
| `internal/contract/formatter_class_test.go` | 1 | 1 | Classifies formatting tools; naming conventions are a brittle proxy. |
| `internal/contract/tui_vocabulary_test.go` | 1 | 1 | Polices TUI terminology; consistent words can still describe wrong behavior. |
| `internal/contract/cecli_autoupdater_test.go` | 1 | 2 | Guards cecli updater suppression; static config may stop being honored. |
| `internal/contract/claudecode_autoupdater_test.go` | 1 | 2 | Guards updater suppression text/config; upstream updater behavior may change. |
| `internal/contract/claudecode_renderer_test.go` | 1 | 2 | Pins Claude config rendering; rendering checks do not launch the client. |
| `internal/contract/spec_notes_test.go` | 1 | 2 | Enforces diagram note formatting; readability rules do not validate claims. |
