package clean

import (
	"sort"
	"strings"
	"testing"
)

func joined(ss []string) string {
	sort.Strings(ss)
	return strings.Join(ss, ",")
}

// A dead session (no running container) is fully reclaimed; a live one is kept.
func TestBuildPlanRoutineSkipsLive(t *testing.T) {
	t.Parallel()
	inv := Inventory{
		Egress: []Container{
			{Name: "dead-squid", Session: "dead", Running: false},
			{Name: "live-squid", Session: "live", Running: true},
			{Name: "live-egress", Session: "live", Running: true},
		},
		Dind: []Container{
			{Name: "proveo-dind-opencode", Running: false},
			{Name: "proveo-dind-cursor", Running: true},
		},
		Networks: []Net{
			{Name: "dead-net", Session: "dead", HasEndpoints: false},
			{Name: "live-net", Session: "live", HasEndpoints: true},
		},
		StateDirs: []string{"dead", "live", "orphan"},
	}

	p := BuildPlan(inv, Options{})

	if got := joined(p.Containers); got != "dead-squid,proveo-dind-opencode" {
		t.Errorf("containers = %q, want the dead egress + exited dind only", got)
	}
	if got := joined(p.Networks); got != "dead-net" {
		t.Errorf("networks = %q, want dead-net only (live has endpoints)", got)
	}
	// "orphan" state dir has no live container → reclaimed; "dead" too; "live" kept.
	if got := joined(p.StateDirs); got != "dead,orphan" {
		t.Errorf("stateDirs = %q, want dead,orphan", got)
	}
	if len(p.Images) != 0 {
		t.Errorf("routine must not touch images, got %v", p.Images)
	}
	if joined(p.SkippedLive) != "container live-egress,container live-squid,container proveo-dind-cursor,network live-net,state live" {
		t.Errorf("SkippedLive = %v", p.SkippedLive)
	}
}

// --force removes the live-looking resources too.
func TestBuildPlanForce(t *testing.T) {
	t.Parallel()
	inv := Inventory{
		Egress:    []Container{{Name: "live-squid", Session: "live", Running: true}},
		Networks:  []Net{{Name: "live-net", Session: "live", HasEndpoints: true}},
		StateDirs: []string{"live"},
	}
	p := BuildPlan(inv, Options{Force: true})
	if len(p.SkippedLive) != 0 {
		t.Errorf("force should skip nothing, got %v", p.SkippedLive)
	}
	if joined(p.Containers) != "live-squid" || joined(p.Networks) != "live-net" || joined(p.StateDirs) != "live" {
		t.Errorf("force didn't sweep everything: %+v", p)
	}
}

// --deep adds proveo/* images; routine leaves them.
func TestBuildPlanDeep(t *testing.T) {
	t.Parallel()
	inv := Inventory{Images: []string{"proveo/base:latest", "proveo/claudecode:latest"}}
	if p := BuildPlan(inv, Options{}); len(p.Images) != 0 {
		t.Errorf("routine must not remove images, got %v", p.Images)
	}
	if p := BuildPlan(inv, Options{Deep: true}); joined(p.Images) != "proveo/base:latest,proveo/claudecode:latest" {
		t.Errorf("deep images = %v", p.Images)
	}
}

func TestBuildPlanLeavesToolchainsUnlessAsked(t *testing.T) {
	t.Parallel()
	inv := Inventory{ToolDirs: []ToolDir{{Path: "/h/.local/share/mise", Bytes: 100}}}
	if p := BuildPlan(inv, Options{}); len(p.ToolDirs) != 0 {
		t.Errorf("routine clean removed toolchains: %v", p.ToolDirs)
	}
	if p := BuildPlan(inv, Options{Deep: true}); len(p.ToolDirs) != 0 {
		t.Errorf("--deep removed toolchains without --tools: %v", p.ToolDirs)
	}
	p := BuildPlan(inv, Options{Tools: true})
	if len(p.ToolDirs) != 1 || p.ToolDirs[0] != "/h/.local/share/mise" {
		t.Errorf("--tools should remove the toolchain, got %v", p.ToolDirs)
	}
}

func TestBuildPlanToolchainsHeldBackByAnyRunningContainer(t *testing.T) {
	t.Parallel()
	dirs := []ToolDir{{Path: "/h/.local/share/mise", Bytes: 100}}
	for _, tc := range []struct {
		name string
		inv  Inventory
	}{
		{"running egress", Inventory{
			Egress:   []Container{{Name: "eg", Running: true, Session: "s1"}},
			ToolDirs: dirs,
		}},
		{"running dind", Inventory{
			Dind:     []Container{{Name: "proveo-dind-1", Running: true}},
			ToolDirs: dirs,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := BuildPlan(tc.inv, Options{Tools: true})
			if len(p.ToolDirs) != 0 {
				t.Errorf("removed a shared toolchain while a run is live: %v", p.ToolDirs)
			}
			if len(p.SkippedLive) == 0 {
				t.Error("skipping a toolchain must be reported, not silent")
			}
			if f := BuildPlan(tc.inv, Options{Tools: true, Force: true}); len(f.ToolDirs) != 1 {
				t.Errorf("--force should remove it anyway, got %v", f.ToolDirs)
			}
		})
	}
}

func TestBuildPlanToolchainsRemovedWhenNothingRuns(t *testing.T) {
	t.Parallel()
	inv := Inventory{
		Egress:   []Container{{Name: "eg", Running: false, Session: "s1"}},
		ToolDirs: []ToolDir{{Path: "/h/go", Bytes: 1}},
	}
	if p := BuildPlan(inv, Options{Tools: true}); len(p.ToolDirs) != 1 {
		t.Errorf("stopped containers must not block a toolchain prune, got %v", p.ToolDirs)
	}
}
