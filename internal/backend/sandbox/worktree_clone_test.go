// SPEC: _spec/internal/sbx/clone-workspace.puml
package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/workspace"
)

func TestSpecClonesALinkedWorktreeThroughItsMainWorktree(t *testing.T) {
	t.Parallel()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	main, wt := filepath.Join(root, "main"), filepath.Join(root, "wt")
	for _, d := range []string{filepath.Join(main, ".git"), filepath.Join(wt, "reports")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	in := Input{
		Target: "claudecode", Man: manifest.Manifest{Name: "claudecode"},
		Sid: "proveo-1-2", EgDir: t.TempDir(), Lookup: func(string) string { return "" },
		Clone: true, RepoRoot: wt, OutputDir: filepath.Join(wt, "reports"),
		CloneSource:      workspace.WorktreeSource{Main: main, Ref: "feat"},
		WorktreeFallback: true, WorktreeEnv: []string{"GIT_DIR=/proveo-git/worktrees/wt", "GIT_WORK_TREE=/app"},
		Mounts: []runner.Mount{
			{Host: wt, Container: "/app"},
			{Host: filepath.Join(main, ".git"), Container: workspace.ContainerGitCommonDir},
			{Host: filepath.Join(wt, "reports"), Container: "/app/reports"},
		},
	}
	cfg, _, _ := Spec(in)

	var hosts []string
	for _, m := range cfg.Mounts {
		hosts = append(hosts, m.Host)
	}
	if FirstHost(cfg.Mounts) != main {
		t.Fatalf("primary workspace = %q, want the main worktree %s — sbx clones only the primary: %v", FirstHost(cfg.Mounts), main, hosts)
	}
	for _, banned := range []string{wt, filepath.Join(wt, "reports")} {
		if slices.Contains(hosts, banned) {
			t.Errorf("%s is still mounted live — the host tree it carries is what a Linux install rewrites: %v", banned, hosts)
		}
	}
	env := strings.Join(cfg.Env, "\n")
	if !strings.Contains(env, CloneRefVar+"=feat") {
		t.Errorf("the seed is not told which ref to check out:\n%s", env)
	}
	if strings.Contains(env, "GIT_DIR=") {
		t.Errorf("GIT_DIR points git at the host's shared .git instead of the clone's own:\n%s", env)
	}
	if !strings.Contains(env, "PROVEO_WORKDIR="+wt) || !strings.Contains(env, CloneMainVar+"="+main) {
		t.Errorf("the agent must work at the worktree's own path, added to the clone at %s:\n%s", main, env)
	}
	if cfg.Name != sbx.SandboxName(in.Target, wt) {
		t.Errorf("name %s is keyed by the main worktree, so this run would reattach to the main checkout's sandbox", cfg.Name)
	}
	if !cfg.Clone {
		t.Error("clone flag dropped")
	}
}

func TestSpecMountsTheStagedEnvReadOnlyOnlyForAClone(t *testing.T) {
	t.Parallel()
	repo, stage := t.TempDir(), t.TempDir()
	staged := filepath.Join(stage, ".env")
	base := Input{
		Target: "claudecode", Man: manifest.Manifest{Name: "claudecode"},
		Sid: "proveo-1-2", EgDir: t.TempDir(), Lookup: func(string) string { return "" },
		RepoRoot: repo, CloneEnv: staged,
		Mounts: []runner.Mount{{Host: repo, Container: "/app"}},
	}
	clone := base
	clone.Clone = true
	cfg, _, _ := Spec(clone)
	var found bool
	for _, m := range cfg.Mounts {
		if m.Host == stage {
			found = m.ReadOnly
		}
	}
	if !found {
		t.Errorf("the staged .env dir is not mounted read-only: %+v", cfg.Mounts)
	}
	if !strings.Contains(strings.Join(cfg.Env, "\n"), CloneEnvVar+"="+staged) {
		t.Errorf("the seed is not told where the staged .env is")
	}
	cfg, _, _ = Spec(base)
	if strings.Contains(strings.Join(cfg.Env, "\n"), CloneEnvVar) {
		t.Error("a mounted checkout already carries its .env; linking one over it is wrong")
	}
}

func TestWorktreeCloneKitCreatesTheWorktreeDirAsRootFirst(t *testing.T) {
	t.Parallel()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	in := Input{Clone: true, RepoRoot: filepath.Join(root, "wt"), CloneSource: workspace.WorktreeSource{Main: filepath.Join(root, "main"), Ref: "feat"}}
	cmds := startupCommands(in, true)
	if len(cmds) < 2 || cmds[0].User != "root" || !slices.Contains(cmds[0].Command, filepath.Join(root, "wt")) {
		t.Fatalf("first startup command = %+v — sbx makes mount-point parents root-owned, so only root can create the worktree dir", cmds)
	}
	if !sbx.CommandNamesSeed(cmds[1].Command) {
		t.Errorf("the seed must follow the dir step: %+v", cmds[1])
	}
	if got := startupCommands(Input{Clone: true, RepoRoot: root}, true); got[0].User == "root" {
		t.Errorf("a plain clone needs no root step: %+v", got[0])
	}
}

func TestCloneLinksCarriesOnlyTheMountedLinksOfAClone(t *testing.T) {
	t.Parallel()
	links := []workspace.Link{
		{Rel: "_spec", Target: "/Users/op/Syncd/_spec", Action: workspace.LinkMounted},
		{Rel: ".env", Target: "/Users/op/repo/.env", Action: workspace.LinkEnvPolicy},
		{Rel: "secrets", Target: "/etc", Action: workspace.LinkRefused},
	}
	if got := cloneLinks(Input{Clone: true, Links: links}); got != "_spec=/Users/op/Syncd/_spec" {
		t.Errorf("cloneLinks = %q — only a link whose target is mounted can be recreated; .env has its own policy", got)
	}
	if got := cloneLinks(Input{Links: links}); got != "" {
		t.Errorf("a mounted checkout already carries its symlinks, got %q", got)
	}
}

func TestOwnAgentCommandKeepsTheEntrypointScriptAheadOfTrailingArgs(t *testing.T) {
	t.Parallel()
	ep := []string{"dumb-init", "--", "/entrypoint.sh"}
	if got := OwnAgentCommand(ep, []string{"chat", "-q", "PONG"}); !slices.Equal(got, []string{"/entrypoint.sh", "chat", "-q", "PONG"}) {
		t.Errorf("= %v — sbx drops the entrypoint's part after --, so dumb-init ran `chat` itself (\"[dumb-init] chat: No such file or directory\")", got)
	}
	if got := OwnAgentCommand(ep, nil); got != nil {
		t.Errorf("no trailing args must leave sbx's default launch alone, got %v", got)
	}
	if got := OwnAgentCommand([]string{"/entrypoint.sh"}, []string{"x"}); !slices.Equal(got, []string{"x"}) {
		t.Errorf("an entrypoint without -- has no tail to restore, got %v", got)
	}
}

func TestSpecTellsTheSeedAnAssistantNeedsNoToolchain(t *testing.T) {
	t.Parallel()
	base := Input{Target: "hermes", Sid: "proveo-1-2", EgDir: t.TempDir(), Lookup: func(string) string { return "" }}
	base.Man = manifest.Manifest{Name: "hermes", Kind: manifest.KindAssistant}
	cfg, _, _ := Spec(base)
	if !strings.Contains(strings.Join(cfg.Env, "\n"), "PROVEO_AGENT_KIND=assistant") {
		t.Errorf("the seed cannot tell hermes is an assistant:\n%s", strings.Join(cfg.Env, "\n"))
	}
	base.Man = manifest.Manifest{Name: "codex"}
	cfg, _, _ = Spec(base)
	if strings.Contains(strings.Join(cfg.Env, "\n"), "PROVEO_AGENT_KIND") {
		t.Error("a coding harness was marked an assistant")
	}
}
