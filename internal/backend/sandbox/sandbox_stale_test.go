package sandbox

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/sbx"
)

// A failed run is kept for diagnosis and the next run over that workspace
// derives the same name, so proveo used to hand sbx a sandbox it refuses to
// give workspaces to — and the operator had to remove it by hand between every
// attempt. SPEC: _spec/internal/sbx/sandbox-backend.puml
func TestAnExistingSandboxIsReattachedNotRecreated(t *testing.T) {
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
	}
	got := reuseOrCreate(cfg, func(string) bool { return true })

	for _, bad := range []struct {
		what string
		got  bool
	}{
		{"workspaces", len(got.Mounts) > 0},
		{"a template", got.Image != ""},
		{"published ports", len(got.Publish) > 0},
		{"a memory size", got.Memory != ""},
		{"a cpu count", got.CPUs > 0},
	} {
		if bad.got {
			t.Errorf("re-attach still passes %s; sbx refuses those on a sandbox that exists", bad.what)
		}
	}
	if got.Name != cfg.Name || got.KitDir != cfg.KitDir || got.Agent != cfg.Agent {
		t.Errorf("re-attach lost the identity it must keep: %+v", got)
	}
	if len(got.Env) != len(cfg.Env) {
		t.Errorf("re-attach dropped the environment: %v", got.Env)
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
