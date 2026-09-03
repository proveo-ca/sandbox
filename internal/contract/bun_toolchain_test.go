// SPEC: _spec/_runtimes/toolchain-provisioning.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Bun is part of the TypeScript floor (defs/base-node) the way node and pnpm are:
// one of each shipped, pinned, and every Node consumer inherits it. The pin is by
// version AND by the digests the release publishes, for both architectures the
// fleet builds.
func TestBaseNodeShipsAPinnedBunBesideNodeAndPnpm(t *testing.T) {
	t.Parallel()
	df := dockerfileBody(t, imageDockerfiles["proveo/base-node"])
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^ARG BUN_VERSION=\d+\.\d+\.\d+$`),
		regexp.MustCompile(`(?m)^ARG BUN_SHA256_X64=[0-9a-f]{64}$`),
		regexp.MustCompile(`(?m)^ARG BUN_SHA256_AARCH64=[0-9a-f]{64}$`),
		regexp.MustCompile(`sha256sum -c`),
		regexp.MustCompile(`ln -sfn bun /usr/local/bin/bunx`),
		regexp.MustCompile(`test "\$\(bun --version\)" = "\$\{BUN_VERSION\}"`),
		regexp.MustCompile(`apt-get install -y --no-install-recommends nodejs unzip`),
	} {
		if !want.MatchString(df) {
			t.Errorf("defs/base-node/Dockerfile lacks %s", want)
		}
	}
	for _, banned := range []string{"npm install -g bun", "bun.sh/install", "bun.com/install"} {
		if strings.Contains(df, banned) && !strings.Contains(df, "not `npm install -g bun`") {
			t.Errorf("bun comes from the pinned release zip, not %q", banned)
		}
	}
	ensure, err := os.ReadFile(filepath.Join(repoRoot(t), "defs/base-node/ensure.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"command -v bun", "command -v bunx"} {
		if !strings.Contains(string(ensure), want) {
			t.Errorf("base-node ensure.sh floor probe must check %q — a stale :local without bun looks present", want)
		}
	}
}

// The seed's dependency step already chooses `bun install --frozen-lockfile` for
// a Bun lockfile; that row and the shipped binary are one feature.
func TestSeedChoosesBunInstallForABunLockfile(t *testing.T) {
	t.Parallel()
	lib := entrypointLib(t)
	start := strings.Index(lib, "_dep_install_cmd() {")
	if start < 0 {
		t.Fatal("_dep_install_cmd not found in entrypoint-lib.sh")
	}
	body := lib[start:]
	if end := strings.Index(body, "esac; return 0; }"); end >= 0 {
		body = body[:end]
	}
	if !strings.Contains(body, `"$d/bun.lockb" || -f "$d/bun.lock"`) || !strings.Contains(body, "bun install --frozen-lockfile") {
		t.Errorf("_dep_install_cmd must map bun.lock/bun.lockb to a frozen bun install:\n%s", body)
	}
}

// bunHarness drives ensure_node_toolchain with FAKE tools on PATH — bun reports a
// chosen version, corepack and mise only log what they were asked — so the pin
// logic is observable without a registry or an image. node is the real one: the
// helper reads package.json through it.
type bunHarness struct {
	t    *testing.T
	bash string
	ws   string
	bin  string
	log  string
}

func newBunHarness(t *testing.T, bunVersion string) *bunHarness {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; ensure_node_toolchain reads package.json through it")
	}
	h := &bunHarness{t: t, bash: bashOrSkip(t), ws: t.TempDir(), bin: t.TempDir()}
	h.log = filepath.Join(t.TempDir(), "calls.log")
	if err := os.Symlink(node, filepath.Join(h.bin, "node")); err != nil {
		t.Fatal(err)
	}
	logging := "#!/usr/bin/env bash\nprintf '%s %s\\n' \"$(basename \"$0\")\" \"$*\" >> \"$PROVEO_TEST_CALLS\"\n"
	for _, tool := range []string{"corepack", "mise", "pnpm"} {
		if err := os.WriteFile(filepath.Join(h.bin, tool), []byte(logging), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bun := "#!/usr/bin/env bash\n[[ \"$1\" == --version ]] && { echo " + bunVersion + "; exit 0; }\n" + logging
	if err := os.WriteFile(filepath.Join(h.bin, "bun"), []byte(bun), 0o755); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *bunHarness) write(rel, body string) {
	h.t.Helper()
	p := filepath.Join(h.ws, rel)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *bunHarness) run() (out string, calls []string) {
	h.t.Helper()
	script := `source "$1/packages/lib/entrypoint-lib.sh"
export PATH="$2:/usr/bin:/bin"
export PROVEO_TEST_CALLS="$3"
export HOME="$4"
ensure_node_toolchain "$4"
echo DONE`
	cmd := exec.Command(h.bash, "-c", script, "bash", repoRoot(h.t), h.bin, h.log, h.ws)
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN=", "GH_TOKEN=")
	b, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(b), "DONE") {
		h.t.Fatalf("ensure_node_toolchain did not run to completion: %v\n%s", err, b)
	}
	raw, rerr := os.ReadFile(h.log)
	if rerr == nil {
		calls = strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(calls) == 1 && calls[0] == "" {
			calls = nil
		}
	}
	return string(b), calls
}

func TestBunPinIsMisesJobNotCorepacks(t *testing.T) {
	t.Parallel()
	h := newBunHarness(t, "1.4.0")
	h.write("package.json", `{"name":"m","packageManager":"bun@1.3.9"}`)
	out, calls := h.run()
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "mise use -g bun@1.3.9") {
		t.Errorf("an exact bun pin the floor does not match must go to mise; calls=%q\n%s", calls, out)
	}
	if strings.Contains(joined, "corepack") {
		t.Errorf("corepack does not manage bun and must not be asked to; calls=%q", calls)
	}
}

func TestBunRangeThatTheFloorSatisfiesInstallsNothing(t *testing.T) {
	t.Parallel()
	h := newBunHarness(t, "1.4.0")
	h.write("package.json", `{"name":"m","engines":{"bun":"^1.2.0"}}`)
	h.write("bun.lock", "{}")
	_, calls := h.run()
	if strings.Contains(strings.Join(calls, "\n"), "mise") {
		t.Errorf("a satisfied range must not re-provision; calls=%q", calls)
	}
}

func TestBunVersionFileAndRangeReachMiseWithAUsableSpec(t *testing.T) {
	t.Parallel()
	t.Run(".bun-version is an exact pin", func(t *testing.T) {
		t.Parallel()
		h := newBunHarness(t, "1.4.0")
		h.write("package.json", `{"name":"m"}`)
		h.write(".bun-version", "v1.2.22\n")
		_, calls := h.run()
		if !strings.Contains(strings.Join(calls, "\n"), "mise use -g bun@1.2.22") {
			t.Errorf(".bun-version must be honoured exactly; calls=%q", calls)
		}
	})
	t.Run("an unsatisfied range asks mise for its prefix", func(t *testing.T) {
		t.Parallel()
		h := newBunHarness(t, "1.4.0")
		h.write("package.json", `{"name":"m","engines":{"bun":">=2.1"}}`)
		_, calls := h.run()
		if !strings.Contains(strings.Join(calls, "\n"), "mise use -g bun@2.1") {
			t.Errorf("a range reaches mise as its leading version; calls=%q", calls)
		}
	})
}

func TestBunLockfileAloneMeansUseTheFloorsBun(t *testing.T) {
	t.Parallel()
	h := newBunHarness(t, "1.4.0")
	h.write("package.json", `{"name":"m"}`)
	h.write("bun.lock", "{}")
	out, calls := h.run()
	if len(calls) != 0 {
		t.Errorf("no pin, no provisioning; calls=%q\n%s", calls, out)
	}
}
