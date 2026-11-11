// SPEC: _spec/internal/promptkit/prompt-kit.puml
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/promptkit"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func TestThePromptRowDefaultsToNowAndSkipPromptLocksItOff(t *testing.T) {
	t.Parallel()
	plan := linuxPlan(t, sbx.DefaultPrefix(homeDir()))
	for _, c := range []struct {
		name   string
		o      initOptions
		want   bool
		locked bool
	}{
		{"by default", initOptions{}, true, false},
		{"--skip-prompt", initOptions{skipPrompt: true}, false, true},
	} {
		def := defaultDecisions(plan, "", c.o)
		if def.Prompt != c.want {
			t.Errorf("%s: Prompt = %v, want %v", c.name, def.Prompt, c.want)
		}
		found := false
		for _, r := range initRows(plan, "", def, c.o) {
			if r.Label != rowPrompt {
				continue
			}
			found = true
			if r.Locked != c.locked || (r.Options[r.Selected] == "now") != c.want {
				t.Errorf("%s: row = %+v", c.name, r)
			}
			if !strings.Contains(r.Help["now"], "REPLACES this shell's prompt") {
				t.Errorf("the row does not say starship replaces the prompt: %q", r.Help["now"])
			}
		}
		if !found {
			t.Errorf("%s: no prompt row", c.name)
		}
		if got := describeDecisions(def); !strings.Contains(got, "prompt: "+yesNo(c.want)) {
			t.Errorf("%s: the decisions line does not say what was chosen for the prompt: %s", c.name, got)
		}
	}
	prev := defaultDecisions(plan, "", initOptions{})
	if again := carried(defaultDecisions(plan, "", initOptions{}), &prev); again.Prompt {
		t.Error("a revisit of the page would install the prompt kit a second time")
	}
}

func TestPromptInitPrintsEachShellsLineAndRefusesAnUnknownShell(t *testing.T) {
	for shellName, want := range map[string]string{
		"bash":          `eval "$(starship init bash)"`,
		"/bin/zsh":      `eval "$(starship init zsh)"`,
		"/usr/bin/fish": `starship init fish | source`,
	} {
		cmd := promptCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Flags().Set("init", shellName); err != nil {
			t.Fatal(err)
		}
		if err := cmd.RunE(cmd, nil); err != nil || strings.TrimSpace(out.String()) != want {
			t.Errorf("prompt --init %s = %q, %v; want %q", shellName, out.String(), err, want)
		}
	}
	cmd := promptCmd()
	_ = cmd.Flags().Set("init", "powershell")
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Error("prompt --init accepted a shell proveo writes no line for")
	}
}

func TestUninstallTakesThePromptKitOutAndLeavesTheOperatorsConfig(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".proveo")
	state := filepath.Join(home, "state")
	t.Setenv("HOME", home)
	t.Setenv("PROVEO_INSTALL_ROOT", root)
	t.Setenv("PROVEO_STATE_ROOT", state)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("STARSHIP_CONFIG", "")
	t.Setenv("SHELL", "/bin/zsh")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	const theirConfig = "format = \"$all\"\n"
	config := filepath.Join(home, ".config", "starship.toml")
	rc := filepath.Join(home, ".zshrc")
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(theirConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rc, []byte("export EDITOR=vim\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := promptKitOptions(false)
	o.LookPath = func(string) (string, error) { return "/usr/local/bin/starship", nil }
	o.Fetch = func(string) ([]byte, error) { t.Fatal("the test reached the network"); return nil, nil }
	fonts := filepath.Join(home, "Library", "Fonts")
	if err := os.MkdirAll(fonts, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{fonts, filepath.Join(home, ".local", "share", "fonts")} {
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "HackNerdFont-Regular.ttf"), nil, 0o644)
	}
	if _, err := promptkit.Apply(o); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(rc); !strings.Contains(string(b), "starship init zsh") {
		t.Fatalf("the kit did not add the init line: %q", b)
	}

	if err := doUninstall(true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(config); string(b) != theirConfig {
		t.Errorf("uninstall left the starship config as %q, want the operator's own %q", b, theirConfig)
	}
	if b, _ := os.ReadFile(rc); string(b) != "export EDITOR=vim\n" {
		t.Errorf("uninstall left the rc as %q", b)
	}
	if _, err := os.Stat(filepath.Join(state, "prompt-kit.json")); !os.IsNotExist(err) {
		t.Error("uninstall left the kit's record")
	}
}
