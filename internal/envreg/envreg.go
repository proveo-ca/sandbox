// SPEC: _spec/internal/envreg/env-registry.puml
// Package envreg lists every PROVEO_* environment variable proveo's Go code reads or sets.
package envreg

import "strings"

// Scope says who sets a variable.
type Scope int

const (
	Operator   Scope = iota // a user sets it on the host
	Maintainer              // build, release, CI and test tooling sets it
	Internal                // proveo sets it for its own processes or the sandbox
)

// Scopes is every scope, in Vars order.
var Scopes = []Scope{Operator, Maintainer, Internal}

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

// Var is one registered variable.
type Var struct {
	Name    string // a literal PROVEO_* name, or a family with one <PLACEHOLDER>, e.g. PROVEO_<TARGET>_IMAGE
	Scope   Scope
	Default string // what happens when it is unset, in a few words
	Doc     string // one line: what setting it does
}

// Vars is every registered variable, by scope, then name.
var Vars = []Var{
	{Name: "PROVEO_AGENT_EVIDENCE", Scope: Operator, Default: "verbose",
		Doc: "Pick agent output detail: default drops the extra narration, verbose keeps it"},
	{Name: "PROVEO_ASSET_BASE_URL", Scope: Operator, Default: "https://proveo.ca/cli",
		Doc: "Point `proveo update` at another release channel URL"},
	{Name: "PROVEO_AUTO_PROVISION", Scope: Operator, Default: "ask on a TTY, else decline",
		Doc: "Build a missing agent image without asking; 0 declines without asking"},
	{Name: "PROVEO_CLONE", Scope: Operator, Default: "on",
		Doc: "Run the agent on a clone of the workspace instead of the live tree; 0 turns it off"},
	{Name: "PROVEO_DEPS", Scope: Operator, Default: "refuse host trees across platforms",
		Doc: "shared or reinstall lets a mounted checkout's dependency trees cross platforms"},
	{Name: "PROVEO_EGRESS_ENV_FILE", Scope: Operator, Default: "unset",
		Doc: "Deprecated alias of PROVEO_ENV_FILE"},
	{Name: "PROVEO_EGRESS_ROOT", Scope: Operator, Default: "unset",
		Doc: "Deprecated alias of PROVEO_STATE_ROOT"},
	{Name: "PROVEO_ENV_FILE", Scope: Operator, Default: "first .env in cwd, input dir, repo root",
		Doc: "Read provider keys and run settings from this file instead of the project .env"},
	{Name: "PROVEO_GLYPHS", Scope: Operator, Default: "Nerd Font glyphs",
		Doc: "Pick status glyphs: ascii, or off for none"},
	{Name: "PROVEO_HOME", Scope: Operator, Default: "~/.proveo",
		Doc: "Set the host directory that holds agent homes, logins and toolchains"},
	{Name: "PROVEO_HOST_ADB_PORT", Scope: Operator, Default: "5037",
		Doc: "Set the host adb server port the android add-on uses"},
	{Name: "PROVEO_HOST_AVD", Scope: Operator, Default: "the first AVD listed",
		Doc: "Name the Android Virtual Device the android add-on boots"},
	{Name: "PROVEO_HOST_CDP_PORT", Scope: Operator, Default: "9222",
		Doc: "Set the host Chrome DevTools port the host-chrome add-on uses"},
	{Name: "PROVEO_INSTALL_ROOT", Scope: Operator, Default: "~/.proveo",
		Doc: "Set the install root `proveo uninstall` removes"},
	{Name: "PROVEO_KEEP_AGENT_LOGINS", Scope: Operator, Default: "off",
		Doc: "Keep logins an agent makes inside a run instead of scrubbing them next run"},
	{Name: "PROVEO_KEYCHAIN_SERVICE", Scope: Operator, Default: "Claude Code's own item names",
		Doc: "Name the macOS Keychain item proveo reads the Claude Code login from"},
	{Name: "PROVEO_LOCAL_MODEL_FORCE", Scope: Operator, Default: "off",
		Doc: "Load a local model even when host memory looks too small"},
	{Name: "PROVEO_LOCAL_MODEL_PULL", Scope: Operator, Default: "on",
		Doc: "Pull a local model missing from the host's Ollama; 0 fails instead"},
	{Name: "PROVEO_MOUNT_GH_CONFIG", Scope: Operator, Default: "on",
		Doc: "Mount the host gh config read-only into the agent home; 0 turns it off"},
	{Name: "PROVEO_MOUNT_ROOT_DEPS", Scope: Operator, Default: "on",
		Doc: "Mount the repo root's dependency trees into a sub-scope run; 0 turns it off"},
	{Name: "PROVEO_SBX_CPUS", Scope: Operator, Default: "no --cpus: sbx decides",
		Doc: "Set the sandbox CPU count, clamped to the host's"},
	{Name: "PROVEO_SBX_INSTANCES", Scope: Operator, Default: "no split: sbx sizes each sandbox",
		Doc: "Size sandboxes for N at once: host memory / N, half the CPUs when N ≥ 2"},
	{Name: "PROVEO_SBX_MCP", Scope: Operator, Default: "off",
		Doc: "Let sbx's MCP gateway register in the agent"},
	{Name: "PROVEO_SBX_MEMORY", Scope: Operator, Default: "no -m: sbx decides",
		Doc: "Set the sandbox memory ceiling, e.g. 16g; capped at 32g"},
	{Name: "PROVEO_SBX_RELOAD", Scope: Operator, Default: "off",
		Doc: "Drop and reload the agent image in sbx's template store before the run"},
	{Name: "PROVEO_STATE_ROOT", Scope: Operator, Default: "${XDG_STATE_HOME:-~/.local/state}/proveo",
		Doc: "Set the host directory that holds per-run state under runs/<sid>"},
	{Name: "PROVEO_STDIN_FILTER", Scope: Operator, Default: "on",
		Doc: "Strip terminal report replies from the agent's stdin; 0 passes them through"},
	{Name: "PROVEO_UNINSTALL_ASSUME_YES", Scope: Operator, Default: "off",
		Doc: "Skip the `proveo uninstall` confirmation"},
	{Name: "PROVEO_WIZARD", Scope: Operator, Default: "on",
		Doc: "Show interactive pickers and prompts on a TTY; 0 skips them"},

	{Name: "PROVEO_<TARGET>_IMAGE", Scope: Maintainer, Default: "proveo/<target>:<tag>",
		Doc: "Override a build target's image repo and the ref its child images build from"},
	{Name: "PROVEO_BIN", Scope: Maintainer, Default: "nothing reads it",
		Doc: "Appears in build-cli's PATH hint only; no code reads it"},
	{Name: "PROVEO_BUILDKIT_CACHE", Scope: Maintainer, Default: "on",
		Doc: "Share a local BuildKit cache across image builds; 0 turns it off"},
	{Name: "PROVEO_BUILDKIT_CACHE_DIR", Scope: Maintainer, Default: "${XDG_CACHE_HOME:-~/.cache}/proveo/buildkit",
		Doc: "Set the local BuildKit cache directory"},
	{Name: "PROVEO_BUILDX_BUILDER", Scope: Maintainer, Default: "proveo-multiarch",
		Doc: "Name the docker-container buildx builder for push and cross-platform builds"},
	{Name: "PROVEO_BUILDX_LOCAL_BUILDER", Scope: Maintainer, Default: "docker-driver context, else proveo-multiarch",
		Doc: "Name the buildx builder for host-platform --load builds"},
	{Name: "PROVEO_CDN_REQUIRE_DIST", Scope: Maintainer, Default: "off",
		Doc: "Fail stage-cdn when dist/ lacks a binary instead of cross-compiling"},
	{Name: "PROVEO_CDN_ROOT", Scope: Maintainer, Default: "apps/cli/public/cli",
		Doc: "Set the CDN tree stage-cdn writes"},
	{Name: "PROVEO_COV_DIR", Scope: Maintainer, Default: "cov",
		Doc: "Set the coverage data directory proveo-dev coverage writes"},
	{Name: "PROVEO_DEFS_DIR", Scope: Maintainer, Default: "embedded manifests; <repo>/defs for builds",
		Doc: "Load harness manifests and image defs from this directory"},
	{Name: "PROVEO_DIST_DIR", Scope: Maintainer, Default: "dist",
		Doc: "Set the goreleaser dist directory stage-cdn reads binaries from"},
	{Name: "PROVEO_DOCKER_PULL", Scope: Maintainer, Default: "auto",
		Doc: "Set buildx --pull: auto, 0/never or 1/always"},
	{Name: "PROVEO_DOCKER_PUSH", Scope: Maintainer, Default: "off",
		Doc: "Push image builds to the registry instead of loading them locally"},
	{Name: "PROVEO_IDLE_FOR", Scope: Maintainer, Default: "45s from proveo-dev; 5m in the test",
		Doc: "Set how long the e2e idle test leaves the agent idle"},
	{Name: "PROVEO_IDLE_TARGETS", Scope: Maintainer, Default: "claudecode",
		Doc: "List the defs the e2e idle test runs, comma-separated"},
	{Name: "PROVEO_IDLE_TEST", Scope: Maintainer, Default: "off",
		Doc: "Run the e2e idle-survival test"},
	{Name: "PROVEO_LADDER_TARGET", Scope: Maintainer, Default: "claudecode",
		Doc: "Name the def the e2e sandbox ladder climbs"},
	{Name: "PROVEO_LADDER_TEST", Scope: Maintainer, Default: "off",
		Doc: "Run the e2e sandbox ladder"},
	{Name: "PROVEO_PLATFORMS", Scope: Maintainer, Default: "linux/amd64,linux/arm64",
		Doc: "Set buildx platforms; a local --load keeps only the host's"},
	{Name: "PROVEO_SBX_AGENT_KIT", Scope: Maintainer, Default: "on",
		Doc: "Run a def with no built-in sbx agent as its own kit agent; 0 uses sbx's shell"},
	{Name: "PROVEO_SCHEDULE_NOW", Scope: Maintainer, Default: "the wall clock",
		Doc: "Pin the scheduler's clock to an RFC 3339 time"},
	{Name: "PROVEO_SEED_DONE_MARKER", Scope: Maintainer, Default: "/dev/shm/proveo-seed-done",
		Doc: "Set the file whose presence makes a provision seed exit early"},
	{Name: "PROVEO_SUBAGENTS_DIR", Scope: Maintainer, Default: "/opt/proveo/subagents",
		Doc: "Set the subagent defs directory the image's subagent renderer reads"},
	{Name: "PROVEO_TEST_GO_TIMEOUT", Scope: Maintainer, Default: "45m for coverage e2e; 150m for e2e-full",
		Doc: "Set the go test timeout of the e2e lanes"},
	{Name: "PROVEO_TOOLCHAIN_TEST", Scope: Maintainer, Default: "off",
		Doc: "Run the e2e language toolchain tests"},
	{Name: "PROVEO_TRACE_STDIN", Scope: Maintainer, Default: "no trace",
		Doc: "Append every byte sent to the agent's stdin to this file"},
	{Name: "PROVEO_VERSION", Scope: Maintainer, Default: "dist metadata, else git describe, else dev",
		Doc: "Set the release version stage-cdn stamps into binaries and latest.json"},

	{Name: "PROVEO_AGENT_KIND", Scope: Internal, Default: "a coding agent",
		Doc: "Mark an assistant def so the seed skips toolchain and dependency provisioning"},
	{Name: "PROVEO_CHROME_BRIDGE", Scope: Internal, Default: "the bridge has nothing to relay",
		Doc: "Hand the in-sandbox Chrome bridge the host relay's host:port"},
	{Name: "PROVEO_CHROME_BRIDGE_TOKEN", Scope: Internal, Default: "the bridge refuses to relay",
		Doc: "Hand the in-sandbox Chrome bridge its per-run handshake token"},
	{Name: "PROVEO_CLONE_ENV", Scope: Internal, Default: "no .env linked",
		Doc: "Name the staged host .env the seed links into a clone workspace"},
	{Name: "PROVEO_CLONE_LINKS", Scope: Internal, Default: "no links recreated",
		Doc: "List rel=target symlinks, |-separated, the seed recreates in a clone"},
	{Name: "PROVEO_CLONE_MAIN", Scope: Internal, Default: "no linked worktree",
		Doc: "Name the clone a linked worktree is added to inside the sandbox"},
	{Name: "PROVEO_CLONE_REF", Scope: Internal, Default: "no linked worktree",
		Doc: "Name the ref the seed checks out in a linked worktree's clone"},
	{Name: "PROVEO_CLONE_WORKSPACE", Scope: Internal, Default: "no clone base recorded",
		Doc: "Mark a clone workspace so the seed records the clone's base commit"},
	{Name: "PROVEO_CONFIG_DIRS", Scope: Internal, Default: "no home dirs synced",
		Doc: "List the agent-home config dirs the image syncs with the proveo home"},
	{Name: "PROVEO_CONFIG_FILES", Scope: Internal, Default: "no home files synced",
		Doc: "List the agent-home config files, ;-separated, the image syncs with the proveo home"},
	{Name: "PROVEO_CONFIG_FILES_ROOT", Scope: Internal, Default: "no host file root",
		Doc: "Name the host directory that holds the run's synced agent-home config files"},
	{Name: "PROVEO_HOST_OS", Scope: Internal, Default: "treated as not darwin",
		Doc: "Tell the sandbox the host's OS, as Go's GOOS names it"},
	{Name: "PROVEO_LOCAL_MODEL", Scope: Internal, Default: "no local model",
		Doc: "Carry the --local-model Ollama tag into the sandbox"},
	{Name: "PROVEO_OPERATOR_NAME", Scope: Internal, Default: "the image keeps its house name",
		Doc: "Carry the operator name `proveo init` stored into the sandbox"},
	{Name: "PROVEO_SCHEDULE", Scope: Internal, Default: "a headed run",
		Doc: "Mark an unattended scheduled run: no headed prompts; Ctrl+C reaches the agent"},
	{Name: "PROVEO_SCOPE_REL", Scope: Internal, Default: "git sees the whole repo",
		Doc: "Name the repo-relative sub-scope the image scopes git to"},
	{Name: "PROVEO_SEED_ONLY", Scope: Internal, Default: "the entrypoint launches the agent",
		Doc: "Stop the def entrypoint before it launches the agent"},
	{Name: "PROVEO_SEED_PHASE", Scope: Internal, Default: "all",
		Doc: "Pick the seed phase to run; none skips the shared seed"},
	{Name: "PROVEO_SHARES", Scope: Internal, Default: "no share assertions",
		Doc: "List rel=rw|ro pairs, |-separated, the seed asserts before it releases the agent"},
	{Name: "PROVEO_STATE_HOME", Scope: Internal, Default: "the agent's own home",
		Doc: "Hand the sandbox the host path of the proveo home for config and tool sync"},
	{Name: "PROVEO_WORKDIR", Scope: Internal, Default: "the current directory",
		Doc: "Name the agent's working directory inside the sandbox"},
}

// Lookup returns the Var named name, matching families too.
func Lookup(name string) (Var, bool) {
	for _, v := range Vars {
		if v.Name == name {
			return v, true
		}
	}
	for _, v := range Vars {
		if v.Matches(name) {
			return v, true
		}
	}
	return Var{}, false
}

// Family reports whether v names a family through one <PLACEHOLDER>.
func (v Var) Family() bool {
	_, _, ok := v.split()
	return ok
}

// Matches reports whether name is v's name or a member of v's family.
func (v Var) Matches(name string) bool {
	if name == v.Name {
		return true
	}
	prefix, suffix, ok := v.split()
	if !ok || len(name) <= len(prefix)+len(suffix) ||
		!strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	return nameChars(name[len(prefix) : len(name)-len(suffix)])
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

func (v Var) split() (prefix, suffix string, ok bool) {
	open := strings.IndexByte(v.Name, '<')
	if open < 0 {
		return "", "", false
	}
	end := strings.IndexByte(v.Name[open:], '>')
	if end < 0 {
		return "", "", false
	}
	return v.Name[:open], v.Name[open+end+1:], true
}

func nameChars(s string) bool {
	for _, c := range s {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return s != ""
}
