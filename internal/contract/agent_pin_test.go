// SPEC: _spec/_devops/agent-version-pin.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// agentPins is the table: every harness image that bakes an agent, and how it
// pins it — a build-arg the Dockerfile USES, a version check inside the same
// RUN, and the label that records what landed. The ARG is declared BARE.
// SPEC: _spec/_devops/agent-version-pin.puml
var agentPins = []struct {
	image       string // key into imageDockerfiles
	buildScript string
	arg         string         // build-arg name; also the maintainer override env var
	pkg         string         // proveo.agent label value
	ecosystem   string         // proveo_agent_version ecosystem
	install     *regexp.Regexp // the pinned install, using the arg
	banned      []string       // spellings that would reintroduce @latest
}{
	{
		image: "proveo/opencode", buildScript: "defs/opencode/build.sh",
		arg: "OPENCODE_VERSION", pkg: "opencode-ai", ecosystem: "npm",
		install: regexp.MustCompile(`npm install -g "opencode-ai@\$\{OPENCODE_VERSION\}"`),
		banned:  []string{"opencode-ai@latest", "npm install -g opencode-ai "},
	},
	{
		image: "proveo/claudecode", buildScript: "defs/claudecode/build.sh",
		arg: "CLAUDE_CODE_VERSION", pkg: "@anthropic-ai/claude-code", ecosystem: "npm",
		install: regexp.MustCompile(`npm install -g "@anthropic-ai/claude-code@\$\{CLAUDE_CODE_VERSION\}"`),
		banned:  []string{"claude-code@latest", "npm install -g @anthropic-ai/claude-code "},
	},
	{
		image: "proveo/cecli", buildScript: "defs/cecli/build.sh",
		arg: "CECLI_VERSION", pkg: "cecli-dev", ecosystem: "pypi",
		install: regexp.MustCompile(`pip install "cecli-dev==\$\{CECLI_VERSION\}"`),
		banned:  []string{"pip install cecli-dev "},
	},
	{
		// cursor.com/install takes no version, so the pin is a verification: the
		// arg names the release build.sh read out of the script, and the RUN
		// checks the installer unpacked exactly that one.
		image: "proveo/cursor", buildScript: "defs/cursor/build.sh",
		arg: "CURSOR_AGENT_VERSION", pkg: "cursor-agent", ecosystem: "cursor",
		install: regexp.MustCompile(`test -d "/opt/cursor-dist/\.local/share/cursor-agent/versions/\$\{CURSOR_AGENT_VERSION\}"`),
	},
}

func TestEveryHarnessPinsItsAgentByABuildArgItUses(t *testing.T) {
	t.Parallel()
	for _, p := range agentPins {
		t.Run(p.image, func(t *testing.T) {
			t.Parallel()
			rel, ok := imageDockerfiles[p.image]
			if !ok {
				t.Fatalf("%s is not in imageDockerfiles", p.image)
			}
			df := dockerfileBody(t, rel)
			for _, want := range []*regexp.Regexp{
				// Bare: no default. A default is @latest with a different spelling.
				regexp.MustCompile(`(?m)^ARG ` + p.arg + `$`),
				regexp.MustCompile(`test -n "\$\{` + p.arg + `\}"`),
				p.install,
				regexp.MustCompile(`proveo\.agent="` + regexp.QuoteMeta(p.pkg) + `"`),
				regexp.MustCompile(`proveo\.agent\.version="\$\{` + p.arg + `\}"`),
				regexp.MustCompile(`SPEC: _spec/_devops/agent-version-pin\.puml`),
			} {
				if !want.MatchString(df) {
					t.Errorf("%s lacks %s", rel, want)
				}
			}
			for _, b := range p.banned {
				if strings.Contains(df, b) {
					t.Errorf("%s still installs the agent unpinned (%q) — the cache key never moves", rel, b)
				}
			}

			sh, err := os.ReadFile(filepath.Join(repoRoot(t), p.buildScript))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"proveo_agent_version " + p.arg + " " + p.ecosystem + " ",
				`--build-arg ` + p.arg + `="$` + p.arg + `"`,
			} {
				if !strings.Contains(string(sh), want) {
					t.Errorf("%s lacks %q — the Dockerfile requires the arg, so a build without it fails", p.buildScript, want)
				}
			}
		})
	}
}

// pinHarness runs proveo_agent_version with FAKE npm and curl on PATH, so the
// resolver is observable without a registry: npm answers what NPM_VIEW_OUTPUT
// says (exit 1 when empty), curl prints CURL_BODY (exit 22 when FAKE_CURL_FAIL
// is set). jq/python3 stay real for the JSON hop.
func pinHarness(t *testing.T) (run func(env map[string]string, args ...string) (string, string, error)) {
	t.Helper()
	bin := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("npm", `[[ -n "${NPM_VIEW_OUTPUT:-}" ]] || exit 1
printf '%s\n' "$NPM_VIEW_OUTPUT"
`)
	write("curl", `[[ -z "${FAKE_CURL_FAIL:-}" ]] || exit 22
printf '%s' "${CURL_BODY:-}"
`)
	lib := filepath.Join(repoRoot(t), "defs", "lib", "docker-build.sh")
	return func(env map[string]string, args ...string) (string, string, error) {
		script := "source '" + lib + "' && proveo_agent_version " + strings.Join(args, " ")
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		var out, errb strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.String(), errb.String(), err
	}
}

func TestAgentVersionResolverIsUniformAcrossEcosystems(t *testing.T) {
	t.Parallel()
	run := pinHarness(t)
	// wantNote is the 📌 line on stderr: the version AND how it was chosen.
	// SPEC: _spec/_devops/agent-version-pin.puml
	cases := []struct {
		name     string
		env      map[string]string
		args     []string
		want     string
		wantNote string
	}{
		{name: "npm dist-tag", env: map[string]string{"NPM_VIEW_OUTPUT": "1.18.26"},
			args: []string{"OPENCODE_VERSION", "npm", "opencode-ai"}, want: "1.18.26",
			wantNote: "📌 opencode-ai@1.18.26 (resolved upstream; override with OPENCODE_VERSION=<version>)"},
		{name: "npm falls back to the registry when npm is unusable",
			env:  map[string]string{"CURL_BODY": `{"name":"opencode-ai","version":"1.18.27"}`},
			args: []string{"OPENCODE_VERSION", "npm", "opencode-ai"}, want: "1.18.27",
			wantNote: "📌 opencode-ai@1.18.27 (resolved upstream"},
		{name: "pypi current release", env: map[string]string{"CURL_BODY": `{"info":{"name":"cecli-dev","version":"1.4.0"},"releases":{"1.3.0":[]}}`},
			args: []string{"CECLI_VERSION", "pypi", "cecli-dev"}, want: "1.4.0",
			wantNote: "📌 cecli-dev@1.4.0 (resolved upstream; override with CECLI_VERSION=<version>)"},
		{name: "cursor reads the release out of the installer",
			env:  map[string]string{"CURL_BODY": "FINAL_DIR=\"$HOME/.local/share/cursor-agent/versions/2026.08.31-4057e58\"\nln -s ~/.local/share/cursor-agent/versions/2026.08.31-4057e58/cursor-agent ~/.local/bin/agent\n"},
			args: []string{"CURSOR_AGENT_VERSION", "cursor", "https://cursor.com/install"}, want: "2026.08.31-4057e58",
			wantNote: "@2026.08.31-4057e58 (resolved upstream; override with CURSOR_AGENT_VERSION=<version>)"},
		{name: "an exported override wins without asking upstream",
			env:  map[string]string{"OPENCODE_VERSION": "1.18.20", "FAKE_CURL_FAIL": "1"},
			args: []string{"OPENCODE_VERSION", "npm", "opencode-ai"}, want: "1.18.20",
			wantNote: "📌 opencode-ai@1.18.20 (from OPENCODE_VERSION)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, stderr, err := run(tc.env, tc.args...)
			if err != nil {
				t.Fatalf("proveo_agent_version %v: %v\n%s", tc.args, err, stderr)
			}
			if got != tc.want {
				t.Errorf("proveo_agent_version %v = %q, want %q", tc.args, got, tc.want)
			}
			if !strings.Contains(stderr, tc.wantNote) {
				t.Errorf("proveo_agent_version %v stderr lacks %q:\n%s", tc.args, tc.wantNote, stderr)
			}
		})
	}
}

// Unresolvable is a refusal that names the way out, never a silent `latest`: a
// fallback to the dist-tag would put the hole this exists to close back in the
// one place — offline, behind a proxy — where nobody is looking.
func TestAgentVersionResolverRefusesRatherThanGuessing(t *testing.T) {
	t.Parallel()
	run := pinHarness(t)
	got, stderr, err := run(map[string]string{"FAKE_CURL_FAIL": "1"}, "OPENCODE_VERSION", "npm", "opencode-ai")
	if err == nil {
		t.Fatalf("resolver succeeded with nothing reachable, printed %q", got)
	}
	if got != "" {
		t.Errorf("resolver printed %q on failure — a caller would bake it", got)
	}
	for _, want := range []string{"could not resolve", "OPENCODE_VERSION=<x.y.z>"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("failure message lacks %q:\n%s", want, stderr)
		}
	}
	if _, stderr, err := run(nil, "X_VERSION", "cargo", "x"); err == nil || !strings.Contains(stderr, "unknown ecosystem") {
		t.Errorf("unknown ecosystem accepted (err=%v):\n%s", err, stderr)
	}
}
