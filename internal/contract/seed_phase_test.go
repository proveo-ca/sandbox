// SPEC: _spec/internal/sbx/startup-dispatch.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var provisionFnRe = regexp.MustCompile(`(?s)\n_proveo_seed_provision\(\) \{\n(.*?)\n\}\n`)

// provisionBody is the seed's post-release half, the part the provision step runs.
func provisionBody(t *testing.T, lib string) string {
	t.Helper()
	m := provisionFnRe.FindStringSubmatch(lib)
	if m == nil {
		t.Fatal("_proveo_seed_provision() not found in entrypoint-lib.sh")
	}
	return m[1]
}

// seedSteps are the functions proveo_seed delegates to; each is stubbed to log its call.
var seedSteps = []string{
	"proveo_clone_checkout", "proveo_clone_env", "proveo_clone_links", "proveo_clone_base",
	"proveo_assert_shares", "proveo_seed_instructions", "seed_github_known_hosts",
	"proveo_sync_state", "proveo_sync_config", "proveo_install_git_sync_hooks", "render_subagents",
	"accept_workspace_trust", "proveo_wire_config", "proveo_compose_house_rules",
	"proveo_address_operator_claude", "proveo_apply_ui_defaults", "proveo_install_claude_hooks",
	"proveo_install_claude_env_hook", "proveo_seed_browser_skills", "proveo_release_agent",
	"proveo_sync_tools", "proveo_provision_toolchain", "proveo_chrome_bridge",
}

// runSeedPhase sources the lib, stubs every step, and runs proveo_seed with args.
func runSeedPhase(t *testing.T, bash string, env []string, args ...string) (calls string, rc string, ready bool) {
	t.Helper()
	home := t.TempDir()
	marker := filepath.Join(t.TempDir(), "toolchain-ready")
	var stubs strings.Builder
	for _, f := range seedSteps {
		stubs.WriteString(f + `() { echo "CALL ` + f + ` $*"; }` + "\n")
	}
	script := `source "$1/packages/lib/entrypoint-lib.sh"
proveo_seed_step() { :; }
` + stubs.String() + `shift
proveo_seed "$@"
echo "rc=$?"`
	cmd := exec.Command(bash, append([]string{"-c", script, "bash", repoRoot(t)}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+home, "PROVEO_HOME=", "PROVEO_SEED_PHASE=", "PROVEO_AGENT_KIND=",
		"PROVEO_TOOLCHAIN_READY="+marker, "PROVEO_HOOKS_MARKER="+filepath.Join(home, "hooks"),
		"PROVEO_PROVISIONING="+filepath.Join(home, "provisioning"))
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proveo_seed %q: %v\n%s", args, err, out)
	}
	s := string(out)
	_, statErr := os.Stat(marker)
	i := strings.LastIndex(s, "rc=")
	if i < 0 {
		t.Fatalf("proveo_seed %q printed no status:\n%s", args, s)
	}
	return s[:i], strings.TrimSpace(s[i+3:]), statErr == nil
}

func TestSeedPhasesSplitAtTheRelease(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	releaseSide := []string{"CALL proveo_sync_config restore", "CALL render_subagents claudecode", "CALL proveo_install_claude_hooks",
		"CALL proveo_chrome_bridge claudecode", "CALL proveo_release_agent claudecode"}
	provisionSide := []string{"CALL proveo_sync_tools restore", "CALL proveo_provision_toolchain"}
	for _, tc := range []struct {
		name      string
		args      []string
		env       []string
		want      []string
		absent    []string
		wantReady bool
	}{
		{"release", []string{"claudecode", "release"}, nil, releaseSide, provisionSide, false},
		{"provision", []string{"claudecode", "provision"}, nil, provisionSide, releaseSide, true},
		{"all", []string{"claudecode"}, nil, append(append([]string{}, releaseSide...), provisionSide...), nil, true},
		{"none", []string{"claudecode", "none"}, nil, nil, append(append([]string{}, releaseSide...), provisionSide...), false},
		{"none from env", []string{"claudecode"}, []string{"PROVEO_SEED_PHASE=none"}, nil, append(append([]string{}, releaseSide...), provisionSide...), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls, rc, ready := runSeedPhase(t, bash, tc.env, tc.args...)
			if rc != "0" {
				t.Errorf("proveo_seed %q rc = %s, want 0\n%s", tc.args, rc, calls)
			}
			for _, w := range tc.want {
				if !strings.Contains(calls, w) {
					t.Errorf("proveo_seed %q never ran %q\n%s", tc.args, w, calls)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(calls, a) {
					t.Errorf("proveo_seed %q ran %q, which belongs to the other phase\n%s", tc.args, a, calls)
				}
			}
			if ready != tc.wantReady {
				t.Errorf("proveo_seed %q toolchain-ready marker = %v, want %v", tc.args, ready, tc.wantReady)
			}
			if tc.name == "all" && strings.Index(calls, "CALL proveo_release_agent") > strings.Index(calls, "CALL proveo_sync_tools restore") {
				t.Errorf("proveo_seed %q restored toolchains before releasing the agent\n%s", tc.args, calls)
			}
			if strings.Contains(calls, "CALL proveo_chrome_bridge") &&
				strings.Index(calls, "CALL proveo_chrome_bridge") > strings.Index(calls, "CALL proveo_release_agent") {
				t.Errorf("proveo_seed %q starts the chrome bridge after the release, while Claude Code already owns ~/.claude.json\n%s", tc.args, calls)
			}
		})
	}
	if _, rc, _ := runSeedPhase(t, bash, nil, "claudecode", "sideways"); rc != "2" {
		t.Errorf("proveo_seed claudecode sideways rc = %s, want 2 — an unknown phase must not fall through to a full seed", rc)
	}
}

// runSeedScript runs packages/lib/proveo-seed against a stub lib and reports
// which phase reached proveo_seed and whether the boot was marked seed-done.
func runSeedScript(t *testing.T, bash string, refused, halted bool, seedRC int, args ...string) (seeded string, done, haltedAfter bool) {
	t.Helper()
	dir := t.TempDir()
	logf, doneF, refusedF := filepath.Join(dir, "seed.log"), filepath.Join(dir, "seed-done"), filepath.Join(dir, "refused")
	haltedF := filepath.Join(dir, "halted")
	stub := filepath.Join(dir, "lib.sh")
	if err := os.WriteFile(stub, []byte(`PROVEO_INSTRUCTIONS_MARKER="`+filepath.Join(dir, "released")+`"
PROVEO_SEED_REFUSED="`+refusedF+`"
PROVEO_SEED_HALTED="`+haltedF+`"
proveo_seed_step() { :; }
proveo_restore_image_path() { :; }
proveo_release_agent() { : > "$PROVEO_INSTRUCTIONS_MARKER"; }
proveo_seed() { echo "$*" >> "`+logf+`"; return `+strconv.Itoa(seedRC)+`; }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if refused {
		seedFile(t, refusedF, "a share is missing\n")
	}
	if halted {
		seedFile(t, haltedF, "")
	}
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "packages", "lib", "proveo-seed"))
	if err != nil {
		t.Fatal(err)
	}
	src := strings.Replace(string(body), "source /entrypoint-lib.sh", `source "`+stub+`"`, 1)
	if src == string(body) {
		t.Fatal("proveo-seed no longer sources /entrypoint-lib.sh in the shape this test rewrites")
	}
	script := filepath.Join(dir, "proveo-seed")
	if err := os.WriteFile(script, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "PROVEO_SEED_DONE_MARKER="+doneF, "PROVEO_SEED_PHASE=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("proveo-seed %q: %v\n%s", args, err, out)
	}
	b, _ := os.ReadFile(logf)
	_, statErr := os.Stat(doneF)
	_, haltErr := os.Stat(haltedF)
	return strings.TrimSpace(string(b)), statErr == nil, haltErr == nil
}

func TestProveoSeedMarksTheBootDoneOnlyAfterInstalls(t *testing.T) {
	t.Parallel()
	bash := bashOrSkip(t)
	for _, tc := range []struct {
		name            string
		args            []string
		refused, halted bool
		seedRC          int
		wantSeeded      string
		wantDone        bool
		wantHalted      bool
	}{
		{"release", []string{"claudecode", "release"}, false, false, 0, "claudecode release", false, false},
		{"release that fails halts the installs", []string{"claudecode", "release"}, false, false, 1, "claudecode release", false, true},
		{"release clears a stale halt", []string{"claudecode", "release"}, false, true, 0, "claudecode release", false, false},
		{"provision", []string{"claudecode", "provision"}, false, false, 0, "claudecode provision", true, false},
		{"all", []string{"claudecode"}, false, false, 0, "claudecode", true, false},
		{"provision after a refusal", []string{"claudecode", "provision"}, true, false, 0, "", true, false},
		{"provision after a halt", []string{"claudecode", "provision"}, false, true, 0, "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seeded, done, halted := runSeedScript(t, bash, tc.refused, tc.halted, tc.seedRC, tc.args...)
			if seeded != tc.wantSeeded {
				t.Errorf("proveo-seed %q called proveo_seed with %q, want %q", tc.args, seeded, tc.wantSeeded)
			}
			if done != tc.wantDone {
				t.Errorf("proveo-seed %q seed-done = %v, want %v — seed-done means the installs are over", tc.args, done, tc.wantDone)
			}
			if halted != tc.wantHalted {
				t.Errorf("proveo-seed %q halted marker = %v, want %v", tc.args, halted, tc.wantHalted)
			}
		})
	}
}
