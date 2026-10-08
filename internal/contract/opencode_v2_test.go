// SPEC: _spec/defs/opencode/native-v2-integration.puml, _spec/packages/lib/config-seeding-and-persistence.puml
package contract_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type opencodeV2Permission struct {
	Action   string `json:"action" yaml:"action"`
	Resource string `json:"resource" yaml:"resource"`
	Effect   string `json:"effect" yaml:"effect"`
}

type opencodeV2Agent struct {
	Description string                 `json:"description" yaml:"description"`
	Mode        string                 `json:"mode" yaml:"mode"`
	Permissions []opencodeV2Permission `json:"permissions" yaml:"permissions"`
}

func opencodeV2Policy(edit, shell string) []opencodeV2Permission {
	return []opencodeV2Permission{{"edit", "*", edit}, {"shell", "*", shell}}
}

func opencodeV2JSON(t *testing.T, path string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(opencodeV2Read(t, path), &got); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return got
}

func opencodeV2Read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOpencodeV2Defaults(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	defaults := opencodeV2JSON(t, filepath.Join(root, "defs/opencode/defaults/opencode.json"))
	sample := opencodeV2JSON(t, filepath.Join(root, "defs/opencode/sample_opencode.json"))
	if !reflect.DeepEqual(defaults, sample) {
		t.Fatalf("sample and baked defaults disagree:\n%v\n%v", sample, defaults)
	}
	want := map[string]any{
		"$schema":   "https://opencode.ai/config.json",
		"providers": map[string]any{}, "update": "disable",
		"compaction": map[string]any{"auto": true},
		"agents": map[string]opencodeV2Agent{
			"plan": {
				Description: "Read-only planner. Produces specs and step lists; never edits or runs shell.",
				Mode:        "primary", Permissions: opencodeV2Policy("deny", "deny"),
			},
			"build": {
				Description: "Implementer. Edits allowed; bash requires human approval per command.",
				Mode:        "primary", Permissions: opencodeV2Policy("allow", "ask"),
			},
		},
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(b, &normalized); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults, normalized) {
		t.Fatalf("native policy, descriptions, or unpinned seed changed:\n%v", defaults)
	}
}

func TestOpencodeV2RenderedSubagents(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	files := composedFiles(t, composeSubagents(t, "opencode"))
	if len(files) != 10 {
		t.Fatalf("rendered %d OpenCode subagents, want 10", len(files))
	}
	var replacements []string
	for _, line := range strings.Split(string(opencodeV2Read(t, filepath.Join(root, "defs/subagents/_vars/opencode.env"))), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			replacements = append(replacements, "{{"+key+"}}", value)
		}
	}
	replacer := strings.NewReplacer(replacements...)
	for file, rendered := range files {
		fm, body, ok := strings.Cut(strings.TrimPrefix(rendered, "---\n"), "\n---\n\n")
		if !ok {
			t.Fatalf("%s: invalid rendered frontmatter", file)
		}
		var agent opencodeV2Agent
		decoder := yaml.NewDecoder(strings.NewReader(fm))
		decoder.KnownFields(true)
		if err := decoder.Decode(&agent); err != nil {
			t.Fatalf("%s: non-native frontmatter: %v", file, err)
		}
		edit := "deny"
		if file == "spec-keeper.md" {
			edit = "allow"
			for _, scope := range []string{"`_spec/`", "`PLAN.md`", "`AGENTS.md`"} {
				if !strings.Contains(body, scope) {
					t.Errorf("spec-keeper prompt lost editing scope %s", scope)
				}
			}
		}
		if agent.Description == "" || agent.Mode != "subagent" || !reflect.DeepEqual(agent.Permissions, opencodeV2Policy(edit, "deny")) {
			t.Errorf("%s: unexpected native agent: %+v", file, agent)
		}
		shared := string(opencodeV2Read(t, filepath.Join(root, "defs/subagents", file)))
		wantBody := replacer.Replace(shared)
		if !strings.HasSuffix(wantBody, "\n") {
			wantBody += "\n"
		}
		if body != wantBody {
			t.Errorf("%s: rendering changed the shared prompt body", file)
		}
	}
}

func opencodeV2Function(t *testing.T, name string) string {
	t.Helper()
	src := string(opencodeV2Read(t, filepath.Join(repoRoot(t), "defs/opencode/entrypoint.sh")))
	start := strings.Index(src, name+"() {\n")
	if start < 0 {
		t.Fatalf("entrypoint function %s missing", name)
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("entrypoint function %s unterminated", name)
	}
	return src[start : start+end+3]
}

func opencodeV2Command(t *testing.T, home, script string, env ...string) *exec.Cmd {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	cmd := exec.Command(bashOrSkip(t), "-c", `set -euo pipefail
source "$1/packages/lib/entrypoint-lib.sh"
export PROVEO_OPENCODE_DEFAULTS_DIR="$1/defs/opencode/defaults"
export PROVEO_SUBAGENTS_DIR="$1/defs/subagents"
`+script, "bash", repoRoot(t))
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+home, "PROVEO_HOME=", "PROVEO_STATE_HOME=",
		"OPENCODE_RESEED=", "OPENCODE_CONFIG_DIR=", "XDG_CONFIG_HOME=",
		"PROVEO_LOCAL_MODEL=", "OLLAMA_API_BASE=", "OPENCODE_MODEL=", "OPENCODE_SMALL_MODEL=",
		"PROVEO_CONFIG_DIRS=", "PROVEO_CONFIG_FILES=", "PROVEO_CONFIG_FILES_ROOT=", "PROVEO_CONFIG_SYNC=",
		"PROVEO_OPENCODE_FORMATTER=", "PROVEO_WORKDIR=", "PROVEO_SHARES=",
		"PROVEO_CLONE_WORKSPACE=", "PROVEO_CLONE_ENV=", "PROVEO_CLONE_LINKS=",
		"PROVEO_CLONE_REF=", "PROVEO_CLONE_MAIN=", "PROVEO_AGENT_KIND=assistant",
		"PROVEO_HOUSE_RULES=off", "PROVEO_TOOLCHAIN_READY="+filepath.Join(home, "toolchain-ready"),
		"PROVEO_HOOKS_MARKER="+filepath.Join(home, "hooks-ready"),
		"PROVEO_SEED_REFUSED="+filepath.Join(home, "seed-refused"))
	cmd.Env = append(cmd.Env, env...)
	return cmd
}

const opencodeV2SeedIsolation = `
proveo_seed_instructions() { :; }
seed_github_known_hosts() { :; }
proveo_sync_state() { :; }
proveo_install_git_sync_hooks() { :; }
accept_workspace_trust() { :; }
proveo_compose_house_rules() { :; }
proveo_seed_browser_skills() { :; }
proveo_chrome_bridge() { :; }
proveo_release_agent() {
  [[ -f "$HOME/.config/opencode/opencode.json" || -f "$HOME/.config/opencode/opencode.jsonc" ]]
  [[ -f "$HOME/.config/opencode/agents/architect.md" ]]
  echo RELEASED_WITH_CONFIG_AND_AGENTS
}
`

func TestOpencodeV2SeedOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, json, jsonc string
		restore, reseed   bool
		entrypoint        bool
	}{
		{name: "fresh Kit ownAgent"},
		{name: "fresh entrypoint", entrypoint: true},
		{name: "existing JSON", json: `{"model":"user/model", "lsp":false, "formatter":false}`},
		{name: "restored JSON", json: `{"model":"user/model", "lsp":false, "formatter":false}`, restore: true},
		{name: "JSONC only", jsonc: "{// user config\n\"model\":\"user/model\"}"},
		{name: "restored JSONC", jsonc: "{// user config\n\"model\":\"user/model\"}", restore: true},
		{name: "both formats", json: `{"model":"lower/priority"}`, jsonc: "{// preferred\n}"},
		{name: "malformed JSON", json: "{invalid"},
		{name: "reseed JSON", json: `{"model":"user/model"}`, restore: true, reseed: true},
		{name: "reseed JSONC", jsonc: "{// user config\n}", restore: true, reseed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, state := t.TempDir(), t.TempDir()
			dst := filepath.Join(home, ".config/opencode")
			src := dst
			if tc.restore {
				src = filepath.Join(state, "opencode/config")
			}
			if tc.json != "" {
				seedFile(t, filepath.Join(src, "opencode.json"), tc.json)
			}
			if tc.jsonc != "" {
				seedFile(t, filepath.Join(src, "opencode.jsonc"), tc.jsonc)
			}
			seedFile(t, filepath.Join(src, "agents/architect.md"), "user-owned architect\n")
			seedFile(t, filepath.Join(dst, "sample_opencode.json"), `{"model":"weak/sample"}`)
			script := opencodeV2SeedIsolation + "\nproveo_seed opencode"
			if tc.entrypoint {
				script = opencodeV2SeedIsolation + opencodeV2Function(t, "seed_defaults") + "\nseed_defaults"
			}
			reseed := "0"
			if tc.reseed {
				reseed = "1"
			}
			cmd := opencodeV2Command(t, home, script, "PROVEO_STATE_HOME="+state,
				"PROVEO_CONFIG_DIRS=opencode/config|.config/opencode|", "OPENCODE_RESEED="+reseed)
			out, err := cmd.CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte("RELEASED_WITH_CONFIG_AND_AGENTS")) {
				t.Fatalf("seed/release failed: %v\n%s", err, out)
			}
			for _, config := range []struct{ name, body string }{{"opencode.json", tc.json}, {"opencode.jsonc", tc.jsonc}} {
				if config.body != "" && !tc.reseed {
					if got := string(opencodeV2Read(t, filepath.Join(dst, config.name))); got != config.body {
						t.Errorf("overwrote %s:\n%s", config.name, got)
					}
				}
			}
			if tc.jsonc != "" && tc.json == "" {
				if _, err := os.Stat(filepath.Join(dst, "opencode.json")); !os.IsNotExist(err) {
					t.Errorf("created shadow JSON config despite existing JSONC: %v", err)
				}
			}
			files := composedFiles(t, filepath.Join(dst, "agents"))
			if len(files) != 10 {
				t.Errorf("seed produced %d agents, want 10", len(files))
			}
			if !tc.reseed {
				if files["architect.md"] != "user-owned architect\n" {
					t.Error("overwrote the user-owned rendered agent")
				}
			} else if !strings.Contains(files["architect.md"], "permissions:") {
				t.Error("explicit reseed did not replace the rendered agent")
			}
			if tc.reseed || (tc.json == "" && tc.jsonc == "") {
				name := "opencode.json"
				if tc.jsonc != "" {
					name = "opencode.jsonc"
				}
				got := opencodeV2JSON(t, filepath.Join(dst, name))
				if tc.jsonc == "" {
					if got["lsp"] != true || got["formatter"] != true {
						t.Errorf("release did not follow optional wiring: %v", got)
					}
					delete(got, "lsp")
					delete(got, "formatter")
				}
				want := opencodeV2JSON(t, filepath.Join(repoRoot(t), "defs/opencode/defaults/opencode.json"))
				if !reflect.DeepEqual(got, want) {
					t.Errorf("seed did not use unpinned native baked defaults: %v", got)
				}
			}
		})
	}
}

func TestOpencodeV2SeedOrdering(t *testing.T) {
	t.Parallel()
	seed := seedBody(t, entrypointLib(t))
	last := -1
	for _, step := range []string{"proveo_sync_config restore", "proveo_bootstrap_opencode_config", "render_subagents opencode", "proveo_wire_config", "\n proveo_release_agent"} {
		at := strings.Index(seed, step)
		if at < 0 || at <= last {
			t.Fatalf("restore → missing-seed → render → wire → release ordering lost at %s", step)
		}
		last = at
	}
}

func TestOpencodeV2OptionalWritersPreserveUserFiles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, json, jsonc, warning string }{
		{"malformed", "{invalid", "", "fix"},
		{"null", "null", "", "fix"},
		{"array", "[]", "", "fix"},
		{"multiple documents", "{}\n{}", "", "fix"},
		{"JSONC only", "", "{// user comment\n}", "directly"},
		{"higher priority JSONC", `{"model":"user/model"}`, "{// preferred\n}", "directly"},
		{"JSONC with invalid JSON", "{invalid", "{// preferred\n}", "directly"},
		{"explicit off", `{"lsp":false,"formatter":false}`, "", ""},
		{"explicit formatter object", `{"lsp":false,"formatter":{"prettier":{"disabled":true}}}`, "", ""},
		{"explicit formatter true", `{"lsp":false,"formatter":true}`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			dst := filepath.Join(home, ".config/opencode")
			if tc.json != "" {
				seedFile(t, filepath.Join(dst, "opencode.json"), tc.json)
			}
			if tc.jsonc != "" {
				seedFile(t, filepath.Join(dst, "opencode.jsonc"), tc.jsonc)
			}
			cmd := opencodeV2Command(t, home, `
detect_workspace_lsps() { printf '%s\n' 'typescript|1|typescript-language-server|--stdio|.ts'; }
configure_opencode_lsp "$PWD"
configure_opencode_formatter`)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("optional writers failed instead of skipping: %v\n%s", err, out)
			}
			if tc.warning != "" && (bytes.Count(out, []byte("Skipping automatic")) != 2 || !bytes.Contains(out, []byte(tc.warning))) {
				t.Errorf("missing actionable warnings:\n%s", out)
			}
			if tc.json != "" {
				if got := string(opencodeV2Read(t, filepath.Join(dst, "opencode.json"))); got != tc.json {
					t.Errorf("optional writer changed JSON: %s", got)
				}
			} else if _, err := os.Stat(filepath.Join(dst, "opencode.json")); !os.IsNotExist(err) {
				t.Errorf("optional writer created shadow JSON: %v", err)
			}
			if tc.jsonc != "" && string(opencodeV2Read(t, filepath.Join(dst, "opencode.jsonc"))) != tc.jsonc {
				t.Error("optional writer changed JSONC")
			}
		})
	}
}

func TestOpencodeV2LocalModelMerge(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"", "http://ollama:11434", "http://ollama:11434///", "http://ollama:11434/v1", "http://ollama:11434/v1/", "http://ollama:11434/v1/v1//"} {
		t.Run(base, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			config := filepath.Join(home, ".config/opencode/opencode.json")
			want := opencodeV2JSON(t, filepath.Join(repoRoot(t), "defs/opencode/defaults/opencode.json"))
			want["providers"].(map[string]any)["kept"] = map[string]any{"name": "user provider"}
			want["agents"].(map[string]any)["title"] = map[string]any{"description": "user title", "model": "user/model"}
			want["mcp"] = map[string]any{"servers": map[string]any{"mobile": map[string]any{"command": []any{"user-command"}}}}
			b, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			seedFile(t, config, string(b))
			script := opencodeV2Function(t, "configure_opencode_local_model") + `
configure_opencode_local_model
printf 'metadata=%s|%s\n' "$OPENCODE_MODEL" "$OPENCODE_SMALL_MODEL"
configure_opencode_local_model`
			out, err := opencodeV2Command(t, home, script, "PROVEO_LOCAL_MODEL=qwen3:8b", "OLLAMA_API_BASE="+base).CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte("metadata=ollama/qwen3:8b|ollama/qwen3:8b")) {
				t.Fatalf("local wiring failed: %v\n%s", err, out)
			}
			want["providers"].(map[string]any)["ollama"] = map[string]any{
				"package": "@opencode/ai/providers/openai-compatible", "name": "Ollama (local)",
				"settings": map[string]any{"baseURL": "http://ollama:11434/v1", "apiKey": "ollama"},
				"models":   map[string]any{"qwen3:8b": map[string]any{"name": "qwen3:8b (local)"}},
			}
			want["model"] = "ollama/qwen3:8b"
			want["agents"].(map[string]any)["title"].(map[string]any)["model"] = "ollama/qwen3:8b"
			if got := opencodeV2JSON(t, config); !reflect.DeepEqual(got, want) {
				t.Errorf("native local merge lost user values or selected an extra model:\n%v", got)
			}
		})
	}
}

func TestOpencodeV2LocalModelRefusesUnsafeWrites(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, json, jsonc, model string }{
		{"malformed", "{invalid", "", "qwen3:8b"},
		{"non-object", "[]", "", "qwen3:8b"},
		{"multiple documents", "{}\n{}", "", "qwen3:8b"},
		{"invalid providers", `{"providers":[]}`, "", "qwen3:8b"},
		{"JSONC only", "", "{// user\n}", "qwen3:8b"},
		{"higher priority JSONC", `{"model":"user/model"}`, "{// user\n}", "qwen3:8b"},
		{"no explicit selection", "{invalid", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			dst := filepath.Join(home, ".config/opencode")
			if tc.json != "" {
				seedFile(t, filepath.Join(dst, "opencode.json"), tc.json)
			}
			if tc.jsonc != "" {
				seedFile(t, filepath.Join(dst, "opencode.jsonc"), tc.jsonc)
			}
			script := opencodeV2Function(t, "configure_opencode_local_model") + "\nconfigure_opencode_local_model"
			out, err := opencodeV2Command(t, home, script, "PROVEO_LOCAL_MODEL="+tc.model).CombinedOutput()
			if (err != nil) != (tc.model != "") {
				t.Errorf("explicit wiring error = %v, model = %q\n%s", err, tc.model, out)
			}
			if bytes.Contains(out, []byte("Wired Ollama")) {
				t.Errorf("reported success without wiring:\n%s", out)
			}
			if tc.model != "" && !bytes.Contains(out, []byte("requested Ollama model")) {
				t.Errorf("missing actionable failure:\n%s", out)
			}
			if tc.json != "" {
				if got := string(opencodeV2Read(t, filepath.Join(dst, "opencode.json"))); got != tc.json {
					t.Errorf("failed local wiring changed JSON: %s", got)
				}
			} else if _, err := os.Stat(filepath.Join(dst, "opencode.json")); !os.IsNotExist(err) {
				t.Errorf("failed local wiring created shadow JSON: %v", err)
			}
			if tc.jsonc != "" && string(opencodeV2Read(t, filepath.Join(dst, "opencode.jsonc"))) != tc.jsonc {
				t.Error("failed local wiring changed JSONC")
			}
			leftovers, err := filepath.Glob(filepath.Join(dst, "*.tmp.*"))
			if err != nil || len(leftovers) != 0 {
				t.Errorf("failed local wiring left temporary files: %v, %v", leftovers, err)
			}
		})
	}
}

func TestOpencodeV2NativeValidation(t *testing.T) {
	image := os.Getenv("PROVEO_OPENCODE_V2_IMAGE")
	if image == "" {
		t.Skip("set PROVEO_OPENCODE_V2_IMAGE to the v2.0.25 image for offline native validation")
	}
	root := repoRoot(t)
	for _, model := range []string{"", "qwen3:8b"} {
		name := "defaults"
		if model != "" {
			name = "local model"
		}
		t.Run(name, func(t *testing.T) {
			args := []string{"run", "--rm", "--network", "none", "--env", "OPENCODE_SERVER_PASSWORD=offline-contract",
				"--env", "PROVEO_LOCAL_MODEL=" + model, "--env", "OLLAMA_API_BASE=http://ollama:11434/v1/v1/"}
			for _, mount := range []struct{ src, dst string }{
				{"packages/lib/entrypoint-lib.sh", "/entrypoint-lib.sh"},
				{"defs/opencode/defaults", "/opt/opencode/defaults"},
				{"defs/subagents", "/opt/proveo/subagents"},
			} {
				args = append(args, "--mount", "type=bind,src="+filepath.Join(root, mount.src)+",dst="+mount.dst+",readonly")
			}
			script := `set -euo pipefail
source /entrypoint-lib.sh
proveo_bootstrap_opencode_config
render_subagents opencode "$HOME/.config/opencode/agents"
` + opencodeV2Function(t, "configure_opencode_local_model") + `
configure_opencode_local_model
native="$(PATH="${PATH#/opt/proveo/shims:}" command -v opencode)"
[[ "$("$native" --version)" == "opencode v2.0.25" ]]
"$native" serve --hostname 127.0.0.1 --port 4096 >/dev/null 2>&1 & server=$!
trap 'kill "$server" 2>/dev/null || true' EXIT
for ((i=0;i<100;i++)); do
  result="$(curl -fsS -u opencode:offline-contract 'http://127.0.0.1:4096/api/agent?location%5Bdirectory%5D=%2Fworkspace' 2>/dev/null || true)"
  if jq -e '.data | any(.id == "architect")' <<< "$result" >/dev/null 2>&1; then
    config="$(curl -fsS -u opencode:offline-contract 'http://127.0.0.1:4096/api/config?location%5Bdirectory%5D=%2Fworkspace')"
    printf 'NATIVE_VALIDATION={"agents":%s,"config":%s}\n' "$result" "$config"
    exit 0
  fi
  sleep 0.2
done
printf 'Native agent registration failed: %s\n' "$result" >&2
exit 1`
			args = append(args, "--entrypoint", "/bin/bash", image, "-c", script)
			out, err := exec.Command("docker", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("offline native validation failed: %v\n%s", err, out)
			}
			_, payload, ok := strings.Cut(string(out), "NATIVE_VALIDATION=")
			if !ok {
				t.Fatalf("native validation output missing:\n%s", out)
			}
			var got struct {
				Agents struct {
					Data []struct {
						ID string `json:"id"`
						opencodeV2Agent
					} `json:"data"`
				} `json:"agents"`
				Config []struct {
					Info map[string]any `json:"info"`
				} `json:"config"`
			}
			if err := json.Unmarshal([]byte(payload), &got); err != nil {
				t.Fatalf("native response: %v\n%s", err, payload)
			}
			want := opencodeV2JSON(t, filepath.Join(root, "defs/opencode/defaults/opencode.json"))
			if model != "" {
				selection := map[string]any{"providerID": "ollama", "model": model}
				want["model"] = selection
				want["agents"].(map[string]any)["title"] = map[string]any{"model": selection}
				want["providers"].(map[string]any)["ollama"] = map[string]any{
					"package": "@opencode/ai/providers/openai-compatible", "name": "Ollama (local)",
					"settings": map[string]any{"baseURL": "http://ollama:11434/v1", "apiKey": "ollama"},
					"models":   map[string]any{model: map[string]any{"name": model + " (local)"}},
				}
			}
			if len(got.Config) == 0 || !reflect.DeepEqual(got.Config[0].Info, want) {
				t.Errorf("native decoder changed or dropped configuration:\n%+v", got.Config)
			}
			policies := map[string][]opencodeV2Permission{"plan": opencodeV2Policy("deny", "deny"), "build": opencodeV2Policy("allow", "ask")}
			for file := range composedFiles(t, composeSubagents(t, "opencode")) {
				edit := "deny"
				if file == "spec-keeper.md" {
					edit = "allow"
				}
				policies[strings.TrimSuffix(file, ".md")] = opencodeV2Policy(edit, "deny")
			}
			for _, agent := range got.Agents.Data {
				policy, ok := policies[agent.ID]
				if !ok {
					continue
				}
				var relevant []opencodeV2Permission
				for _, permission := range agent.Permissions {
					if permission.Action == "edit" || permission.Action == "shell" {
						relevant = append(relevant, permission)
					}
				}
				if n := len(relevant); n < 2 || !reflect.DeepEqual(relevant[n-2:], policy) {
					t.Errorf("%s: native edit/shell permission tail = %v, want %v", agent.ID, relevant, policy)
				}
				if agent.Mode != "subagent" && agent.ID != "plan" && agent.ID != "build" {
					t.Errorf("%s: native mode = %q", agent.ID, agent.Mode)
				}
				delete(policies, agent.ID)
			}
			if len(policies) != 0 {
				t.Errorf("native server did not register agents: %v", policies)
			}
		})
	}
}
