// SPEC: _spec/cmd/proveo/operator-name.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const houseRules = "# rules\n1. Address me as \"Executor\", as a prefix on every output message.\n2. Use active voice.\n"

func operatorLib(t *testing.T, name, script string) (home, rules, managed string) {
	t.Helper()
	home = t.TempDir()
	dir := t.TempDir()
	rules = filepath.Join(dir, "AGENTS.md")
	managed = filepath.Join(dir, "CLAUDE.md")
	for _, f := range []string{rules, managed} {
		if err := os.WriteFile(f, []byte(houseRules), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "packages", "lib", "entrypoint-lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const fixed = "readonly PROVEO_HOUSE_RULES_FILE=/opt/proveo/AGENTS.md"
	if !strings.Contains(string(src), fixed) {
		t.Fatalf("the lib no longer pins %q; update this test's fixture swap", fixed)
	}
	lib := filepath.Join(dir, "entrypoint-lib.sh")
	if err := os.WriteFile(lib, []byte(strings.Replace(string(src), fixed, `PROVEO_HOUSE_RULES_FILE="$R"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bashOrSkip(t), "-c", `source "$1"; `+script, "bash", lib)
	cmd.Env = append(os.Environ(), "PROVEO_HOME=", "HOME="+home, "PROVEO_OPERATOR_NAME="+name,
		"R="+rules, "PROVEO_CLAUDE_MANAGED_MD="+managed)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return home, rules, managed
}

func TestHouseRulesAddressTheStoredName(t *testing.T) {
	for _, target := range []string{"codex", "opencode"} {
		home, _, _ := operatorLib(t, "Roberto", `proveo_compose_house_rules `+target)
		rel := map[string]string{"codex": ".codex/AGENTS.md", "opencode": ".config/opencode/AGENTS.md"}[target]
		b, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		if !strings.Contains(string(b), `Address me as "Roberto"`) || strings.Contains(string(b), "Executor") {
			t.Errorf("%s house rules:\n%s", target, b)
		}
		if !strings.Contains(string(b), "Use active voice.") {
			t.Errorf("%s lost the other rules:\n%s", target, b)
		}
	}
}

func TestHouseRulesKeepExecutorWithoutAName(t *testing.T) {
	home, _, managed := operatorLib(t, "", `proveo_compose_house_rules codex; proveo_address_operator_claude claudecode`)
	b, _ := os.ReadFile(filepath.Join(home, ".codex/AGENTS.md"))
	if !strings.Contains(string(b), `Address me as "Executor"`) {
		t.Errorf("no name ⇒ the default must stay:\n%s", b)
	}
	if m, _ := os.ReadFile(managed); string(m) != houseRules {
		t.Errorf("no name ⇒ claudecode's managed CLAUDE.md must be untouched:\n%s", m)
	}
}

func TestClaudeManagedRulesAddressTheStoredName(t *testing.T) {
	_, rules, managed := operatorLib(t, "Roberto", `proveo_address_operator_claude claudecode; proveo_address_operator_claude codex`)
	m, _ := os.ReadFile(managed)
	if !strings.Contains(string(m), `Address me as "Roberto"`) || !strings.Contains(string(m), "Use active voice.") {
		t.Errorf("claudecode managed CLAUDE.md:\n%s", m)
	}
	if r, _ := os.ReadFile(rules); string(r) != houseRules {
		t.Errorf("the baked source must stay the template:\n%s", r)
	}
}

func TestOperatorNameCannotBreakOutOfTheQuotes(t *testing.T) {
	home, _, _ := operatorLib(t, "R&D \"x\" \\y`z", `proveo_compose_house_rules codex`)
	b, _ := os.ReadFile(filepath.Join(home, ".codex/AGENTS.md"))
	if !strings.Contains(string(b), `Address me as "R&D x yz"`) {
		t.Errorf("hostile name ⇒\n%s", b)
	}
}
