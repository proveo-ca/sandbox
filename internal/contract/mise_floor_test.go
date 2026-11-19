// SPEC: _spec/_runtimes/toolchain-provisioning.puml (THE MISE FLOOR)
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func miseRequired(t *testing.T, ws string) string {
	t.Helper()
	script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
want="$(_mise_required_version "$2")"
printf 'WANT=%s\n' "$want"`
	out, err := exec.Command(bashOrSkip(t), "-c", script, "bash", repoRoot(t), ws).CombinedOutput()
	if err != nil {
		t.Fatalf("_mise_required_version aborted the seed (%v)\n%s", err, out)
	}
	m := regexp.MustCompile(`(?m)^WANT=(.*)$`).FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("no WANT line\n%s", out)
	}
	return m[1]
}

func TestMiseRequiredVersionReadsEveryMinVersionForm(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"none", nil, ""},
		{"string", map[string]string{"mise.toml": "min_version = \"2026.10.17\"\n"}, "2026.10.17"},
		{"inline table", map[string]string{"mise.toml": "min_version = { hard = \"2026.10.17\", soft = \"2026.11.1\" }\n"}, "2026.10.17"},
		{"soft only warns, never requires", map[string]string{"mise.toml": "min_version = { soft = \"2026.11.1\" }\n"}, ""},
		{"table header", map[string]string{"mise.toml": "[min_version]\nsoft = \"2026.11.1\"\nhard = \"2026.8.0\"\n\n[tools]\ngo = \"1\"\n"}, "2026.8.0"},
		{"dotted key, literal string, v prefix", map[string]string{"mise.toml": "min_version.hard = 'v2026.7.1'\n"}, "2026.7.1"},
		{"a key inside another table", map[string]string{"mise.toml": "[tools]\nmin_version = \"9999.1.1\"\n"}, ""},
		{"a comment", map[string]string{"mise.toml": "# min_version = \"9999.1.1\"\n"}, ""},
		{"highest across configs", map[string]string{
			"mise.toml":         "min_version = \"2026.10.17\"\n",
			".mise.local.toml":  "min_version = \"2026.11.2\" # local\n",
			"mise/config.toml":  "min_version = \"2026.9.30\"\n",
			"unrelated/x.toml":  "min_version = \"9999.1.1\"\n",
			".config/mise.toml": "min_version = \"9999.1.1\"\n",
		}, "2026.11.2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ws := t.TempDir()
			for rel, body := range c.files {
				p := filepath.Join(ws, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := miseRequired(t, ws); got != c.want {
				t.Errorf("_mise_required_version = %q, want %q", got, c.want)
			}
		})
	}
}

var baseMisePin = regexp.MustCompile(`(?m)^ARG MISE_VERSION=v(\S+)$`)

// The base image's mise must read this repo's own mise.toml without a download.
func TestBaseImageMiseSatisfiesTheRepoMinVersion(t *testing.T) {
	t.Parallel()
	want := miseRequired(t, repoRoot(t))
	if want == "" {
		t.Skip("mise.toml declares no hard min_version")
	}
	m := baseMisePin.FindStringSubmatch(dockerfileBody(t, imageDockerfiles["proveo/base"]))
	if m == nil {
		t.Fatal("defs/base/Dockerfile has no ARG MISE_VERSION=v<x.y.z>")
	}
	script := `source "$1/packages/lib/entrypoint-lib.sh"; _version_at_least "$2" "$3" && echo AT_LEAST`
	out, _ := exec.Command(bashOrSkip(t), "-c", script, "bash", repoRoot(t), m[1], want).CombinedOutput()
	if !strings.Contains(string(out), "AT_LEAST") {
		t.Errorf("proveo/base bakes mise %s, but mise.toml requires min_version %s: every sandbox "+
			"of this repo downloads mise at seed time — bump ARG MISE_VERSION and its sha256s", m[1], want)
	}
}

type miseFloor struct {
	t                    *testing.T
	ws, image, tool, log string
	env                  []string
}

const fakeInstaller = `printf 'installer MISE_VERSION=%s\n' "$MISE_VERSION" >> "$PROVEO_TEST_CALLS"
mkdir -p "$(dirname "$MISE_INSTALL_PATH")"
{
  printf '#!/usr/bin/env bash\n'
  printf '[[ "$1" == --version ]] && { echo "%s linux-x64 (test)"; exit 0; }\n' "$MISE_VERSION"
  printf 'printf "new-mise %%s\\n" "$*" >> "$PROVEO_TEST_CALLS"\n'
} > "$MISE_INSTALL_PATH"
chmod +x "$MISE_INSTALL_PATH"
`

func newMiseFloor(t *testing.T, imageMise, minVersion string, curlWorks bool) *miseFloor {
	t.Helper()
	f := &miseFloor{t: t, ws: t.TempDir(), image: t.TempDir(), tool: t.TempDir()}
	f.log = filepath.Join(t.TempDir(), "calls.log")
	installer := filepath.Join(t.TempDir(), "install.sh")
	f.writeExec(installer, fakeInstaller)
	f.writeExec(filepath.Join(f.image, "mise"), `#!/usr/bin/env bash
[[ "$1" == --version ]] && { echo "`+imageMise+` linux-x64 (2026-09-02)"; exit 0; }
printf 'image-mise %s\n' "$*" >> "$PROVEO_TEST_CALLS"
`)
	curl := "#!/usr/bin/env bash\nprintf 'curl %s\\n' \"$*\" >> \"$PROVEO_TEST_CALLS\"\nexit 7\n"
	if curlWorks {
		curl = `#!/usr/bin/env bash
printf 'curl %s\n' "$*" >> "$PROVEO_TEST_CALLS"
while (($#)); do [[ "$1" == -o ]] && { cp "$PROVEO_TEST_INSTALLER" "$2"; exit 0; }; shift; done
exit 1
`
	}
	f.writeExec(filepath.Join(f.image, "curl"), curl)
	if minVersion != "" {
		if err := os.WriteFile(filepath.Join(f.ws, "mise.toml"), []byte("min_version = \""+minVersion+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.env = append(seedFixtureEnv(t), "GITHUB_TOKEN=", "GH_TOKEN=", "PROVEO_TEST_CALLS="+f.log,
		"PROVEO_TEST_INSTALLER="+installer, "_PROVEO_TOOL_HOME="+f.tool, "HOME="+t.TempDir(),
		"PROVEO_AUTO_INSTALL_TOOLS=", "PROVEO_MISE_TRUST=")
	return f
}

func (f *miseFloor) writeExec(path, body string) {
	f.t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *miseFloor) run(call string, env ...string) (out string, calls []string) {
	f.t.Helper()
	script := `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
export PATH="$2:/usr/bin:/bin"
cd "$3"
` + call + `
printf 'MISE=%s\n' "$(command -v mise)"
echo DONE`
	cmd := exec.Command(bashOrSkip(f.t), "-c", script, "bash", repoRoot(f.t), f.image, f.ws)
	cmd.Env = append(f.env, env...)
	b, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(b), "DONE") {
		f.t.Fatalf("%s did not run to completion: %v\n%s", call, err, b)
	}
	if raw, err := os.ReadFile(f.log); err == nil && strings.TrimSpace(string(raw)) != "" {
		calls = strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
	return string(b), calls
}

func TestMiseFloorInstallsTheRequiredMiseAndRetargetsTheShims(t *testing.T) {
	t.Parallel()
	f := newMiseFloor(t, "2026.9.1", "2026.10.17", true)
	out, calls := f.run(`_proveo_ensure_mise_floor "$PWD"`)
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "installer MISE_VERSION=2026.10.17") {
		t.Errorf("the floor must install exactly the min_version mise.toml requires; calls=%q\n%s", calls, out)
	}
	if !strings.Contains(joined, "new-mise reshim --force") {
		t.Errorf("shims are symlinks to the mise that made them; the new mise must rebuild them; calls=%q", calls)
	}
	if want := "MISE=" + filepath.Join(f.tool, ".local", "bin", "mise"); !strings.Contains(out, want) {
		t.Errorf("the installed mise must come first on PATH (%s)\n%s", want, out)
	}
}

func TestMiseTrustRunsOnTheFloorsMise(t *testing.T) {
	t.Parallel()
	f := newMiseFloor(t, "2026.9.1", "2026.10.17", true)
	_, calls := f.run(`proveo_trust_workspace_mise`, "PROVEO_MISE_TRUST=on")
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "new-mise trust --yes "+filepath.Join(f.ws, "mise.toml")) {
		t.Errorf("mise trust must run on a mise that can parse mise.toml; calls=%q", calls)
	}
	if strings.Contains(joined, "image-mise trust") {
		t.Errorf("the image's mise %s refuses min_version 2026.10.17 and must not be asked to trust it; calls=%q", "2026.9.1", calls)
	}
}

func TestMiseFloorLeavesASatisfyingMiseAlone(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, image, min string }{
		{"newer image mise", "2026.11.8", "2026.10.17"},
		{"equal", "2026.10.17", "2026.10.17"},
		{"no min_version", "2026.9.1", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newMiseFloor(t, c.image, c.min, true)
			out, calls := f.run(`_proveo_ensure_mise_floor "$PWD"`)
			if len(calls) != 0 {
				t.Errorf("a satisfied floor installs nothing; calls=%q\n%s", calls, out)
			}
			if !strings.Contains(out, "MISE="+filepath.Join(f.image, "mise")) {
				t.Errorf("the image's mise stays first on PATH\n%s", out)
			}
		})
	}
}

func TestMiseFloorNamesTheGapWhenItCannotInstall(t *testing.T) {
	t.Parallel()
	t.Run("offline", func(t *testing.T) {
		t.Parallel()
		f := newMiseFloor(t, "2026.9.1", "2026.10.17", false)
		out, _ := f.run(`_proveo_ensure_mise_floor "$PWD"`)
		if !strings.Contains(out, "mise 2026.10.17 did not install") {
			t.Errorf("a failed install must say which mise the workspace needs\n%s", out)
		}
	})
	t.Run("auto-install off", func(t *testing.T) {
		t.Parallel()
		f := newMiseFloor(t, "2026.9.1", "2026.10.17", true)
		out, calls := f.run(`_proveo_ensure_mise_floor "$PWD"`, "PROVEO_AUTO_INSTALL_TOOLS=off")
		if len(calls) != 0 || !strings.Contains(out, "older than the workspace's min_version 2026.10.17") {
			t.Errorf("PROVEO_AUTO_INSTALL_TOOLS=off installs nothing and names the gap; calls=%q\n%s", calls, out)
		}
	})
}
