// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/internal/choiceui/choice-prompt-render.puml
package run

import (
	"slices"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/devports"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// The run form asks agent, execution, reach, interface in that order, each section opening on its
// first present row, and the cursor opens on the top row with an answer to change, on that answer.
func TestTheRunFormAsksItsQuestionsInOrder(t *testing.T) {
	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]manifest.Manifest{}
	for _, m := range ms {
		byName[m.Name] = m
	}
	orig := policyBaseline
	t.Cleanup(func() { policyBaseline = orig })
	egress := func(known bool) choiceui.Row {
		policyBaseline = func() (string, bool) { return sbx.BaselineBalanced, known }
		return egressRow()
	}
	readable, unreadable := egress(true), egress(false)
	const creds = "credentials"
	agent, execution := sectionAgent+" "+creds, addonHeading[rowExecution]+" "+rowExecution
	interfaceSec := addonHeading[rowInterface] + " " + rowInterface
	ports := []devports.Candidate{{Port: 5173, Command: "dev", Tool: "vite", Source: "package.json"}}
	apps := []devports.AndroidApp{{Module: ":app", AppID: "com.example.app", Source: "app/build.gradle.kts"}}
	for _, c := range []struct {
		name, target string
		egress       choiceui.Row
		ports        []devports.Candidate
		apps         []devports.AndroidApp
		sections     []string
		labels       []string
		start        string
	}{
		{"claudecode", "claudecode", readable, nil, nil,
			[]string{agent, execution, sectionReach + " egress", interfaceSec},
			[]string{creds, rowModel, rowExecution, repoTrustLabel, "egress", mcpLabel, rowInterface, evidenceLabel}, rowModel},
		{"hermes", "hermes", readable, nil, nil,
			[]string{agent, execution, sectionReach + " egress", interfaceSec},
			[]string{creds, rowModel, rowExecution, repoTrustLabel, "egress", rowInterface, evidenceLabel}, rowModel},
		{"codex", "codex", readable, nil, nil,
			[]string{agent, execution, sectionReach + " egress", interfaceSec},
			[]string{creds, rowModel, rowExecution, repoTrustLabel, "egress", mcpLabel, rowInterface, evidenceLabel}, rowModel},
		{"cursor with no key and no login", "cursor", readable, nil, nil,
			[]string{agent, execution, sectionReach + " egress", interfaceSec},
			[]string{creds, rowExecution, repoTrustLabel, "egress", rowInterface, evidenceLabel}, repoTrustLabel},
		{"unreadable egress", "claudecode", unreadable, nil, nil,
			[]string{agent, execution, sectionReach + " " + mcpLabel, interfaceSec},
			[]string{creds, rowModel, rowExecution, repoTrustLabel, mcpLabel, rowInterface, evidenceLabel}, rowModel},
		{"unreadable egress and no gateway", "cursor", unreadable, nil, nil,
			[]string{agent, execution, interfaceSec},
			[]string{creds, rowExecution, repoTrustLabel, rowInterface, evidenceLabel}, repoTrustLabel},
		{"discovered ports and apps", "claudecode", readable, ports, apps,
			[]string{agent, execution, sectionReach + " egress", interfaceSec},
			[]string{creds, rowModel, rowExecution, repoTrustLabel, "egress", mcpLabel, rowInterface, evidenceLabel, rowPorts, rowApps}, rowModel},
	} {
		man, ok := byName[c.target]
		if !ok {
			t.Fatalf("%s: no %s manifest", c.name, c.target)
		}
		p := &Params{Target: c.target, discovered: c.ports, discoveredApps: c.apps}
		rows, _ := p.runRows(man, env(nil), t.TempDir(), c.egress, credentialInventory{})
		f := &choiceui.Form{Rows: rows}
		gateRunForm(f)
		var sections, labels []string
		for _, r := range f.Rows {
			labels = append(labels, r.Label)
			if r.Divider {
				sections = append(sections, r.Heading+" "+r.Label)
			}
		}
		if !slices.Equal(sections, c.sections) {
			t.Errorf("%s: sections (name, first row) = %q, want %q", c.name, sections, c.sections)
		}
		if !slices.Equal(labels, c.labels) {
			t.Errorf("%s: rows = %q, want %q", c.name, labels, c.labels)
		}
		row, option := f.StartsOn()
		if want := f.Selection(c.start); row != c.start || option != want || want == "" {
			t.Errorf("%s: the cursor opens on %q · %q, want %q · %q", c.name, row, option, c.start, want)
		}
		t.Logf("%-32s %q start %s · %s", c.name, sections, row, option)
	}
}
