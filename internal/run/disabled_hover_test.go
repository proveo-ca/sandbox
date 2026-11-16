// SPEC: _spec/internal/choiceui/wizard-keys.puml, _spec/internal/choiceui/wireframe.puml
package run

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/devports"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// runFormCase is the run form for one harness, one set of host keys and one egress reading.
type runFormCase struct {
	name string
	man  manifest.Manifest
	form *choiceui.Form
}

// runFormCases is the run form for every harness, with no key, a provider key, the harness's own
// plan key and both, under a readable and an unreadable egress baseline; its credentials row holds
// the inventory promptChoices builds from those keys, an SSH key and a .env file.
func runFormCases(t *testing.T) []runFormCase {
	t.Helper()
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	orig := policyBaseline
	t.Cleanup(func() { policyBaseline = orig })
	apps := []devports.AndroidApp{{Module: ":app", AppID: "com.example.app", Source: "app/build.gradle.kts"}}
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("DATABASE_URL=postgres://db\nNODE_ENV=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out []runFormCase
	for _, man := range ms {
		plan := map[string]string{}
		for _, e := range man.Env {
			if e.Secret {
				plan[e.Name] = "plan-token"
			}
		}
		provider := map[string]string{"ANTHROPIC_API_KEY": "sk-ant", "OPENAI_API_KEY": "sk-oai"}
		for _, keys := range []struct {
			name string
			env  map[string]string
		}{
			{"no keys", nil},
			{"a provider key", provider},
			{"a plan key", plan},
			{"both", mergedEnv(provider, plan)},
		} {
			for _, known := range []bool{true, false} {
				policyBaseline = func() (string, bool) { return sbx.BaselineBalanced, known }
				p := &Params{Target: man.Name, discoveredApps: apps}
				inv := inventoryOf(man, env(keys.env), nil, []string{"id_ed25519"}).withEnvFile(envFile)
				rows, _ := p.runRows(man, env(keys.env), t.TempDir(), egressRow(), inv)
				f := &choiceui.Form{Rows: rows}
				gateRunForm(f)
				name := man.Name + ", " + keys.name + ", egress unreadable"
				if known {
					name = man.Name + ", " + keys.name + ", egress readable"
				}
				out = append(out, runFormCase{name, man, f})
			}
		}
	}
	return out
}

func mergedEnv(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range []map[string]string{a, b} {
		maps.Copy(out, m)
	}
	return out
}

// disabled reports whether option j of r is one the operator cannot change: off, or on a locked row.
func disabled(r choiceui.Row, j int) bool {
	return r.Locked || (j < len(r.Off) && r.Off[j])
}

// On every run form, ←/→ land on every disabled option of every row the cursor can rest on, each
// names itself or its open list's entry and shows a status line, a radio row's answer is never a
// disabled option, and a full lap leaves the answer where it was.
func TestEveryDisabledRunOptionTakesTheCursorAndStatesItsStatus(t *testing.T) {
	cases := runFormCases(t)
	harnesses := map[string]bool{}
	sawRadioOff, sawList := false, false
	for _, c := range cases {
		harnesses[c.man.Name] = true
		f := c.form
		for i := range f.Rows {
			r := f.Rows[i]
			if len(r.Options) == 0 || f.Walk(r.Label, "right", "left") == nil {
				continue
			}
			before := f.Selection(r.Label)
			for _, key := range []string{"right", "left"} {
				seen := map[string]bool{}
				for range r.Options {
					hv := f.Walk(r.Label, key)[0]
					seen[hv.Option] = true
					j := slices.Index(r.Options, hv.Option)
					if !disabled(f.Rows[i], j) {
						continue
					}
					if len(hv.Status) == 0 || strings.TrimSpace(strings.Join(hv.Status, "")) == "" {
						t.Errorf("%s: %s · %q is disabled and shows no status line; help %q", c.name, r.Label, hv.Option, hv.Help)
					}
					if len(hv.Help) == 0 || !strings.HasPrefix(hv.Help[0], "› ") {
						t.Errorf("%s: %s · %q is disabled and its help names nothing: help %q, status %q", c.name, r.Label, hv.Option, hv.Help, hv.Status)
					}
					sawList = sawList || (len(hv.Help) > 0 && strings.HasPrefix(hv.Help[0], "› id_ed25519 — "))
					if !r.Multi && !r.Locked {
						sawRadioOff = true
						if got := f.Selection(r.Label); got == hv.Option {
							t.Errorf("%s: %s answers %q, a disabled option", c.name, r.Label, got)
						}
					}
				}
				for j, opt := range r.Options {
					if disabled(f.Rows[i], j) && !seen[opt] {
						t.Errorf("%s: %s · %q is disabled and %s never lands on it", c.name, r.Label, opt, key)
					}
				}
				if got := f.Selection(r.Label); !r.Locked && got != before {
					t.Errorf("%s: %s answers %q after a lap of %s, want %q as before", c.name, r.Label, got, key, before)
				}
			}
		}
	}
	for _, name := range []string{"claudecode", "codex", "cursor", "hermes", "opencode"} {
		if !harnesses[name] {
			t.Errorf("no run form for %s", name)
		}
	}
	if !sawRadioOff {
		t.Error("no run form put a disabled option on a radio row; the case that motivated this test is gone")
	}
	if !sawList {
		t.Error("no credentials cell opened its list; the inventory never reached the run form")
	}
}

// claudecode with a plan and no provider key: ← from plan hovers API keys, plan stays the answer,
// and the help names API keys and why it is off.
func TestClaudecodeHoversAPIKeysWithNoProviderKey(t *testing.T) {
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	var man manifest.Manifest
	for _, m := range ms {
		if m.Name == "claudecode" {
			man = m
		}
	}
	orig := policyBaseline
	t.Cleanup(func() { policyBaseline = orig })
	policyBaseline = func() (string, bool) { return sbx.BaselineBalanced, true }
	p := &Params{Target: "claudecode"}
	rows, _ := p.runRows(man, env(map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "plan-token"}), t.TempDir(), egressRow(), credentialInventory{})
	f := &choiceui.Form{
		Step:     choiceui.Step{At: 2, Of: 2, Leave: runLeave},
		Title:    "run claudecode — confirm or change this run",
		Header:   []string{"git:      monorepo on main"},
		Topology: topologyWith(p.Target, p.credentialsOrDefault(), man.Capabilities.AlwaysMCPGateway()),
		Rows:     rows,
	}
	gateRunForm(f)
	if row, option := f.StartsOn(); row != rowModel || option != modelPlan {
		t.Fatalf("the form opens on %s · %s, want model · plan", row, option)
	}
	hv := f.Walk(rowModel, "left")[0]
	if hv.Option != modelAPIKeys || f.Selection(rowModel) != modelPlan {
		t.Fatalf("← from plan: cursor on %q, answer %q; want API keys hovered, plan kept", hv.Option, f.Selection(rowModel))
	}
	why := f.Rows[slices.IndexFunc(f.Rows, func(r choiceui.Row) bool { return r.Label == rowModel })].OffWhy[modelAPIKeys]
	if why == "" {
		t.Fatal("API keys is off with no OffWhy")
	}
	frame := f.RenderOn(rowModel, 120, 40)
	head, _, _ := strings.Cut(why, ",")
	for _, want := range []string{"› API keys — metered by the vendor beyond the plan's included usage\n", "off: " + head} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame lacks %q:\n%s", want, frame)
		}
	}
	if n := strings.Count(frame, "bring-your-own-key"); n != 1 {
		t.Errorf("the frame says bring-your-own-key %d times, want once, in the status:\n%s", n, frame)
	}
	t.Logf("claudecode, API keys hovered with no provider key:\n%s", frame)
}
