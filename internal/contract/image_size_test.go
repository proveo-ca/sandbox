// SPEC: _spec/_plans/image-size-reduction.puml, _spec/_devops/image-lineage-and-publish.puml
package contract_test

import (
	"regexp"
	"strings"
	"testing"
)

func TestBrowserLayerInstallsOneChromiumNotTwo(t *testing.T) {
	t.Parallel()
	df := readRepoFile(t, "defs/base-node-browser/Dockerfile")

	if !strings.Contains(df, "playwright install --with-deps --no-shell chromium") {
		t.Error("the browser layer must install chromium with --no-shell; " +
			"without it Playwright also downloads chromium_headless_shell, which nothing here launches")
	}
	if !strings.Contains(df, `! ls -d "$PLAYWRIGHT_BROWSERS_PATH"/chromium_headless_shell-*`) {
		t.Error("the install must assert the headless shell is absent in the SAME RUN — " +
			"a flag upstream stops honouring would otherwise re-add 334 MB silently")
	}
	// A `rm` in a LATER RUN removes the path and keeps the bytes; overlay layers are
	// additive. If someone swaps the flag for a delete, it has to be in this RUN.
	if m := regexp.MustCompile(`(?m)^RUN.*rm -rf.*chromium_headless_shell`).FindString(df); m != "" {
		if !strings.Contains(df, "playwright install --with-deps --no-shell") {
			t.Error("deleting the headless shell in its own RUN saves nothing — use --no-shell")
		}
	}
}

func TestBaseRunsPlantUMLOnTheTemplateJDK(t *testing.T) {
	t.Parallel()
	df := readRepoFile(t, "defs/base/Dockerfile")

	if installedPackages(dockerfileBody(t, "defs/base/Dockerfile"))["default-jre-headless"] {
		t.Error("proveo/base must not apt-install default-jre-headless (194 MB in every " +
			"descendant); the jre-builder stage provides a 72 MB runtime for plantuml.jar")
	}
	// SPEC: _spec/_devops/sandbox-template-rebase.puml
	if strings.Contains(df, "jlink --add-modules") {
		t.Error("the jlink stage is redundant on a template that ships a full JDK — " +
			"two Java runtimes in one lineage is the cost this test exists to prevent")
	}
	if !strings.Contains(df, "ln -sfn \"${java_home}\" /opt/jre") {
		t.Error("proveo/base must keep /opt/jre resolving to the template's JDK: JAVA_HOME " +
			"and every downstream reference are written against that path, not an arch-specific one")
	}

	pkgs := installedPackages(dockerfileBody(t, "defs/base/Dockerfile"))
	for _, pkg := range []string{"fontconfig", "fonts-dejavu-core", "libharfbuzz0b"} {
		if !pkgs[pkg] {
			t.Errorf("proveo/base must install %q — libfontmanager links the SYSTEM harfbuzz and "+
				"freetype, and the sandbox template ships the JDK without them (verified in the "+
				"registry: no libharfbuzz.so, no fontconfig in any template layer)", pkg)
		}
	}

	if !strings.Contains(df, "/opt/jre/bin/java -Djava.awt.headless=true -jar /opt/plantuml.jar") {
		t.Error("the plantuml shim must exec the runtime by absolute path")
	}
	// `java` on PATH is what _install_jdtls probes, and what the build-time render asserts.
	if !strings.Contains(df, `PATH="/opt/jre/bin:${PATH}"`) {
		t.Error("proveo/base must put /opt/jre/bin on PATH — _install_jdtls probes `java -version`")
	}
	if !strings.Contains(df, "plantuml -tpng -o /tmp /tmp/floor.puml") {
		t.Error("the layer that writes the shim must render one diagram: a runtime missing a module " +
			"fails at first use, which is a sandbox an operator is already sitting in")
	}
}

func TestRepoRootDockerignoreFiltersTheBuildContext(t *testing.T) {
	t.Parallel()
	ignored := dockerignoreEntries(t)
	for _, want := range []string{"node_modules", ".git", "apps", ".pnpm-store"} {
		if !ignored[want] {
			t.Errorf(".dockerignore must exclude %q from the repo-root build context", want)
		}
	}

	base := readRepoFile(t, "defs/base/Dockerfile")
	if regexp.MustCompile(`(?m)^COPY \. \.$`).MatchString(base) {
		t.Error("the base builder must copy what it compiles (COPY cmd / COPY internal), not the whole " +
			"context — see defs/sidecars/egress-proxy/Dockerfile for the shape")
	}
	for _, want := range []string{"COPY cmd ./cmd", "COPY internal ./internal"} {
		if !strings.Contains(base, want) {
			t.Errorf("defs/base/Dockerfile lacks %q", want)
		}
	}
}

func TestDockerignoreNeverHidesSomethingADockerfileCopies(t *testing.T) {
	t.Parallel()
	ignored := dockerignoreEntries(t)
	copyRe := regexp.MustCompile(`(?m)^COPY\s+(?:--[^\s]+\s+)*([^\s]+)`)

	// The defs that build from the repo root. base-node-browser and the sidecars use
	// their own directory as context, so this file cannot affect them.
	for _, rel := range []string{
		"defs/base/Dockerfile",
		"defs/opencode/Dockerfile",
		"defs/cursor/Dockerfile",
		"defs/cecli/Dockerfile",
		"defs/claudecode/mcp/Dockerfile",
	} {
		for _, m := range copyRe.FindAllStringSubmatch(readRepoFile(t, rel), -1) {
			src := m[1]
			if strings.HasPrefix(src, "/") || strings.HasPrefix(src, "--") {
				continue // a --from=<stage> copy reads the stage, not the context
			}
			top := strings.SplitN(strings.TrimPrefix(src, "./"), "/", 2)[0]
			if ignored[top] {
				t.Errorf("%s copies %q but .dockerignore excludes %q — the COPY would silently "+
					"produce an empty directory", rel, src, top)
			}
		}
	}
}

func dockerignoreEntries(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, line := range strings.Split(readRepoFile(t, ".dockerignore"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[strings.TrimPrefix(line, "**/")] = true
	}
	return out
}

// harnessDockerfiles are the four images that used to install the docker static
// tarball. claudecode-solidity is absent because it inherits claudecode's layers.
var harnessDockerfiles = []string{
	"defs/claudecode/mcp/Dockerfile",
	"defs/cursor/Dockerfile",
	"defs/opencode/Dockerfile",
	"defs/cecli/Dockerfile",
}

// SPEC: _spec/_devops/sandbox-template-rebase.puml,
// _spec/_plans/image-size-reduction.puml
func TestNoHarnessImageShipsDockerBinaries(t *testing.T) {
	t.Parallel()
	for _, rel := range harnessDockerfiles {
		df := readRepoFile(t, rel)
		for _, banned := range []string{
			"DOCKER_VERSION=",
			"download.docker.com",
			"/tmp/docker.tgz",
			"ln -sf /usr/local/bin/dockerd",
		} {
			if strings.Contains(df, banned) {
				t.Errorf("%s contains %q — docker comes from the sandbox-template base now, "+
					"so a hand-rolled tarball is a second copy shadowing the inherited one",
					rel, banned)
			}
		}
	}
}

func TestDockerInSandboxKeepsTheLabelTheGroupAndIptables(t *testing.T) {
	t.Parallel()
	for _, rel := range harnessDockerfiles {
		df := readRepoFile(t, rel)
		// sbx's own switch: without it sbx records `dind: false` at container creation
		// and never starts a daemon at all.
		if !strings.Contains(df, `LABEL com.docker.sandboxes.start-docker="true"`) {
			t.Errorf("%s must keep the start-docker label — it is sbx's switch, not a binary", rel)
		}
		// The socket is created root:docker, so the runtime uid needs the group.
		if !strings.Contains(df, "usermod -aG docker") {
			t.Errorf("%s must keep the docker group — it is how a runtime uid reaches "+
				"a root:docker socket", rel)
		}
	}
	// SPEC: _spec/_devops/sandbox-template-rebase.puml
	if !installedPackages(dockerfileBody(t, "defs/base/Dockerfile"))["iptables"] {
		t.Error("proveo/base must install iptables — the per-sandbox daemon builds its own " +
			"NAT chain, and every harness inherits the base")
	}
	for _, rel := range harnessDockerfiles {
		if installedPackages(dockerfileBody(t, rel))["iptables"] {
			t.Errorf("%s reinstalls iptables, which proveo/base already carries", rel)
		}
	}
}

// SPEC: _spec/_plans/image-size-reduction.puml
func TestSolidityPinsItsToolchainAndShipsOnlyWhatItAudits(t *testing.T) {
	t.Parallel()
	df := readRepoFile(t, "defs/claudecode/solidity/Dockerfile")

	for _, want := range []string{
		`ARG FOUNDRY_VERSION=`,
		`--install "${FOUNDRY_VERSION}"`,
		`ARG SEMGREP_VERSION=`,
		`ARG SOLC_SELECT_VERSION=`,
		`ARG SOLHINT_VERSION=`,
	} {
		if !strings.Contains(df, want) {
			t.Errorf("defs/claudecode/solidity/Dockerfile lacks %q — an unpinned install lets a "+
				"warm cache serve a toolchain nothing in the build names", want)
		}
	}

	if !strings.Contains(df, `find "/home/${USER_NAME}/.foundry" \( -name anvil -o -name chisel \) -delete`) {
		t.Error("solidity must delete anvil and chisel BY NAME under ~/.foundry — " +
			"removing the bin/ symlinks alone leaves the binaries behind")
	}

	if strings.Contains(instructionsOnly(df), "--break-system-packages") {
		t.Error("solidity must not pip-install into dist-packages; /opt/security is its venv")
	}

	// The foundry bin dir lives under a home the agent can write and sbx mounts
	// volumes into. At the FRONT of PATH it shadows any binary of the same name.
	if strings.Contains(df, `ENV PATH="/home/${USER_NAME}/.foundry/bin:$PATH"`) {
		t.Error("~/.foundry/bin must be APPENDED to PATH — prepending lets a writable, " +
			"mount-target directory shadow git, curl or anything else for every later command")
	}
	if !strings.Contains(df, `ENV PATH="$PATH:/home/${USER_NAME}/.foundry/bin"`) {
		t.Error("solidity must append ~/.foundry/bin to PATH")
	}
}

func TestPythonDepFailureNamesTheMissingToolchain(t *testing.T) {
	t.Parallel()
	lib := readRepoFile(t, "packages/lib/entrypoint-lib.sh")
	for _, want := range []string{
		`command -v cc >/dev/null 2>&1`,
		`No C toolchain in this image`,
	} {
		if !strings.Contains(lib, want) {
			t.Errorf("_py_build_env must name the missing toolchain; %q not found", want)
		}
	}

	for _, rel := range append([]string{"defs/base/Dockerfile", "defs/base-node/Dockerfile",
		"defs/base-node-lsp/Dockerfile"}, harnessDockerfiles...) {
		pkgs := installedPackages(dockerfileBody(t, rel))
		for _, cc := range []string{"build-essential", "gcc", "g++"} {
			if pkgs[cc] {
				t.Errorf("%s installs %q — a compiler is back in the fleet, so the "+
					"\"No C toolchain\" advice in _py_build_env is now wrong", rel, cc)
			}
		}
	}
}

func instructionsOnly(df string) string {
	var out []string
	for _, line := range strings.Split(df, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
