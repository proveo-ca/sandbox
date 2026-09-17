package sandbox

import (
	"reflect"
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// A failed run is kept for diagnosis and the next run over that workspace
// derives the same name, so proveo used to hand sbx a sandbox it refuses to
// give workspaces to — and the operator had to remove it by hand between every
// attempt. SPEC: _spec/internal/sbx/kit-lifecycle.puml
func TestAnExistingSandboxIsReattachedWithNameAndAgentArgsOnly(t *testing.T) {
	t.Parallel()
	cfg := sbx.RunConfig{
		Name:    "proveo-cursor-177e7812",
		KitDir:  "/kit",
		Image:   "proveo/cursor:local",
		Memory:  "8192m",
		CPUs:    14,
		Clone:   true,
		Publish: []string{"127.0.0.1:1234:9222"},
		Mounts:  []sbx.Mount{{Host: "/w"}},
		Env:     []string{"HOME=/proveo-home"},
		Agent:   "cursor",
		Command: []string{"--resume", "thread-1"},
	}
	got := reuseOrCreate(cfg, func(string) bool { return true })

	want := []string{"run", "--name", cfg.Name, "--", "--resume", "thread-1"}
	if args := sbx.RunArgs(got); !reflect.DeepEqual(args, want) {
		t.Errorf("re-attach args = %q, want the existing name plus agent args only %q", args, want)
	}
}

func TestAFreshNameIsCreatedWithEverything(t *testing.T) {
	t.Parallel()
	cfg := sbx.RunConfig{
		Name:   "proveo-cursor-aaaaaaaa",
		Image:  "proveo/cursor:local",
		Mounts: []sbx.Mount{{Host: "/w"}},
	}
	got := reuseOrCreate(cfg, func(string) bool { return false })
	if len(got.Mounts) == 0 || got.Image == "" {
		t.Errorf("a sandbox that does not exist yet must be created with its workspace and template: %+v", got)
	}
}
