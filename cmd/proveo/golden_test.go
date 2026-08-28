// SPEC: _spec/internal/run/run-spec.puml
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

	"github.com/proveo-ca/proveo/internal/backend/dockeregress"
	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/egress"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/run"
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
		plan.OllamaContainer, plan.CAWaitPath, dockeregress.NeedsLifecycle(plan))
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
		in   dockeregress.Input
	}{
		{
			name: "open-forward-opencode",
			in: dockeregress.Input{
				Target:      "opencode",
				Image:       "proveo/opencode:latest",
				Mode:        "open",
				Credentials: "forward",
				Sid:         "sid", EgDir: "/st", UID: "1000", GID: "1000",
				PidsLimit: 4096,
				Workdir:   "/app",
				Mounts:    []runner.Mount{{Host: "/work", Container: "/app"}},
			},
		},
		{
			name: "open-broker-opencode",
			in: dockeregress.Input{
				Target:      "opencode",
				Image:       "proveo/opencode:latest",
				Mode:        "open",
				Credentials: "broker",
				Sid:         "sid", EgDir: "/st", UID: "1000", GID: "1000",
				Providers: []string{"anthropic"}, BrokerFile: "/st/inject/broker.env",
				ProviderHosts: []string{"api.anthropic.com"},
				PidsLimit:     4096,
				Workdir:       "/app",
				Mounts:        []runner.Mount{{Host: "/work", Container: "/app"}},
			},
		},
		{
			name: "allowlist-broker-cursor",
			in: dockeregress.Input{
				Target:      "cursor",
				Image:       "proveo/cursor:latest",
				Mode:        "allowlist",
				Credentials: "broker",
				Sid:         "sid", EgDir: "/st", UID: "1000", GID: "1000",
				Providers: []string{"cursor"}, BrokerFile: "/st/inject/broker.env",
				WriteHosts:    []string{"api2.cursor.sh"},
				ProviderHosts: []string{"api2.cursor.sh"},
				Env: []string{
					"CURSOR_API_KEY=" + entrypoint.DefaultSentinel,
					"PROVEO_CREDENTIAL_BROKER_KEYS=CURSOR_API_KEY",
				},
				PidsLimit: 4096,
				Workdir:   "/app",
				Mounts:    []runner.Mount{{Host: "/work", Container: "/app"}},
			},
		},
		{
			name: "review-broker-claudecode",
			in: dockeregress.Input{
				Target:      "claudecode",
				Image:       "proveo/claudecode:latest",
				Mode:        "review",
				Credentials: "broker",
				Sid:         "sid", EgDir: "/st", UID: "1000", GID: "1000",
				Providers: []string{"anthropic"}, BrokerFile: "/st/inject/broker.env",
				ReviewSocket:  "/st/review/gate.sock",
				ProviderHosts: []string{"api.anthropic.com"},
				PidsLimit:     4096,
				Workdir:       "/app",
				Mounts:        []runner.Mount{{Host: "/work", Container: "/app"}},
			},
		},
		{
			name: "allowlist-localmodel-opencode",
			in: dockeregress.Input{
				Target:      "opencode",
				Image:       "proveo/opencode:latest",
				Mode:        "allowlist",
				Credentials: "broker",
				LocalModel:  "qwen3",
				Sid:         "sid", EgDir: "/st", UID: "1000", GID: "1000",
				ModelsDir: "/models",
				PidsLimit: 4096,
				Workdir:   "/app",
				Mounts:    []runner.Mount{{Host: "/work", Container: "/app"}},
			},
		},
		{
			name: "allowlist-shell-datadir-scope",
			in: dockeregress.Input{
				Target:      "cecli",
				Image:       "proveo/cecli:latest",
				Mode:        "allowlist",
				Credentials: "broker",
				DataDir:     "/data",
				Shell:       true,
				Sid:         "sid", EgDir: "/st", UID: "501", GID: "20",
				PidsLimit: 2048,
				Workdir:   "/app/apps/web",
				Mounts: []runner.Mount{
					{Host: "/work", Container: "/app"},
					{Host: "/work/reports", Container: "/app/output"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, agent, err := dockeregress.Assemble(tc.in)
			if err != nil {
				t.Fatalf("dockeregress.Assemble: %v", err)
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
		in   func(work, data, home string) sandbox.Input
	}{
		{
			name: "claudecode-broker",
			in: func(work, data, home string) sandbox.Input {
				return sandbox.Input{
					Target:         "claudecode",
					Image:          "proveo/claudecode:latest",
					Evidence:       run.EvidenceVerbose,
					Forwards:       false,
					SandboxAddonOn: true,
					Man:            claudecode, Sid: "proveo-sid", Lookup: lookup, Detected: detected("anthropic"),
					GitEnv:  []string{"GIT_AUTHOR_NAME=Executor", "GIT_AUTHOR_EMAIL=executor@proveo.test"},
					HomeEnv: []string{"HOME=/proveo-home", "PROVEO_HOME=/proveo-home"},
					Mounts: []runner.Mount{
						{Host: work, Container: "/app"},
						{Host: home, Container: "/proveo-home"},
					},
					EgDir: "/st", Memory: "8192m", HomeRoot: home,
				}
			},
		},
		{
			name: "cursor-forward",
			in: func(work, data, home string) sandbox.Input {
				return sandbox.Input{
					Target:         "cursor",
					Image:          "proveo/cursor:latest",
					Evidence:       run.EvidenceDefault,
					Forwards:       true,
					SandboxAddonOn: true,
					Man:            cursor, Sid: "proveo-sid", Lookup: lookup, Detected: detected("cursor"),
					GitEnv:  []string{"GIT_AUTHOR_NAME=Executor"},
					HomeEnv: []string{"HOME=/proveo-home", "PROVEO_HOME=/proveo-home"},
					Mounts:  []runner.Mount{{Host: work, Container: "/app"}},
					EgDir:   "/st", Memory: "8192m", HomeRoot: home,
				}
			},
		},
		{
			name: "claudecode-shell-clone-datadir",
			in: func(work, data, home string) sandbox.Input {
				return sandbox.Input{
					Target:         "claudecode",
					Image:          "proveo/claudecode:latest",
					Shell:          true,
					Clone:          true,
					Evidence:       run.EvidenceVerbose,
					Forwards:       false,
					SandboxAddonOn: true,
					Man:            claudecode, Sid: "proveo-sid", Lookup: lookup, Detected: detected("anthropic"),
					GitEnv:   []string{"GIT_AUTHOR_NAME=Executor"},
					HomeEnv:  []string{"HOME=/proveo-home", "PROVEO_HOME=/proveo-home"},
					Mounts:   []runner.Mount{{Host: work, Container: "/app"}},
					DataDir:  data,
					ScopeRel: "apps/web",
					EgDir:    "/st", Memory: "4096m", HomeRoot: home,
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROVEO_EGRESS_PROVIDER_DOMAINS", "")
			t.Setenv("PROVEO_SBX_MCP", "")
			work, data, home := t.TempDir(), t.TempDir(), t.TempDir()

			cfg, kit, secrets := sandbox.Spec(tc.in(work, data, home))
			got := renderSandboxPlan(t, cfg, kit, secrets)
			assertNoSecretValues(t, got, oauthValue, keyValue, cursorValue)
			got = scrub(got, map[string]string{work: "<WORK>", data: "<DATA>", home: "<HOME>"})
			assertGolden(t, "sbx-"+tc.name, got)
		})
	}
}
