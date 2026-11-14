// SPEC: _spec/cmd/proveo/github-access.puml
package run

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/ui"
)

func TestCredentialInventoryDeduplicatesMetadataWithoutRenderingSecretValues(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{Name: "claudecode", Env: []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}}}
	values := map[string]string{"OPENAI_API_KEY": "test-api-secret", "CLAUDE_CODE_OAUTH_TOKEN": "test-oauth-secret"}
	store := map[string]sbx.StoreEntry{"openai": {APIKey: true, OAuth: true}, "anthropic": {OAuth: true}, "github": {APIKey: true}}
	ssh := []string{"SHA256:public-fingerprint (ED25519)"}
	got := inventoryOf(man, func(name string) string { return values[name] }, store, ssh)
	if !slices.Equal(got.APIKeys, []string{"github", "openai"}) || !slices.Equal(got.OAuth, []string{"anthropic", "openai"}) || !slices.Equal(got.SSH, ssh) {
		t.Fatalf("inventory = %#v", got)
	}
	row := credentialsRow(got)
	for _, secret := range values {
		if strings.Contains(rowText(row), secret) {
			t.Fatal("credential inventory rendered a secret value")
		}
	}
	ssh[0] = "changed"
	if got.SSH[0] != "SHA256:public-fingerprint (ED25519)" {
		t.Fatal("inventory shares mutable SSH input")
	}
}

// rowText is the row and every list it opens, as Go syntax.
func rowText(row choiceui.Row) string {
	text := fmt.Sprintf("%#v", row)
	for _, m := range row.Lists {
		text += fmt.Sprintf(" %#v", *m)
	}
	return text
}

func TestTheEnvCellListsTheEnvFilesNamesAndNeverAValue(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".env")
	values := []string{"envvalue-zz-0117", "envvalue-qq-4471", "envvalue-ww-9023"}
	body := "zeta=" + values[0] + "\nexport ALPHA=\"" + values[1] + "\"\n=" + values[2] + "\nbeta='x'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	row := credentialsRow(credentialInventory{}.withEnvFile(path))
	env := row.Lists[3]
	if env == nil || !slices.Equal(env.Options, []string{"ALPHA", "beta", "zeta"}) || !row.On[3] {
		t.Fatalf(".env cell = %#v", row)
	}
	for _, v := range values {
		if strings.Contains(rowText(row), v) {
			t.Fatalf("the .env value %q reached the row: %s", v, rowText(row))
		}
	}
	if !strings.Contains(env.Help["zeta"], ui.Tilde(path)) {
		t.Errorf(".env help %q does not name the file", env.Help["zeta"])
	}
}

func TestEmptyCredentialClassesStayUntickedWithoutDropdowns(t *testing.T) {
	t.Parallel()
	row := credentialsRow(credentialInventory{OAuth: []string{"openai"}})
	if !slices.Equal(row.On, []bool{false, true, false, false}) || row.Lists[0] != nil || row.Lists[2] != nil || row.Lists[1] == nil || row.Lists[3] != nil {
		t.Fatalf("empty classes advertised credentials: %#v", row)
	}
	missing := credentialsRow(credentialInventory{}.withEnvFile(filepath.Join(t.TempDir(), "absent.env")))
	if missing.On[3] || missing.Lists[3] != nil {
		t.Fatalf("an env file with no names advertised a list: %#v", missing)
	}
	frame := formOf(row).Render(120, 24)
	for _, cell := range []string{"API keys (0)", "OAuth (1) ▾", "SSH (0)", ".env (0)"} {
		if !strings.Contains(frame, cell) {
			t.Errorf("the credentials row lacks %q:\n%s", cell, frame)
		}
	}
	if strings.Contains(frame, "(0) ▾") {
		t.Errorf("an empty class drew an arrow:\n%s", frame)
	}
	if line := credentialsLine(frame); strings.Contains(line, "[x]") || strings.Contains(line, "[ ]") {
		t.Errorf("the credentials row drew a tick box: %q", line)
	}
}

// credentialsLine is the frame's credentials row without its right wall or trailing blanks.
func credentialsLine(frame string) string {
	for line := range strings.SplitSeq(frame, "\n") {
		if strings.Contains(line, "credentials") {
			return strings.TrimRight(strings.TrimSuffix(line, "│"), " ")
		}
	}
	return ""
}

// formOf is a run form holding row above a model row the cursor opens on.
func formOf(row choiceui.Row) *choiceui.Form {
	return &choiceui.Form{Title: "run claudecode — confirm or change this run", Rows: []choiceui.Row{row,
		{Label: "model", Options: []string{"Qwen 3.8", "API keys"}, Selected: 1, Cols: []int{0, 1}}}}
}

func TestEachCredentialCellCountsItsListAndNoCountReachesTheSelection(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".env")
	var body strings.Builder
	for i := range 20 {
		fmt.Fprintf(&body, "NAME_%02d=envvalue-%02d\n", i, i)
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	inv := credentialInventory{APIKeys: []string{"anthropic", "github", "openai", "xai"}, OAuth: []string{"anthropic", "openai"},
		SSH: []string{"SHA256:public-fingerprint (ED25519)"}}.withEnvFile(path)
	row := credentialsRow(inv)
	if !slices.Equal(row.Options, []string{"API keys", "OAuth", "SSH", ".env"}) {
		t.Fatalf("options = %q, want the class names without counts", row.Options)
	}
	want := []string{"API keys (4)", "OAuth (2)", "SSH (1)", ".env (20)"}
	for i, opt := range row.Options {
		if row.Lists[i] == nil || row.Paint[opt] != fmt.Sprintf("%s (%d)", opt, len(row.Lists[i].Options)) || row.Paint[opt] != want[i] {
			t.Errorf("cell %q paints %q, want %q, its list's length", opt, row.Paint[opt], want[i])
		}
	}
	f := formOf(row)
	for _, got := range append(f.Selections("credentials"), f.Selection("credentials")) {
		if strings.Contains(got, "(") {
			t.Errorf("a count reached the selection: %q", got)
		}
	}
	for _, w := range []int{80, 120} {
		frame := f.Render(w, 24)
		for _, cell := range want {
			if !strings.Contains(frame, cell+" ▾") {
				t.Errorf("%d columns: the credentials row lacks %q:\n%s", w, cell+" ▾", frame)
			}
		}
	}
}

// The credentials row draws no tick boxes, so at 80 and 120 columns every class reads whole with
// its count, an empty class as "OAuth (0)" with no ▾.
// SPEC: _spec/internal/choiceui/wireframe.puml (A CELL COUNTS ITS LIST)
func TestTheCredentialsRowShowsEveryCountedCellWholeAt80And120(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".env")
	var body strings.Builder
	for i := range 20 {
		fmt.Fprintf(&body, "NAME_%02d=x\n", i)
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	ssh := []string{"SHA256:public-fingerprint (ED25519)"}
	lists := credentialInventory{APIKeys: []string{"anthropic", "github", "openai", "xai"}, OAuth: []string{"anthropic", "openai"}, SSH: ssh}
	empty := credentialInventory{APIKeys: lists.APIKeys, SSH: ssh}
	for _, c := range []struct {
		name string
		inv  credentialInventory
		w    int
		want string
	}{
		{"lists", lists, 80, "│   credentials   API keys (4) ▾  OAuth (2) ▾     SSH (1) ▾       .env (20) ▾"},
		{"lists", lists, 120, "│   credentials   API keys (4) ▾        OAuth (2) ▾           SSH (1) ▾             .env (20) ▾"},
		{"an empty class", empty, 80, "│   credentials   API keys (4) ▾  OAuth (0)       SSH (1) ▾       .env (20) ▾"},
		{"an empty class", empty, 120, "│   credentials   API keys (4) ▾        OAuth (0)             SSH (1) ▾             .env (20) ▾"},
	} {
		frame := formOf(credentialsRow(c.inv.withEnvFile(path))).Render(c.w, 24)
		if got := credentialsLine(frame); got != c.want {
			t.Errorf("%s, %d columns:\n got %q\nwant %q\n%s", c.name, c.w, got, c.want, frame)
		}
	}
}

// Only the credentials row drops its tick boxes: every other checkbox row of every run form still
// draws "[x] " or "[ ] " in its cells.
func TestNoOtherRunRowLostItsTickBoxes(t *testing.T) {
	others := 0
	for _, c := range runFormCases(t) {
		for _, r := range c.form.Rows {
			if r.Label == "credentials" {
				if !r.Bare || !r.Multi {
					t.Errorf("%s: the credentials row is Multi %v, Bare %v; want both", c.name, r.Multi, r.Bare)
				}
				continue
			}
			ticks := slices.ContainsFunc(r.Options, func(o string) bool { return !slices.Contains(r.RadioSet, o) })
			if !r.Multi || r.Radio || r.Field || !ticks {
				continue
			}
			others++
			if r.Bare {
				t.Errorf("%s: %s is Bare", c.name, r.Label)
			}
			frame := c.form.RenderOn(r.Label, 120, 80)
			var line string
			for l := range strings.SplitSeq(frame, "\n") {
				if strings.HasPrefix(l, "│ › ") {
					line = l
				}
			}
			if !strings.Contains(line, "[x] ") && !strings.Contains(line, "[ ] ") {
				t.Errorf("%s: %s lost its tick boxes: %q", c.name, r.Label, line)
			}
		}
	}
	if others == 0 {
		t.Fatal("no run form held another checkbox row")
	}
}
