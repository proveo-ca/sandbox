//go:build !windows

// SPEC: _spec/_plans/claudecode-mcp-toggle.puml
package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/hostdev"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestClaudeMCPModeControlsLaunchArgsAndLeavesADB(t *testing.T) {
	for _, on := range []bool{false, true} {
		in := specInput("claudecode", "--resume", "conversation")
		in.MCPGateway = on
		in.AgentEnv = []string{hostdev.EnvADBPort + "=5037"}
		cfg, _, _ := Spec(in)
		args := sbx.RunArgs(sbx.Reattach(cfg))
		if slices.Contains(args, claudeStrictMCP) == on ||
			slices.Contains(args, "--mcp-config") != on ||
			!slices.Contains(args, "--resume") {
			t.Errorf("MCP on=%t: effective arguments %q", on, args)
		}
		if !slices.Contains(cfg.Env, hostdev.EnvADBPort+"=5037") {
			t.Errorf("MCP on=%t removed ADB: %q", on, cfg.Env)
		}
	}
}

func TestClaudeMCPOffRejectsExplicitConfig(t *testing.T) {
	for _, arg := range []string{"--mcp-config", "--mcp-config=fixture.json", "--no-strict-mcp-config", "--strict-mcp-config=false"} {
		in := specInput("claudecode", arg)
		if err := in.ValidateLaunch(); err == nil || !strings.Contains(err.Error(), "MCP is off") {
			t.Errorf("%q was accepted with MCP off: %v", arg, err)
		}
		in.MCPGateway = true
		if err := in.ValidateLaunch(); err != nil {
			t.Errorf("%q with MCP on: %v", arg, err)
		}
	}
}

func TestClaudeLaunchRequiresAStoppedKeptSandbox(t *testing.T) {
	if err := stoppedForClaudeLaunch("test", &sbx.Listed{Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	for _, listed := range []*sbx.Listed{nil, {Status: "running"}, {Status: "starting"}} {
		if err := stoppedForClaudeLaunch("test", listed); err == nil {
			t.Errorf("%+v was accepted as stopped", listed)
		}
	}
}

func TestClaudeConfigRefreshUsesTheKeptMount(t *testing.T) {
	home, first, second := t.TempDir(), t.TempDir(), t.TempDir()
	spec := manifest.Home{Enabled: true, Files: []string{".claude.json"}}
	write := func(dir, content string, at time.Time) {
		t.Helper()
		path := filepath.Join(dir, ".claude.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().Add(-time.Minute)
	write(home, "original", at)
	kept, err := PrepareHomeAccess(home, first, spec)
	if err != nil {
		t.Fatal(err)
	}
	current, err := PrepareHomeAccess(home, second, spec)
	if err != nil {
		t.Fatal(err)
	}
	was := &launchReceipt{KitDir: filepath.Join(first, "sbx", "kit", "proveo-claudecode")}
	write(home, "edited", at.Add(time.Second))
	calls := fakeSbx(t, guestDirect, 0, bundleEmpty)
	reused, err := prepareKeptClaude(Input{HomeAccess: current, MCPGateway: true}, "fixture", was)
	if err != nil {
		t.Fatal(err)
	}
	if reused.FilesRoot != kept.FilesRoot {
		t.Fatalf("stage = %q, want %q", reused.FilesRoot, kept.FilesRoot)
	}
	if got := callsOf(t, calls); !strings.Contains(got, kept.FilesRoot) ||
		!strings.Contains(got, "proveo_sync_config restore") || strings.Contains(got, current.FilesRoot) {
		t.Errorf("Claude Code config restore used the wrong mount: %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(reused.FilesRoot, ".claude.json")); string(b) != "edited" {
		t.Errorf("kept sandbox would restore %q", b)
	}
	write(reused.FilesRoot, "guest edit", at.Add(2*time.Second))
	if err := reused.Commit(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude.json")); string(b) != "guest edit" {
		t.Errorf("commit from the kept mount left %q", b)
	}
}
