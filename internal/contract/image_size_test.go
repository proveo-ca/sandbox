// SPEC: _spec/_plans/image-size-reduction.puml, _spec/_devops/image-lineage-and-publish.puml
package contract_test

import (
	"regexp"
	"strings"
	"testing"
)

// `playwright install chromium` lands TWO browsers — `chromium-<rev>` and
// `chromium_headless_shell-<rev>` — and this layer launches only the first: the
// revision-stable symlink's own `find … -path '*/chromium-*/*'` uses a hyphen so it
// cannot match the shell. Measured at 1.61.0, the shell was 334 MB of a 1.33 GB
// layer, installed and then excluded by hand, in all three browser variants.
//
// The flag is the fix and the guard is why the flag can be trusted: a Playwright
// that stops honouring `--no-shell` has to fail the build rather than quietly put
// the 334 MB back.
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

// `default-jre-headless` was the largest single package in proveo/base (dpkg
// Installed-Size 198,806 kB) and its only consumer is /opt/plantuml.jar. Since it
// lives in the root of the lineage, every one of the published images paid for it.
//
// The replacement is a jlink runtime built in a throwaway stage: 72 MB measured,
// rendering -tpng and -tsvg identically. This test pins the three things that make
// that swap survivable rather than the size itself.
func TestBaseRunsPlantUMLOnAJlinkedRuntime(t *testing.T) {
	t.Parallel()
	df := readRepoFile(t, "defs/base/Dockerfile")

	if installedPackages(dockerfileBody(t, "defs/base/Dockerfile"))["default-jre-headless"] {
		t.Error("proveo/base must not apt-install default-jre-headless (194 MB in every " +
			"descendant); the jre-builder stage provides a 72 MB runtime for plantuml.jar")
	}
	for _, want := range []string{
		"FROM debian:trixie-slim AS jre-builder",
		"jlink --add-modules",
		"COPY --from=jre-builder /opt/jre /opt/jre",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("proveo/base lacks %q — the JRE arrives from a builder stage, not from apt", want)
		}
	}

	// jdtls, not plantuml, sets the floor for the module list. _install_jdtls
	// provisions its own Java only when `_java_major` reports < 21, and this runtime
	// reports 21 — so it must be able to host an Eclipse/OSGi application. A prune to
	// plantuml's own needs (59 MB) satisfies the gate and then fails jdtls at runtime.
	for _, mod := range []string{"java.desktop", "java.compiler", "java.instrument", "jdk.attach", "jdk.jdi", "jdk.zipfs"} {
		if !strings.Contains(df, mod) {
			t.Errorf("jlink module %q missing: plantuml needs java.desktop, and jdtls needs the rest "+
				"because it runs on the floor's java whenever _java_major says 21", mod)
		}
	}

	// Debian's libfontmanager.so links against the SYSTEM harfbuzz/freetype, which
	// openjdk-*-jre-headless used to pull in. Without them PlantUML dies in
	// SunFontManager's static initialiser before drawing anything.
	pkgs := installedPackages(dockerfileBody(t, "defs/base/Dockerfile"))
	for _, pkg := range []string{"fontconfig", "fonts-dejavu-core", "libharfbuzz0b"} {
		if !pkgs[pkg] {
			t.Errorf("proveo/base must install %q — it came free with the distro JRE and does not "+
				"come free with a jlink runtime", pkg)
		}
	}

	if !strings.Contains(df, "/opt/jre/bin/java -Djava.awt.headless=true -jar /opt/plantuml.jar") {
		t.Error("the plantuml shim must exec the jlink runtime by absolute path")
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

// Every def builds from the REPO ROOT, because variant Dockerfiles resolve their
// COPY paths from there. Unfiltered, that context is 303 MB — 201 MB of node_modules
// and 83 MB of apps/ — hashed and shipped to the daemon on every image build, and
// `COPY . .` baked it into the base's builder layer while invalidating the compile
// on any file in the tree.
//
// defs/cecli/.dockerignore does NOT do this job: BuildKit reads
// <context>/.dockerignore or <dockerfile>.dockerignore, and that path is neither.
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

// The denylist is deliberately wide, which makes the opposite failure the one worth
// pinning: a path some Dockerfile COPYs must never appear in it. A COPY reaching
// nothing fails loudly, but a COPY reaching an EMPTY directory succeeds and ships an
// image missing the thing it was supposed to carry.
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

// The docker static tarball left every harness image: all eight of its binaries
// (docker, dockerd, containerd, containerd-shim-runc-v2, ctr, runc, docker-proxy,
// docker-init — 210 MB measured per image) overlap the sbx sandbox runtime that
// owns the daemon, and sbx is the only backend a harness has.
//
// This test is a ratchet against a re-add, because a re-add is cheap to do by
// copy-paste and expensive to notice: the four blocks were already four copies of
// one recipe, each with its own hard-coded version and a comment asking humans to
// keep them in sync.
// SPEC: _spec/_plans/image-size-reduction.puml
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
				t.Errorf("%s contains %q — the docker binaries were removed because they "+
					"overlap sbx, which owns the daemon it starts from the start-docker label",
					rel, banned)
			}
		}
	}
}

// The three things that stayed are not overlaps, and each has a distinct reason.
// Removing any of them with the binaries would have broken something the binaries
// were not responsible for, so they are pinned separately from the removal above.
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
		// A NAT chain needs iptables whoever builds it.
		if !installedPackages(dockerfileBody(t, rel))["iptables"] {
			t.Errorf("%s must keep iptables — the per-sandbox daemon builds its own NAT chain", rel)
		}
	}
}

// The solidity variant's two giants were also its two reproducibility holes.
// `curl … | bash && foundryup` with no argument installs the NIGHTLY — foundry's
// release feed is nightlies with a stable tag every few weeks — so the audit
// toolchain changed under the image on every rebuild; and semgrep went unpinned
// into Debian's dist-packages behind --break-system-packages.
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

	// anvil is a devnet and chisel a REPL: a sandbox has no chain and no operator
	// at a prompt. foundryup has no component selector, so they must be removed in
	// the SAME RUN that installs them — a later RUN drops the paths, not the bytes.
	// By NAME, not by path. foundryup puts the real binaries under
	// versions/foundry-rs/foundry/<tag>/ and only symlinks them into bin/, so
	// deleting the bin entries hid the commands and left 100 MB in the image
	// (measured: anvil 49 MB, chisel 51 MB, still present after the first attempt).
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

// No proveo image ships a C compiler — cecli was the last to carry one, for its
// own venv. That makes one failure mode dominant for workspace Python projects,
// and the seed has to name it: a dependency with no wheel for this arch cannot
// build from source, and the generic "environment may be partial" reads like a
// transient network problem.
//
// The two halves are asserted together on purpose. If a compiler ever comes back,
// this test fails and the message has to be revisited rather than left lying.
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

// instructionsOnly drops comment lines. A contract about what a layer DOES must
// not match what a comment SAYS: the solidity prose names the flag it removed,
// and a raw grep read that as the flag still being there.
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
