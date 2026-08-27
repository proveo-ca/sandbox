// SPEC: _spec/_plans/main-decomposition.puml
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/sbx"
)

var updateGolden = flag.Bool("update", false, "rewrite the plan goldens from what the code produces now")

const goldenDir = "testdata/plan"

func goldenPath(name string) string { return filepath.Join(goldenDir, name+".golden") }

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := goldenPath(name)
	if *updateGolden {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\nrun `go test ./cmd/proveo -run Golden -update` to create it, then read the diff before committing", err)
	}
	if string(want) == got {
		return
	}
	t.Errorf("%s changed.\n--- want ---\n%s\n--- got ---\n%s\n"+
		"If the change is intended, re-run with -update and review the diff as part of the change.",
		path, want, got)
}

func section(b *strings.Builder, title string) { fmt.Fprintf(b, "== %s ==\n", title) }

func commands(b *strings.Builder, title string, cmds []egress.Command) {
	section(b, title)
	for _, c := range cmds {
		fmt.Fprintf(b, "docker %s\n", strings.Join(c, " "))
	}
}

func renderDockerPlan(plan egress.Plan, agent runner.Config) string {
	var b strings.Builder
	section(&b, "agent argv")
	fmt.Fprintf(&b, "docker %s\n", strings.Join(runner.DockerRunArgs(agent), " "))
	commands(&b, "networks", plan.Networks)
	commands(&b, "sidecars", plan.Sidecars)
	commands(&b, "connects", plan.Connects)
	commands(&b, "cleanup", plan.Cleanup)
	section(&b, "images")
	for _, i := range plan.Images {
		fmt.Fprintln(&b, i)
	}
	section(&b, "flags")
	fmt.Fprintf(&b, "usesSquid=%v\nagentNetwork=%s\nproxyContainer=%s\nsquidContainer=%s\nollamaContainer=%s\ncaWaitPath=%s\nneedsLifecycle=%v\n",
		plan.UsesSquid, plan.AgentNetwork, plan.ProxyContainer, plan.SquidContainer,
		plan.OllamaContainer, plan.CAWaitPath, needsLifecycle(plan))
	return b.String()
}

func renderSandboxPlan(t *testing.T, cfg sbx.RunConfig, kit sbx.Kit, secrets [][2]string) string {
	t.Helper()
	var b strings.Builder
	section(&b, "sbx argv")
	fmt.Fprintf(&b, "sbx %s\n", strings.Join(sbx.RunArgs(cfg), " "))
	section(&b, "secret names")
	for _, kv := range secrets {
		fmt.Fprintln(&b, kv[0])
	}
	section(&b, "kit spec.yaml")
	doc, err := yaml.Marshal(kit)
	if err != nil {
		t.Fatal(err)
	}
	b.Write(doc)
	return b.String()
}

func scrub(s string, replacements map[string]string) string {
	keys := make([]string, 0, len(replacements))
	for k := range replacements {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if k == "" {
			continue
		}
		s = strings.ReplaceAll(s, k, replacements[k])
	}
	return s
}

func assertNoSecretValues(t *testing.T, rendered string, values ...string) {
	t.Helper()
	for _, v := range values {
		if v != "" && strings.Contains(rendered, v) {
			t.Fatalf("a secret VALUE reached the rendered plan: %q", v)
		}
	}
}

func TestDockerPlanGolden(t *testing.T) {
	cases := []struct {
		name string
		in   assembleInput
	}{
		{
			name: "open-forward-opencode",
			in: assembleInput{
				params: runParams{mode: "open", credentials: "forward", target: "opencode", image: "proveo/opencode:latest"},
				sid:    "sid", egDir: "/st", uid: "1000", gid: "1000",
				pidsLimit: 4096,
				workdir:   "/app",
				mounts:    []runner.Mount{{Host: "/work", Container: "/app"}},
			},
		},
		{
			name: "open-broker-opencode",
			in: assembleInput{
				params: runParams{mode: "open", credentials: "broker", target: "opencode", image: "proveo/opencode:latest"},
				sid:    "sid", egDir: "/st", uid: "1000", gid: "1000",
				providers: []string{"anthropic"}, brokerFile: "/st/inject/broker.env",
				providerHosts: []string{"api.anthropic.com"},
				pidsLimit:     4096,
				workdir:       "/app",
				mounts:        []runner.Mount{{Host: "/work", Container: "/app"}},
			},
		},
		{
			name: "allowlist-broker-cursor",
			in: assembleInput{
				params: runParams{mode: "allowlist", credentials: "broker", target: "cursor", image: "proveo/cursor:latest"},
				sid:    "sid", egDir: "/st", uid: "1000", gid: "1000",
				providers: []string{"cursor"}, brokerFile: "/st/inject/broker.env",
				writeHosts:    []string{"api2.cursor.sh"},
				providerHosts: []string{"api2.cursor.sh"},
				env: []string{
					"CURSOR_API_KEY=" + entrypoint.DefaultSentinel,
					"PROVEO_CREDENTIAL_BROKER_KEYS=CURSOR_API_KEY",
				},
				pidsLimit: 4096,
				workdir:   "/app",
				mounts:    []runner.Mount{{Host: "/work", Container: "/app"}},
			},
		},
		{
			name: "review-broker-claudecode",
			in: assembleInput{
				params: runParams{mode: "review", credentials: "broker", target: "claudecode", image: "proveo/claudecode:latest"},
				sid:    "sid", egDir: "/st", uid: "1000", gid: "1000",
				providers: []string{"anthropic"}, brokerFile: "/st/inject/broker.env",
				reviewSocket:  "/st/review/gate.sock",
				providerHosts: []string{"api.anthropic.com"},
				pidsLimit:     4096,
				workdir:       "/app",
				mounts:        []runner.Mount{{Host: "/work", Container: "/app"}},
			},
		},
		{
			name: "allowlist-localmodel-opencode",
			in: assembleInput{
				params: runParams{mode: "allowlist", credentials: "broker", target: "opencode", image: "proveo/opencode:latest", localModel: "qwen3"},
				sid:    "sid", egDir: "/st", uid: "1000", gid: "1000",
				modelsDir: "/models",
				pidsLimit: 4096,
				workdir:   "/app",
				mounts:    []runner.Mount{{Host: "/work", Container: "/app"}},
			},
		},
		{
			name: "allowlist-shell-datadir-scope",
			in: assembleInput{
				params: runParams{
					mode: "allowlist", credentials: "broker", target: "cecli", image: "proveo/cecli:latest",
					shell: true, dataDir: "/data",
				},
				sid: "sid", egDir: "/st", uid: "501", gid: "20",
				pidsLimit: 2048,
				workdir:   "/app/apps/web",
				mounts: []runner.Mount{
					{Host: "/work", Container: "/app"},
					{Host: "/work/reports", Container: "/app/output"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, agent, err := assemble(tc.in)
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			assertGolden(t, "docker-"+tc.name, renderDockerPlan(plan, agent))
		})
	}
}

func TestSandboxPlanGolden(t *testing.T) {
	const oauthValue = "oauth-secret-value"
	const keyValue = "sk-secret-value"
	const cursorValue = "cursor-secret-value"

	lookup := func(k string) string {
		return map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN": oauthValue,
			"ANTHROPIC_API_KEY":       keyValue,
			"ANTHROPIC_BASE_URL":      "https://api.anthropic.com",
			"CURSOR_API_KEY":          cursorValue,
		}[k]
	}
	claudecode := manifest.Manifest{
		Name:         "claudecode",
		Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}, Hosts: []string{"api.anthropic.com", "statsig.anthropic.com"}},
		Env: []manifest.EnvVar{
			{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true},
			{Name: "ANTHROPIC_BASE_URL"},
		},
	}
	cursor := manifest.Manifest{
		Name:         "cursor",
		Provider:     "cursor",
		Capabilities: manifest.Capabilities{Egress: []string{"open"}, Credentials: []string{"forward"}, Providers: []string{"cursor"}, Hosts: []string{"api2.cursor.sh"}},
		Env:          []manifest.EnvVar{{Name: "CURSOR_API_KEY", Secret: true}},
	}
	detected := func(name string) []string {
		if _, ok := provider.Lookup(name); ok {
			return []string{name}
		}
		return nil
	}

	cases := []struct {
		name string
		in   func(work, data, home string) runSandboxInput
	}{
		{
			name: "claudecode-broker",
			in: func(work, data, home string) runSandboxInput {
				return runSandboxInput{
					params: runParams{
						target: "claudecode", image: "proveo/claudecode:latest",
						mode: "allowlist", credentials: "broker", evidence: evidenceVerbose,
					},
					man: claudecode, sid: "proveo-sid", lookup: lookup, detected: detected("anthropic"),
					gitEnv:  []string{"GIT_AUTHOR_NAME=Executor", "GIT_AUTHOR_EMAIL=executor@proveo.test"},
					homeEnv: []string{"HOME=/proveo-home", "PROVEO_HOME=/proveo-home"},
					mounts: []runner.Mount{
						{Host: work, Container: "/app"},
						{Host: home, Container: "/proveo-home"},
					},
					egDir: "/st", memory: "8192m", homeRoot: home,
				}
			},
		},
		{
			name: "cursor-forward",
			in: func(work, data, home string) runSandboxInput {
				return runSandboxInput{
					params: runParams{
						target: "cursor", image: "proveo/cursor:latest",
						mode: "open", credentials: "forward", evidence: evidenceDefault,
					},
					man: cursor, sid: "proveo-sid", lookup: lookup, detected: detected("cursor"),
					gitEnv:  []string{"GIT_AUTHOR_NAME=Executor"},
					homeEnv: []string{"HOME=/proveo-home", "PROVEO_HOME=/proveo-home"},
					mounts:  []runner.Mount{{Host: work, Container: "/app"}},
					egDir:   "/st", memory: "8192m", homeRoot: home,
				}
			},
		},
		{
			name: "claudecode-shell-clone-datadir",
			in: func(work, data, home string) runSandboxInput {
				return runSandboxInput{
					params: runParams{
						target: "claudecode", image: "proveo/claudecode:latest",
						mode: "allowlist", credentials: "broker", evidence: evidenceVerbose,
						shell: true, clone: true,
					},
					man: claudecode, sid: "proveo-sid", lookup: lookup, detected: detected("anthropic"),
					gitEnv:   []string{"GIT_AUTHOR_NAME=Executor"},
					homeEnv:  []string{"HOME=/proveo-home", "PROVEO_HOME=/proveo-home"},
					mounts:   []runner.Mount{{Host: work, Container: "/app"}},
					dataDir:  data,
					scopeRel: "apps/web",
					egDir:    "/st", memory: "4096m", homeRoot: home,
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROVEO_EGRESS_PROVIDER_DOMAINS", "")
			t.Setenv("PROVEO_SBX_MCP", "")
			work, data, home := t.TempDir(), t.TempDir(), t.TempDir()

			cfg, kit, secrets := sandboxSpec(tc.in(work, data, home))
			got := renderSandboxPlan(t, cfg, kit, secrets)
			assertNoSecretValues(t, got, oauthValue, keyValue, cursorValue)
			got = scrub(got, map[string]string{work: "<WORK>", data: "<DATA>", home: "<HOME>"})
			assertGolden(t, "sbx-"+tc.name, got)
		})
	}
}
