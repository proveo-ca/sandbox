// SPEC: _spec/internal/sbx/ide-attach.puml
package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestHomeAccessExposesOnlyThisHarnessState(t *testing.T) {
	t.Parallel()
	root, runDir := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude.json"), []byte("host"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := manifest.Home{
		Enabled: true,
		Mounts: []manifest.HomeMount{{
			Host: ".claude", Container: "/proveo-home/.claude",
		}},
		Files: []string{".claude.json"},
	}
	a, err := PrepareHomeAccess(root, runDir, h)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Cleanup()

	got := map[string]bool{}
	for _, m := range a.Mounts {
		got[filepath.Clean(m.Host)] = true
	}
	for _, want := range []string{
		filepath.Join(root, ".claude"),
		filepath.Join(root, "toolchains"),
		a.FilesRoot,
	} {
		if !got[want] {
			t.Errorf("narrow home mounts omit %s: %+v", want, a.Mounts)
		}
	}
	for _, forbidden := range []string{root, filepath.Join(root, "logs")} {
		if got[forbidden] {
			t.Errorf("narrow home mounted %s; an IDE could read unrelated run logs", forbidden)
		}
	}
	b, err := os.ReadFile(filepath.Join(a.FilesRoot, ".claude.json"))
	if err != nil || string(b) != "host" {
		t.Fatalf("home-root config was not staged: %q, %v", b, err)
	}
}

func TestHomeAccessCommitUsesNewestFile(t *testing.T) {
	t.Parallel()
	root, runDir := t.TempDir(), t.TempDir()
	host := filepath.Join(root, ".claude.json")
	if err := os.WriteFile(host, []byte("host"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := PrepareHomeAccess(root, runDir, manifest.Home{
		Enabled: true, Files: []string{".claude.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Cleanup()
	staged := filepath.Join(a.FilesRoot, ".claude.json")
	future := time.Now().Add(time.Minute)
	if err := os.WriteFile(staged, []byte("sandbox"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staged, future, future); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(host); err != nil || string(b) != "sandbox" {
		t.Fatalf("newer sandbox config was not committed: %q, %v", b, err)
	}

	newer := future.Add(time.Minute)
	if err := os.WriteFile(host, []byte("operator"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(host, newer, newer); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(host); err != nil || string(b) != "operator" {
		t.Fatalf("commit overwrote a newer host edit: %q, %v", b, err)
	}
}

func TestOpenCodeHomeAccessSharesOnlyDeclaredV2History(t *testing.T) {
	t.Parallel()
	root, runDir := t.TempDir(), t.TempDir()
	unselected := filepath.Join(root, "other-history")
	if err := os.MkdirAll(unselected, 0o700); err != nil {
		t.Fatal(err)
	}
	h := manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{
		{Host: "opencode/v2/config", Container: "/proveo-home/.config/opencode"},
		{Host: "opencode/v2/share", Container: "/proveo-home/.local/share/opencode"},
	}}
	a, err := PrepareHomeAccess(root, runDir, h)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "opencode/v2/config"), filepath.Join(root, "opencode/v2/share"), filepath.Join(root, "toolchains")}
	if len(a.Mounts) != len(want) {
		t.Fatalf("unexpected sharing plan: %+v", a.Mounts)
	}
	for _, host := range want {
		if !slices.Contains(a.Mounts, runner.Mount{Host: host}) {
			t.Errorf("sharing plan omits %s: %+v", host, a.Mounts)
		}
	}
	cfg, _, _ := Spec(Input{Target: "opencode", Man: manifest.Manifest{Name: "opencode", Home: h}, Sid: "proveo-fixture",
		EgDir: runDir, HomeRoot: root, HomeAccess: a, Lookup: func(string) string { return "" },
		Mounts: []runner.Mount{{Host: root, Container: "/proveo-home"}},
	})
	for _, m := range cfg.Mounts {
		if m.Host == root || m.Host == unselected || m.Host == filepath.Join(root, ".local") || m.Host == filepath.Join(root, ".local/share") || m.Host == filepath.Join(root, "logs") {
			t.Fatalf("history sharing widened the home view: %+v", cfg.Mounts)
		}
	}
}

func TestOpenCodeHomeAccessDoesNotCreateNativeHistoryInHostHome(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, err := PrepareHomeAccess(root, t.TempDir(), manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{{
		Host: "opencode/v2/share", Container: "/proveo-home/.local/share/opencode",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".local/share/opencode")); !os.IsNotExist(err) {
		t.Fatalf("preparation created an obsolete data tree: %v", err)
	}
}

func TestSpecReplacesTheWholeProveoHomeWithNarrowBinds(t *testing.T) {
	t.Parallel()
	root, repo, runDir := t.TempDir(), t.TempDir(), t.TempDir()
	h := manifest.Home{
		Enabled: true,
		Mounts: []manifest.HomeMount{{
			Host: ".cursor", Container: "/proveo-home/.cursor",
		}},
	}
	a, err := PrepareHomeAccess(root, runDir, h)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Cleanup()
	cfg, _, _ := Spec(Input{
		Target: "cursor",
		Man:    manifest.Manifest{Name: "cursor", Home: h},
		Sid:    "proveo-1-2",
		EgDir:  runDir,
		Mounts: []runner.Mount{
			{Host: repo, Container: "/app"},
			{Host: root, Container: "/proveo-home"},
		},
		HomeRoot:   root,
		HomeAccess: a,
		Lookup:     func(string) string { return "" },
	})
	var hosts []string
	for _, m := range cfg.Mounts {
		hosts = append(hosts, m.Host)
	}
	if slices.Contains(hosts, root) {
		t.Fatalf("whole proveo home is still mounted: %v", hosts)
	}
	for _, want := range []string{repo, filepath.Join(root, ".cursor"), filepath.Join(root, "toolchains")} {
		if !slices.Contains(hosts, want) {
			t.Errorf("narrow binds omit %s: %v", want, hosts)
		}
	}
	joined := strings.Join(cfg.Env, "\n")
	if !strings.Contains(joined, sbx.StateHomeVar+"="+root) {
		t.Errorf("state sync lost the host root while narrowing mounts:\n%s", joined)
	}
}

func TestSpecCarriesTheScopedIndexThroughANarrowedHome(t *testing.T) {
	t.Parallel()
	root, runDir := t.TempDir(), t.TempDir()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "a"}} {
		if args[0] == "add" {
			for _, f := range []string{"apps/web/a.ts", "packages/lib/b.ts"} {
				if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(f)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	h := manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{{Host: ".config/opencode", Container: "/proveo-home/.config/opencode"}}}
	a, err := PrepareHomeAccess(root, runDir, h)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Cleanup()
	cfg, _, _ := Spec(Input{
		Target: "opencode", Man: manifest.Manifest{Name: "opencode", Home: h},
		Sid: "proveo-1-2", EgDir: runDir, Lookup: func(string) string { return "" },
		RepoRoot: repo, ScopeRel: "apps/web",
		Mounts: []runner.Mount{
			{Host: filepath.Join(repo, "apps", "web"), Container: "/app/apps/web"},
			{Host: root, Container: "/proveo-home"},
		},
		HomeRoot: root, HomeAccess: a,
	})
	dir := filepath.Join(root, "git-index")
	var mounted bool
	for _, m := range cfg.Mounts {
		mounted = mounted || m.Host == dir
	}
	env := strings.Join(cfg.Env, "\n")
	if !mounted || !strings.Contains(env, "GIT_INDEX_FILE="+filepath.Join(dir, "proveo-1-2")) {
		t.Fatalf("the scoped index is not reachable in the sandbox (mounted=%v) — git reads every unmounted path as deleted:\n%s", mounted, env)
	}
	idx := exec.Command("git", "-C", repo, "ls-files", "-v")
	idx.Env = append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(dir, "proveo-1-2"))
	b, _ := idx.Output()
	if !strings.Contains(string(b), "S packages/lib/b.ts") || strings.Contains(string(b), "S apps/web/a.ts") {
		t.Errorf("the scoped index must hide only unmounted paths:\n%s", b)
	}
}

func TestHomeAccessMountsALinkedDirReadOnly(t *testing.T) {
	t.Parallel()
	root, runDir, repo := t.TempDir(), t.TempDir(), t.TempDir()
	data := filepath.Join(root, "hermes", "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, filepath.Join(data, "skills-muse")); err != nil {
		t.Fatal(err)
	}
	a, err := PrepareHomeAccess(root, runDir, manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{{Host: "hermes/data"}}})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(repo)
	if !slices.Contains(a.Mounts, runner.Mount{Host: want, ReadOnly: true}) {
		t.Errorf("mounts %+v: a declared dir's link out of the home view must mount its target read-only at its host path", a.Mounts)
	}
}

func TestHomeAccessRefusesLinksThatWidenTheView(t *testing.T) {
	t.Parallel()
	root, runDir, other := t.TempDir(), t.TempDir(), t.TempDir()
	data := filepath.Join(root, "hermes", "data")
	for _, d := range []string{filepath.Join(data, "deep"), filepath.Join(root, "toolchains", "go"), filepath.Join(root, "logs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(other, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		filepath.Join(data, "home"):           root,                                    // contains the proveo home
		filepath.Join(data, "slash"):          string(filepath.Separator),              // the whole host
		filepath.Join(data, "top"):            "/usr",                                  // a top-level dir
		filepath.Join(data, "logs"):           filepath.Join(root, "logs"),             // unmounted part of the proveo home
		filepath.Join(data, "file"):           file,                                    // not a directory
		filepath.Join(data, "go"):             filepath.Join(root, "toolchains", "go"), // already mounted
		filepath.Join(data, "deep", "nested"): other,                                   // below the first level
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	a, err := PrepareHomeAccess(root, runDir, manifest.Home{Enabled: true, Mounts: []manifest.HomeMount{{Host: "hermes/data"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range a.Mounts {
		if m.ReadOnly {
			t.Errorf("mounted %s: only a first-level link to a directory outside the home view may add a mount", m.Host)
		}
	}
}
