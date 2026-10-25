// SPEC: _spec/internal/sbx/sbx-kit-v3.puml
package sbx

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeProbeWorkload(t *testing.T, agent string, w WorkloadV3, r RecipeV3) (string, error) {
	t.Helper()
	return WriteWorkloadV3(KitDirV3(t.TempDir(), agent), w, r, nil)
}

func TestAV3WorkloadIsNamedForItsAgentAndOpensWithTheKitSyntax(t *testing.T) {
	t.Parallel()
	agent := AgentName("claudecode")
	dir, err := writeProbeWorkload(t, agent, WorkloadV3{
		DisplayName: "proveo claudecode", Version: "1.0.0",
		Capabilities: []CapabilityV3{{Type: CapSbx}},
	}, RecipeV3{From: "proveo/claudecode:local"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != "proveo-claudecode" {
		t.Errorf("kit dir = %s; sbx registers the dir name as the agent", dir)
	}
	desc, err := os.ReadFile(filepath.Join(dir, agent+".yaml"))
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	if first, _, _ := strings.Cut(string(desc), "\n"); first != KitSyntaxV3 {
		t.Errorf("first line = %q, want %q", first, KitSyntaxV3)
	}
	if _, err := os.Stat(filepath.Join(dir, "spec.yaml")); err == nil {
		t.Error("a v3 kit wrote spec.yaml, which sbx parses as v2 (#738)")
	}
	var doc map[string]any
	if err := yaml.Unmarshal(desc, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["schemaVersion"] != "3" || doc["kind"] != "workload" {
		t.Errorf("schemaVersion=%v kind=%v, want \"3\" and workload", doc["schemaVersion"], doc["kind"])
	}
	allowed := []string{"schemaVersion", "displayName", "description", "kind", "version", "capabilities"}
	for k := range doc {
		if !slices.Contains(allowed, k) {
			t.Errorf("descriptor carries %q; any field the spec does not define is an error", k)
		}
	}
}

func TestTheRecipeIsFromTheImageAndKeepsTheLaunchInExecForm(t *testing.T) {
	t.Parallel()
	agent := AgentName("claudecode")
	dir, err := writeProbeWorkload(t, agent, WorkloadV3{Version: "1.0.0", Capabilities: []CapabilityV3{{Type: CapSbx}}},
		RecipeV3{From: "proveo/claudecode:local", Entrypoint: []string{"claude", "--dangerously-skip-permissions"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, agent+".dockerfile"))
	want := "# syntax=docker/dockerfile:1\nFROM proveo/claudecode:local\n" +
		"ENTRYPOINT [\"claude\", \"--dangerously-skip-permissions\"]\nCMD []\n"
	if string(got) != want {
		t.Errorf("recipe =\n%s\nwant\n%s", got, want)
	}
}

func TestAV3WorkloadRefusesNamesSbxWouldNotRegister(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"claude", "devin", "spec"} {
		if _, err := writeProbeWorkload(t, agent, WorkloadV3{Version: "1"}, RecipeV3{From: "x"}); err == nil {
			t.Errorf("WriteWorkloadV3(%q) succeeded; a built-in name fails to register and spec.yaml parses as v2", agent)
		}
	}
	if _, err := writeProbeWorkload(t, "proveo-x", WorkloadV3{Version: "1"}, RecipeV3{}); err == nil {
		t.Error("a recipe without FROM was written")
	}
}

func TestLifecycleHooksDeclareTheirUserAndTheEnvTheyRead(t *testing.T) {
	t.Parallel()
	agent := AgentName("hermes")
	dir, err := writeProbeWorkload(t, agent, WorkloadV3{Version: "1", Capabilities: []CapabilityV3{
		{Type: CapSbx},
		{Type: CapLifecycle, Config: LifecycleV3{Install: []HookV3{{Command: "seed", User: "0", Env: []string{"PROVEO_WORKDIR"}}}}},
		{Type: CapPort, Config: PortV3{Name: "devserver", Container: 8083}},
	}}, RecipeV3{From: "proveo/hermes:local"})
	if err != nil {
		t.Fatal(err)
	}
	desc, _ := os.ReadFile(filepath.Join(dir, agent+".yaml"))
	var doc struct {
		Capabilities []struct {
			Type   string         `yaml:"type"`
			Config map[string]any `yaml:"config"`
		} `yaml:"capabilities"`
	}
	if err := yaml.Unmarshal(desc, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Capabilities) != 3 {
		t.Fatalf("capabilities = %d, want 3:\n%s", len(doc.Capabilities), desc)
	}
	hook := doc.Capabilities[1].Config["install"].([]any)[0].(map[string]any)
	if hook["user"] != "0" || hook["env"].([]any)[0] != "PROVEO_WORKDIR" {
		t.Errorf("install hook = %v; a hook sees only the env it declares", hook)
	}
	if doc.Capabilities[2].Config["container"] != 8083 {
		t.Errorf("port@1 config = %v", doc.Capabilities[2].Config)
	}
}

func TestLaunchFlagsResendTheLaunchTailBecauseSbxReplacesArgvAfterZero(t *testing.T) {
	t.Parallel()
	launch := []string{"claude", "--dangerously-skip-permissions"}
	if got := LaunchArgs(launch, nil); got != nil {
		t.Errorf("LaunchArgs without flags = %q; no trailing command keeps ENTRYPOINT + CMD", got)
	}
	got := LaunchArgs(launch, []string{"--chrome"})
	if want := []string{"--dangerously-skip-permissions", "--chrome"}; !slices.Equal(got, want) {
		t.Errorf("LaunchArgs = %q, want %q (P7: `-- extra` alone would drop the launch's own flags)", got, want)
	}
	if launch[1] != "--dangerously-skip-permissions" || len(launch) != 2 {
		t.Errorf("LaunchArgs mutated its input: %q", launch)
	}
}

func TestTheRecipeCarriesUpstreamBehaviourEnv(t *testing.T) {
	t.Parallel()
	agent := AgentName("cursor")
	dir, err := writeProbeWorkload(t, agent, WorkloadV3{Version: "1", Capabilities: []CapabilityV3{{Type: CapSbx}}},
		RecipeV3{From: "proveo/cursor:local", Env: []string{"IS_SANDBOX=1", "AGENT_CLI_CREDENTIAL_STORE=memory"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, agent+".dockerfile"))
	for _, want := range []string{"ENV IS_SANDBOX=\"1\"\n", "ENV AGENT_CLI_CREDENTIAL_STORE=\"memory\"\n"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("recipe lacks %q:\n%s", want, got)
		}
	}
}

func TestProveoHooksAndHostsMergeIntoTheUpstreamSingletons(t *testing.T) {
	t.Parallel()
	k := upstreamKit(t, "claudecode", UpstreamOptions{})
	before := len(hooksOf(k))
	caps, err := WithLifecycle(k.Capabilities, LifecycleV3{Startup: []HookV3{{
		Command: []string{"/bin/sh", "-c", "seed"}, User: "1000", Background: true, Description: "proveo seed",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	caps, err = WithRuntimeAllow(caps, []string{"host.docker.internal:11434", "api.anthropic.com"})
	if err != nil {
		t.Fatal(err)
	}
	count := func(typ string) int {
		n := 0
		for _, c := range caps {
			if c.Type == typ {
				n++
			}
		}
		return n
	}
	if count(CapLifecycle) != 1 || count(CapNetworkPolicy) != 1 {
		t.Fatalf("lifecycle@1 ×%d, network-policy@1 ×%d; both are singletons", count(CapLifecycle), count(CapNetworkPolicy))
	}
	hooks := hooksOf(UpstreamKit{Capabilities: caps})
	if len(hooks) != before+1 {
		t.Fatalf("hooks = %d, want upstream's %d + proveo's 1", len(hooks), before)
	}
	if last := hooks[len(hooks)-1]; last["description"] != "proveo seed" || last["background"] != true {
		t.Errorf("proveo's hook = %v, want it last (after upstream's chown) and backgrounded", last)
	}
	for _, c := range caps {
		if c.Type != CapNetworkPolicy {
			continue
		}
		allow := c.Config.(map[string]any)["runtime"].(map[string]any)["allow"].([]any)
		n := 0
		for _, h := range allow {
			if h == "api.anthropic.com" {
				n++
			}
		}
		if n != 1 || !slices.Contains(allow, any("host.docker.internal:11434")) {
			t.Errorf("runtime allow = %v; want the add-on host added once and no duplicate", allow)
		}
	}
	if fresh, _ := WithRuntimeAllow(nil, []string{"x"}); len(fresh) != 1 || fresh[0].Type != CapNetworkPolicy {
		t.Errorf("WithRuntimeAllow on a kit with no policy = %+v, want one network-policy@1", fresh)
	}
}

func TestKitFilesLandBesideTheDescriptorAndNowhereElse(t *testing.T) {
	t.Parallel()
	agent := AgentName("claudecode")
	dir, err := WriteWorkloadV3(KitDirV3(t.TempDir(), agent), WorkloadV3{Version: "1", Capabilities: []CapabilityV3{{Type: CapSbx}}},
		RecipeV3{From: "x"}, map[string][]byte{"claude-context.md": []byte("ctx")})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "claude-context.md")); string(b) != "ctx" {
		t.Errorf("claude-context.md beside the descriptor = %q", b)
	}
	for _, bad := range []string{"../escape.md", "sub/x.md", agent + ".yaml"} {
		if _, err := WriteWorkloadV3(KitDirV3(t.TempDir(), agent), WorkloadV3{Version: "1"}, RecipeV3{From: "x"},
			map[string][]byte{bad: []byte("x")}); err == nil {
			t.Errorf("kit file %q was written; only plain names beside the descriptor are allowed", bad)
		}
	}
}

func TestLaunchedIsWhatSbxRunStarts(t *testing.T) {
	t.Parallel()
	ep := []string{"dumb-init", "--", "/entrypoint.sh"}
	for _, tc := range []struct {
		name                     string
		entrypoint, cmd, command []string
		want                     []string
	}{
		{"entrypoint alone", ep, nil, nil, ep},
		{"entrypoint then CMD", []string{"dumb-init", "--", "cecli-entrypoint"}, []string{"cecli"}, nil,
			[]string{"dumb-init", "--", "cecli-entrypoint", "cecli"}},
		{"trailing command replaces all after argv0", ep, []string{"cecli"}, []string{"--", "/entrypoint.sh", "--yolo"},
			[]string{"dumb-init", "--", "/entrypoint.sh", "--yolo"}},
		{"no entrypoint", nil, []string{"x"}, nil, nil},
	} {
		if got := Launched(tc.entrypoint, tc.cmd, tc.command); !slices.Equal(got, tc.want) {
			t.Errorf("%s: Launched = %v, want %v", tc.name, got, tc.want)
		}
	}
}
