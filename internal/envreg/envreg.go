// SPEC: _spec/internal/envreg/env-registry.puml
// Package envreg lists every PROVEO_* variable proveo's non-test code reads or sets: Go, image and installers.
package envreg

import "strings"

// Scope says who sets a variable.
type Scope int

const (
	Operator   Scope = iota // a user sets it on the host
	Maintainer              // build, release, CI and test tooling sets it
	Internal                // proveo sets it for its own processes or the sandbox
)

func (s Scope) String() string {
	switch s {
	case Operator:
		return "operator"
	case Maintainer:
		return "maintainer"
	case Internal:
		return "internal"
	}
	return "unknown"
}

// Where says which code reads a variable, as a set of bits.
type Where uint8

const (
	Binary    Where = 1 << iota // proveo's Go: the binaries and the e2e tests proveo-dev drives
	Image                       // shell, node or python inside an agent image
	Installer                   // install.sh, install.ps1 or uninstall.sh
)

// String joins the reader names with "+", e.g. "binary+image".
func (w Where) String() string {
	var out []string
	for _, r := range []struct {
		bit  Where
		name string
	}{{Binary, "binary"}, {Image, "image"}, {Installer, "installer"}} {
		if w&r.bit != 0 {
			out = append(out, r.name)
		}
	}
	return strings.Join(out, "+")
}

// Var is one registered variable.
type Var struct {
	Name    string // a literal PROVEO_* name, or a family with one <PLACEHOLDER>, e.g. PROVEO_<TARGET>_IMAGE
	Scope   Scope
	Where   Where
	Default string // what happens when it is unset, in a few words
	Doc     string // one line: what setting it does
	AliasOf string // the replacement a deprecated alias stands for; empty otherwise
}

// Vars is every registered variable, by scope, then name.
var Vars = []Var{
	{Name: "PROVEO_AGENT_EVIDENCE", Scope: Operator, Where: Binary | Image, Default: "verbose",
		Doc: "Pick agent output detail: default drops the extra narration, verbose keeps it"},
	{Name: "PROVEO_ANDROID_BUILD_TOOLS", Scope: Operator, Where: Image, Default: "36.0.0",
		Doc: "Pin the build-tools an arm64 sandbox installs to run aapt2 under qemu"},
	{Name: "PROVEO_ANDROID_CMDLINE_TOOLS", Scope: Operator, Where: Image, Default: "20.0",
		Doc: "Pin the Android cmdline-tools the seed installs for an Android build"},
	{Name: "PROVEO_ANDROID_WAIT", Scope: Operator, Where: Image, Default: "3600s",
		Doc: "Wait this many seconds for the seed's toolchain before an Android app install"},
	{Name: "PROVEO_ASSET_BASE_URL", Scope: Operator, Where: Binary | Installer, Default: "https://proveo.ca/cli",
		Doc: "Point `proveo update` at another release channel URL"},
	{Name: "PROVEO_AUTO_INSTALL_TOOLS", Scope: Operator, Where: Image, Default: "on",
		Doc: "Let the seed install toolchains, dependencies and language servers"},
	{Name: "PROVEO_AUTO_PROVISION", Scope: Operator, Where: Binary, Default: "ask on a TTY with the wizard on, else decline",
		Doc: "Build a missing agent image without asking; 0 declines without asking"},
	{Name: "PROVEO_BROWSER_SKILL", Scope: Operator, Where: Image, Default: "on",
		Doc: "Seed the agent-browser skill into the agent home of a browser image"},
	{Name: "PROVEO_CLAUDE_LSP_PLUGINS", Scope: Operator, Where: Image, Default: "on",
		Doc: "Enable the official Claude Code LSP plugins the image seeds"},
	{Name: "PROVEO_CLONE", Scope: Operator, Where: Binary, Default: "on",
		Doc: "Run the agent on a clone of the workspace instead of the live tree; 0 turns it off"},
	{Name: "PROVEO_CODEX_SANDBOX", Scope: Operator, Where: Image, Default: "codex's sandbox and approvals bypassed",
		Doc: "Launch codex with --sandbox <value> --ask-for-approval never instead of the bypass"},
	{Name: "PROVEO_CONFIG_SYNC", Scope: Operator, Where: Image, Default: "on",
		Doc: "Sync the agent's config dirs and files with the proveo home"},
	{Name: "PROVEO_CURSOR_AUDIT_LOG", Scope: Operator, Where: Image, Default: "~/.cursor/audit-shell.ndjson",
		Doc: "Set the file cursor's shell-audit hook appends each command to"},
	{Name: "PROVEO_CURSOR_SANDBOX", Scope: Operator, Where: Image, Default: "disabled",
		Doc: "Set cursor-agent's --sandbox mode"},
	{Name: "PROVEO_CWD_GUARD", Scope: Operator, Where: Image, Default: "on",
		Doc: "Install the Claude Code hook that reports a vanished working directory"},
	{Name: "PROVEO_DEPS", Scope: Operator, Where: Binary | Image, Default: "auto: refuse foreign host trees; install missing ones",
		Doc: "auto|off|shared|reinstall: shared or reinstall carries host trees across platforms"},
	{Name: "PROVEO_DEPS_PROBE_LIMIT", Scope: Operator, Where: Image, Default: "60",
		Doc: "Probe at most this many native files per dependency tree for a foreign platform"},
	{Name: "PROVEO_DEPS_TIMEOUT", Scope: Operator, Where: Image, Default: "900s",
		Doc: "Bound each dependency install, in seconds"},
	{Name: "PROVEO_DEP_SCAN_DEPTH", Scope: Operator, Where: Image, Default: "4",
		Doc: "Set how many directories deep the seed looks for project roots"},
	{Name: "PROVEO_ENV_FILE", Scope: Operator, Where: Binary, Default: "first .env in cwd, input dir, repo root",
		Doc: "Read provider keys and run settings from this file instead of the project .env"},
	{Name: "PROVEO_GIT_SYNC", Scope: Operator, Where: Image, Default: "on",
		Doc: "Commit and push the workspace at the end of each agent turn"},
	{Name: "PROVEO_GIT_SYNC_MODEL_CLAUDE", Scope: Operator, Where: Image, Default: "sonnet",
		Doc: "Pick the model claude writes git-sync commit subjects with"},
	{Name: "PROVEO_GIT_SYNC_MODEL_CODEX", Scope: Operator, Where: Image, Default: "gpt-5-nano",
		Doc: "Pick the model codex writes git-sync commit subjects with"},
	{Name: "PROVEO_GIT_SYNC_MODEL_CURSOR", Scope: Operator, Where: Image, Default: "none: subjects name the changed paths",
		Doc: "Pick the model cursor-agent writes git-sync commit subjects with"},
	{Name: "PROVEO_GIT_SYNC_MODEL_OPENCODE", Scope: Operator, Where: Image, Default: "anthropic/claude-haiku-4-5",
		Doc: "Pick the model opencode writes git-sync commit subjects with"},
	{Name: "PROVEO_GIT_SYNC_MSG", Scope: Operator, Where: Image, Default: "on",
		Doc: "Let a model write git-sync commit subjects; off names the changed paths"},
	{Name: "PROVEO_GIT_SYNC_MSG_TIMEOUT", Scope: Operator, Where: Image, Default: "20s",
		Doc: "Bound the commit-subject model call, in seconds; the one retry doubles it"},
	{Name: "PROVEO_GIT_SYNC_TRACE", Scope: Operator, Where: Image, Default: "<git dir>/proveo-git-sync.ndjson",
		Doc: "Set the file git-sync appends its trace to; off writes none"},
	{Name: "PROVEO_GLYPHS", Scope: Operator, Where: Binary, Default: "Nerd Font glyphs",
		Doc: "Pick status glyphs: ascii, or off for none"},
	{Name: "PROVEO_GRADLE_VERSION", Scope: Operator, Where: Image, Default: "latest",
		Doc: "Pin the Gradle the seed installs for a Gradle build without a wrapper"},
	{Name: "PROVEO_HOME", Scope: Operator, Where: Binary | Image, Default: "~/.proveo",
		Doc: "Set the host dir of agent homes, logins and toolchains; in the sandbox, the agent home"},
	{Name: "PROVEO_HOST_ADB_PORT", Scope: Operator, Where: Binary | Image, Default: "5037",
		Doc: "Set the host adb server port the android add-on uses; the image needs it set"},
	{Name: "PROVEO_HOST_AVD", Scope: Operator, Where: Binary, Default: "the first AVD listed",
		Doc: "Name the Android Virtual Device the android add-on boots"},
	{Name: "PROVEO_HOST_CDP_PORT", Scope: Operator, Where: Binary | Image, Default: "9222",
		Doc: "Set the host Chrome DevTools port the host-chrome add-on uses; the image needs it set"},
	{Name: "PROVEO_HOUSE_RULES", Scope: Operator, Where: Image, Default: "on",
		Doc: "Write proveo's house rules into codex's and opencode's user AGENTS.md"},
	{Name: "PROVEO_IMAGE_ADB", Scope: Operator, Where: Image, Default: "/usr/bin/adb",
		Doc: "The image's own adb, linked over an Android SDK adb that cannot run in the sandbox"},
	{Name: "PROVEO_INSTALL_LOCK_WAIT", Scope: Operator, Where: Image, Default: "300s",
		Doc: "Wait this many seconds for another run's tool installs, then skip installs"},
	{Name: "PROVEO_INSTALL_ROOT", Scope: Operator, Where: Binary | Installer, Default: "~/.proveo",
		Doc: "Set the install root that holds bin/proveo and that `proveo uninstall` removes"},
	{Name: "PROVEO_INSTRUCTIONS_WAIT", Scope: Operator, Where: Image, Default: "30s; the opencode image sets 5",
		Doc: "Wait this many seconds for the instruction seed, then launch the agent anyway"},
	{Name: "PROVEO_JAVA_VERSION", Scope: Operator, Where: Image, Default: "temurin-21",
		Doc: "Pick the Java the seed installs for a Gradle build when the image's is below 17"},
	{Name: "PROVEO_JDTLS_URL", Scope: Operator, Where: Image, Default: "Eclipse's jdtls snapshot tarball",
		Doc: "Download the Java language server from this tarball URL"},
	{Name: "PROVEO_KEEP_AGENT_LOGINS", Scope: Operator, Where: Binary, Default: "off",
		Doc: "Keep logins an agent makes inside a run instead of scrubbing them next run"},
	{Name: "PROVEO_LOCAL_MODEL_FORCE", Scope: Operator, Where: Binary, Default: "off",
		Doc: "Load a local model even when host memory looks too small"},
	{Name: "PROVEO_LOCAL_MODEL_PULL", Scope: Operator, Where: Binary, Default: "on",
		Doc: "Pull a local model missing from the host's Ollama; 0 fails instead"},
	{Name: "PROVEO_LSP_INSTALL", Scope: Operator, Where: Image, Default: "auto: every detected language",
		Doc: "Pick language servers to install: auto (or any on word), off or a comma-separated list"},
	{Name: "PROVEO_LSP_INSTALL_TIMEOUT", Scope: Operator, Where: Image, Default: "180s",
		Doc: "Bound each mise install the seed runs, language servers included, in seconds"},
	{Name: "PROVEO_LSP_MIN_FILES", Scope: Operator, Where: Image, Default: "1",
		Doc: "Install a language's server only when this many files use the language"},
	{Name: "PROVEO_MOBILE_MCP_VERSION", Scope: Operator, Where: Image, Default: "1.0.6",
		Doc: "Pin the mobile-mcp version the android add-on installs"},
	{Name: "PROVEO_MOUNT_GH_CONFIG", Scope: Operator, Where: Binary, Default: "on",
		Doc: "Mount the host gh config read-only into the agent home; 0 turns it off"},
	{Name: "PROVEO_MOUNT_ROOT_DEPS", Scope: Operator, Where: Binary, Default: "on",
		Doc: "Mount the repo root's dependency trees into a sub-scope run; 0 turns it off"},
	{Name: "PROVEO_NODE_TIMEOUT", Scope: Operator, Where: Image, Default: "180s",
		Doc: "Bound corepack's package-manager activation, in seconds"},
	{Name: "PROVEO_NODE_TOOLCHAIN", Scope: Operator, Where: Image, Default: "on",
		Doc: "Activate the package manager and Node version package.json names"},
	{Name: "PROVEO_OPENCODE_FORMATTER", Scope: Operator, Where: Image, Default: "on",
		Doc: "Wire the workspace's formatters into opencode.json"},
	{Name: "PROVEO_PYTHON_ENV", Scope: Operator, Where: Image, Default: "on",
		Doc: "Build a virtualenv for each Python project the seed finds"},
	{Name: "PROVEO_PYTHON_MAX_PROJECTS", Scope: Operator, Where: Image, Default: "4",
		Doc: "Provision at most this many Python projects; the seed lists the rest"},
	{Name: "PROVEO_PYTHON_SCAN_DEPTH", Scope: Operator, Where: Image, Default: "PROVEO_DEP_SCAN_DEPTH",
		Doc: "Override PROVEO_DEP_SCAN_DEPTH for Python project roots"},
	{Name: "PROVEO_PYTHON_TIMEOUT", Scope: Operator, Where: Image, Default: "600s",
		Doc: "Bound each Python environment build, in seconds"},
	{Name: "PROVEO_PYTHON_VERSION", Scope: Operator, Where: Image, Default: "3.12",
		Doc: "Pick the Python for a project that names none"},
	{Name: "PROVEO_SBX_CPUS", Scope: Operator, Where: Binary, Default: "no --cpus: sbx decides",
		Doc: "Set the sandbox CPU count, clamped to the host's"},
	{Name: "PROVEO_SBX_INSTANCES", Scope: Operator, Where: Binary, Default: "no split: sbx sizes each sandbox",
		Doc: "Size sandboxes for N at once: Docker's memory total / N, half the CPUs when N ≥ 2"},
	{Name: "PROVEO_SBX_MCP", Scope: Operator, Where: Binary, Default: "off",
		Doc: "Let sbx's MCP gateway register in the agent; outranks the run screen's mcp row"},
	{Name: "PROVEO_SBX_MEMORY", Scope: Operator, Where: Binary, Default: "no -m: sbx decides",
		Doc: "Set the sandbox memory ceiling, e.g. 16g; capped at 32g"},
	{Name: "PROVEO_SEED_PROGRESS", Scope: Operator, Where: Image, Default: "off; the opencode image sets on",
		Doc: "Show the seed's progress while an opencode launch waits for it"},
	{Name: "PROVEO_SKIP_INIT", Scope: Operator, Where: Installer, Default: "the installer runs `proveo init`",
		Doc: "Skip the `proveo init` sbx bootstrap at the end of an install"},
	{Name: "PROVEO_STATE_ROOT", Scope: Operator, Where: Binary, Default: "${XDG_STATE_HOME:-~/.local/state}/proveo",
		Doc: "Set the host directory that holds per-run state under runs/<sid>"},
	{Name: "PROVEO_STDIN_FILTER", Scope: Operator, Where: Binary, Default: "on",
		Doc: "Strip terminal report replies from the agent's stdin; 0 passes them through"},
	{Name: "PROVEO_TOOL_SYNC", Scope: Operator, Where: Image, Default: "on",
		Doc: "Keep installed toolchains in the proveo home across runs"},
	{Name: "PROVEO_UI_DEFAULTS", Scope: Operator, Where: Image, Default: "on",
		Doc: "Apply proveo's Claude Code theme and UI setting defaults"},
	{Name: "PROVEO_UNINSTALL_ASSUME_YES", Scope: Operator, Where: Binary | Installer, Default: "off",
		Doc: "Skip the `proveo uninstall` confirmation"},
	{Name: "PROVEO_WIZARD", Scope: Operator, Where: Binary, Default: "on",
		Doc: "Show interactive pickers and prompts on a TTY; 0 skips them"},

	{Name: "PROVEO_<TARGET>_IMAGE", Scope: Maintainer, Where: Binary, Default: "proveo/<target>:<tag>",
		Doc: "Override a target's image repo and the ref its children build from; claudecode* take none"},
	{Name: "PROVEO_AAPT2_WRAPPER", Scope: Maintainer, Where: Image, Default: "/usr/local/lib/proveo/aapt2",
		Doc: "Test seam: set the qemu aapt2 wrapper an arm64 Android build runs"},
	{Name: "PROVEO_ANDROID_LOG", Scope: Maintainer, Where: Image, Default: "/tmp/proveo-android-install.log",
		Doc: "Test seam: set the log the Android app install appends to"},
	{Name: "PROVEO_BUILDKIT_CACHE", Scope: Maintainer, Where: Binary, Default: "on",
		Doc: "Share a local BuildKit cache across image builds; 0 turns it off"},
	{Name: "PROVEO_BUILDKIT_CACHE_DIR", Scope: Maintainer, Where: Binary, Default: "${XDG_CACHE_HOME:-~/.cache}/proveo/buildkit",
		Doc: "Set the local BuildKit cache directory"},
	{Name: "PROVEO_BUILDX_BUILDER", Scope: Maintainer, Where: Binary, Default: "proveo-multiarch",
		Doc: "Name the docker-container buildx builder for push and cross-platform builds"},
	{Name: "PROVEO_BUILDX_LOCAL_BUILDER", Scope: Maintainer, Where: Binary, Default: "docker-driver context, else proveo-multiarch",
		Doc: "Name the buildx builder for host-platform --load builds"},
	{Name: "PROVEO_CDN_REQUIRE_DIST", Scope: Maintainer, Where: Binary, Default: "off",
		Doc: "Fail stage-cdn when dist/ lacks a binary instead of cross-compiling"},
	{Name: "PROVEO_CDN_ROOT", Scope: Maintainer, Where: Binary, Default: "apps/cli/public/cli",
		Doc: "Set the CDN tree stage-cdn writes"},
	{Name: "PROVEO_CLAUDE_MANAGED_MD", Scope: Maintainer, Where: Image, Default: "/etc/claude-code/CLAUDE.md",
		Doc: "Test seam: set the managed CLAUDE.md the operator-name rewrite edits"},
	{Name: "PROVEO_COV_DIR", Scope: Maintainer, Where: Binary, Default: "cov",
		Doc: "Set the coverage data directory proveo-dev coverage writes"},
	{Name: "PROVEO_CWD_GUARD_HOOK", Scope: Maintainer, Where: Image, Default: "/opt/claudecode/defaults/hooks/cwd-guard.sh",
		Doc: "Test seam: set the cwd-guard script the Claude Code hook runs"},
	{Name: "PROVEO_DEFS_DIR", Scope: Maintainer, Where: Binary, Default: "embedded manifests; <repo>/defs for builds",
		Doc: "Load harness manifests and image defs from this directory"},
	{Name: "PROVEO_DIST_DIR", Scope: Maintainer, Where: Binary, Default: "dist",
		Doc: "Set the goreleaser dist directory stage-cdn reads binaries from"},
	{Name: "PROVEO_DOCKER_PULL", Scope: Maintainer, Where: Binary, Default: "auto",
		Doc: "Set buildx --pull: auto, 0/never or 1/always"},
	{Name: "PROVEO_DOCKER_PUSH", Scope: Maintainer, Where: Binary, Default: "off",
		Doc: "Push image builds to the registry instead of loading them locally"},
	{Name: "PROVEO_GITHUB_META_URL", Scope: Maintainer, Where: Image, Default: "https://api.github.com/meta",
		Doc: "Test seam: set the URL the seed fetches GitHub's SSH host keys from"},
	{Name: "PROVEO_GIT_SYNC_HOOK", Scope: Maintainer, Where: Image, Default: "/opt/proveo/hooks/git-sync-turn.sh",
		Doc: "Test seam: set the git-sync script the agent's hooks run"},
	{Name: "PROVEO_GIT_SYNC_PLUGIN", Scope: Maintainer, Where: Image, Default: "/opt/proveo/hooks/proveo-git-sync-turn.js",
		Doc: "Test seam: set the OpenCode git-sync plugin the seed installs"},
	{Name: "PROVEO_HOOKS_MARKER", Scope: Maintainer, Where: Image, Default: "/dev/shm/proveo-hooks-seeded",
		Doc: "Test seam: set the file the seed touches once the agent's hooks are installed"},
	{Name: "PROVEO_IDLE_FOR", Scope: Maintainer, Where: Binary, Default: "45s from proveo-dev; 5m in the test",
		Doc: "Set how long the e2e idle test leaves the agent idle"},
	{Name: "PROVEO_IDLE_TARGETS", Scope: Maintainer, Where: Binary, Default: "claudecode",
		Doc: "List the defs the e2e idle test runs, comma-separated"},
	{Name: "PROVEO_IDLE_TEST", Scope: Maintainer, Where: Binary, Default: "off",
		Doc: "Run the e2e idle-survival test"},
	{Name: "PROVEO_INSTRUCTIONS_DEFAULTS", Scope: Maintainer, Where: Image, Default: "/opt/claudecode/defaults/AGENTS.md",
		Doc: "Test seam: set the AGENTS.md the seed copies into a bare claudecode workspace"},
	{Name: "PROVEO_INSTRUCTIONS_MARKER", Scope: Maintainer, Where: Image, Default: "/dev/shm/proveo-instructions-seeded",
		Doc: "Test seam: set the file whose presence releases the waiting agent"},
	{Name: "PROVEO_LADDER_TARGET", Scope: Maintainer, Where: Binary, Default: "claudecode",
		Doc: "Name the def the e2e sandbox ladder climbs"},
	{Name: "PROVEO_LADDER_TEST", Scope: Maintainer, Where: Binary, Default: "off",
		Doc: "Run the e2e sandbox ladder"},
	{Name: "PROVEO_OPENCODE_DEFAULTS_DIR", Scope: Maintainer, Where: Image, Default: "/opt/opencode/defaults",
		Doc: "Test seam: set the directory the seed copies OpenCode's defaults from"},
	{Name: "PROVEO_PLATFORMS", Scope: Maintainer, Where: Binary, Default: "linux/amd64,linux/arm64",
		Doc: "Set buildx platforms; a local --load of a list keeps only the host's"},
	{Name: "PROVEO_PROC", Scope: Maintainer, Where: Image, Default: "/proc",
		Doc: "Test seam: set the /proc root the cwd-guard hook reads"},
	{Name: "PROVEO_PROC_ROOT", Scope: Maintainer, Where: Image, Default: "/proc",
		Doc: "Test seam: set the /proc root the seed reads PID 1's PATH from"},
	{Name: "PROVEO_PROVISIONING", Scope: Maintainer, Where: Image, Default: "/dev/shm/proveo-provisioning",
		Doc: "Test seam: set the file that holds the provisioning seed's PID"},
	{Name: "PROVEO_RELEASE_SNAPSHOT", Scope: Maintainer, Where: Image, Default: "/dev/shm/proveo-release-snapshot",
		Doc: "Test seam: set the directory the seed copies the agent's startup files into"},
	{Name: "PROVEO_SCHEDULE_NOW", Scope: Maintainer, Where: Binary, Default: "the wall clock",
		Doc: "Pin the scheduler's clock to an RFC 3339 time"},
	{Name: "PROVEO_SEED_DONE_MARKER", Scope: Maintainer, Where: Image, Default: "/dev/shm/proveo-seed-done",
		Doc: "Test seam: set the file whose presence makes a provision seed exit early"},
	{Name: "PROVEO_SEED_HALTED", Scope: Maintainer, Where: Image, Default: "/dev/shm/proveo-seed-halted",
		Doc: "Test seam: set the file that marks a failed release-phase seed"},
	{Name: "PROVEO_SEED_REFUSED", Scope: Maintainer, Where: Image, Default: "/dev/shm/proveo-seed-refused",
		Doc: "Test seam: set the file where the seed records why it held the agent back"},
	{Name: "PROVEO_SEED_STATUS_FILE", Scope: Maintainer, Where: Image, Default: "/dev/shm/proveo-seed-status",
		Doc: "Test seam: set the file the seed writes its progress label to"},
	{Name: "PROVEO_SMOKE_TARGET", Scope: Maintainer, Where: Image, Default: "the def's own name",
		Doc: "Name the target in the smoke-mode ready line"},
	{Name: "PROVEO_SMOKE_TEST", Scope: Maintainer, Where: Image, Default: "off",
		Doc: "Print a ready line and sleep instead of launching the agent"},
	{Name: "PROVEO_SSH_KNOWN_HOSTS", Scope: Maintainer, Where: Image, Default: "/etc/ssh/ssh_known_hosts",
		Doc: "Test seam: set the system known_hosts file checked for GitHub's keys"},
	{Name: "PROVEO_SUBAGENTS_DIR", Scope: Maintainer, Where: Image, Default: "/opt/proveo/subagents",
		Doc: "Set the subagent defs directory the image's subagent renderer reads"},
	{Name: "PROVEO_TEST_GO_TIMEOUT", Scope: Maintainer, Where: Binary, Default: "45m for coverage e2e; 150m for e2e-full",
		Doc: "Set the go test timeout of the e2e lanes"},
	{Name: "PROVEO_TOOLCHAIN_READY", Scope: Maintainer, Where: Image, Default: "/tmp/proveo-toolchain-ready",
		Doc: "Test seam: set the file that marks the seed's toolchain ready"},
	{Name: "PROVEO_TOOLCHAIN_TEST", Scope: Maintainer, Where: Binary, Default: "off",
		Doc: "Run the e2e language toolchain tests"},
	{Name: "PROVEO_TRACE_STDIN", Scope: Maintainer, Where: Binary, Default: "no trace",
		Doc: "Append every byte sent to the agent's stdin to this file"},
	{Name: "PROVEO_VERSION", Scope: Maintainer, Where: Binary | Installer, Default: "dist metadata, else git describe, else dev; installers: latest",
		Doc: "Set the version stage-cdn stamps; the installers only label the install with it"},

	{Name: "PROVEO_ADDRESS", Scope: Internal, Where: Image, Default: "the lib always sets it",
		Doc: "Hand awk the operator name the `Address me as` rewrite inserts"},
	{Name: "PROVEO_AGENT_HOME", Scope: Internal, Where: Image, Default: "the lib always sets it",
		Doc: "Hand a node child the agent home whose JSON config it edits"},
	{Name: "PROVEO_AGENT_KIND", Scope: Internal, Where: Image, Default: "a coding agent",
		Doc: "Mark an assistant def so the seed skips toolchain and dependency provisioning"},
	{Name: "PROVEO_CLAUDE_LSP_OFFICIAL", Scope: Internal, Where: Image, Default: "no official LSP plugin enabled",
		Doc: "List the languages official Claude LSP plugins cover, so proveo's LSP plugin skips them"},
	{Name: "PROVEO_CLONE_ENV", Scope: Internal, Where: Image, Default: "no .env linked",
		Doc: "Name the staged host .env the seed links into a clone workspace"},
	{Name: "PROVEO_CLONE_LINKS", Scope: Internal, Where: Image, Default: "no links recreated",
		Doc: "List rel=target symlinks, |-separated, the seed recreates in a clone"},
	{Name: "PROVEO_CLONE_MAIN", Scope: Internal, Where: Image, Default: "no linked worktree",
		Doc: "Name the clone a linked worktree is added to inside the sandbox"},
	{Name: "PROVEO_CLONE_REF", Scope: Internal, Where: Image, Default: "no linked worktree",
		Doc: "Name the ref the seed checks out in a linked worktree's clone"},
	{Name: "PROVEO_CLONE_WORKSPACE", Scope: Internal, Where: Image, Default: "no clone base recorded",
		Doc: "Mark a clone workspace so the seed records the clone's base commit"},
	{Name: "PROVEO_CONFIG_DIRS", Scope: Internal, Where: Image, Default: "no home dirs synced",
		Doc: "List the agent-home config dirs the image syncs with the proveo home"},
	{Name: "PROVEO_CONFIG_FILES", Scope: Internal, Where: Image, Default: "no home files synced",
		Doc: "List the agent-home config files, ;-separated, the image syncs with the proveo home"},
	{Name: "PROVEO_CONFIG_FILES_ROOT", Scope: Internal, Where: Image, Default: "no host file root",
		Doc: "Name the host directory that holds the run's synced agent-home config files"},
	{Name: "PROVEO_GIT_SYNC_DIALECT", Scope: Internal, Where: Image, Default: "read from the hook payload",
		Doc: "Tell the git-sync hook which hook protocol calls it; the OpenCode plugin sets idle"},
	{Name: "PROVEO_GIT_SYNC_MSG_INFLIGHT", Scope: Internal, Where: Image, Default: "a normal agent run",
		Doc: "Mark the commit-subject model call so its own git-sync hooks stand down"},
	{Name: "PROVEO_HOOK", Scope: Internal, Where: Image, Default: "the lib always sets it",
		Doc: "Hand a node child the cwd-guard script it registers as a hook"},
	{Name: "PROVEO_HOOKS_FILE", Scope: Internal, Where: Image, Default: "the lib always sets it",
		Doc: "Hand a node or python child the hooks file it adds a Stop hook to"},
	{Name: "PROVEO_HOOK_CMD", Scope: Internal, Where: Image, Default: "the lib always sets it",
		Doc: "Hand a node or python child the Stop hook command it adds"},
	{Name: "PROVEO_HOST_OS", Scope: Internal, Where: Image, Default: "treated as not darwin",
		Doc: "Tell the sandbox the host's OS, as Go's GOOS names it"},
	{Name: "PROVEO_LOCAL_MODEL", Scope: Internal, Where: Image, Default: "no local model",
		Doc: "Carry the --local-model Ollama tag into the sandbox"},
	{Name: "PROVEO_OPERATOR_NAME", Scope: Internal, Where: Image, Default: "the image keeps its house name",
		Doc: "Carry the operator name `proveo init` stored into the sandbox"},
	{Name: "PROVEO_PLUGINS", Scope: Internal, Where: Image, Default: "the lib always sets it",
		Doc: "Hand a node child the LSP plugin names it installs and enables"},
	{Name: "PROVEO_SCHEDULE", Scope: Internal, Where: Binary, Default: "a headed run",
		Doc: "Mark an unattended scheduled run: no headed prompts; Ctrl+C reaches the agent"},
	{Name: "PROVEO_SCOPE_REL", Scope: Internal, Where: Image, Default: "git sees the whole repo",
		Doc: "Name the repo-relative sub-scope the image scopes git to"},
	{Name: "PROVEO_SEED", Scope: Internal, Where: Image, Default: "the lib always sets it",
		Doc: "Hand a node child the Claude Code plugin seed directory"},
	{Name: "PROVEO_SEED_ONLY", Scope: Internal, Where: Image, Default: "the entrypoint launches the agent",
		Doc: "Stop the def entrypoint before it launches the agent"},
	{Name: "PROVEO_SEED_PHASE", Scope: Internal, Where: Image, Default: "all",
		Doc: "Pick the seed phase to run; none skips the shared seed"},
	{Name: "PROVEO_SHARES", Scope: Internal, Where: Image, Default: "no share assertions",
		Doc: "List rel=rw|ro pairs, |-separated, the seed asserts before it releases the agent"},
	{Name: "PROVEO_STATE_HOME", Scope: Internal, Where: Image, Default: "the agent's own home",
		Doc: "Hand the sandbox the host path of the proveo home for config and tool sync"},
	{Name: "PROVEO_TRUST_DIR", Scope: Internal, Where: Image, Default: "the lib always sets it",
		Doc: "Hand a node child the directory whose trust dialog it marks accepted"},
	{Name: "PROVEO_WORKDIR", Scope: Internal, Where: Image, Default: "the current directory",
		Doc: "Name the agent's working directory inside the sandbox"},
}

// ByScope returns the Vars of scope s, in Vars order.
func ByScope(s Scope) []Var {
	var out []Var
	for _, v := range Vars {
		if v.Scope == s {
			out = append(out, v)
		}
	}
	return out
}
