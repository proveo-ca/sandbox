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
	return WriteWorkloadV3(KitDirV3(t.TempDir(), agent), w, r)
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
