package main

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
)

func envOf(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func defs(t *testing.T) []manifest.Manifest {
	t.Helper()
	ms, err := loadManifests()
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func slotOf(t *testing.T, slots []credSlot, label string) credSlot {
	t.Helper()
	for _, s := range slots {
		if s.Label == label {
			return s
		}
	}
	t.Fatalf("no slot for %q in %d slots", label, len(slots))
	return credSlot{}
}

func hasSlot(slots []credSlot, label string) bool {
	for _, s := range slots {
		if s.Label == label {
			return true
		}
	}
	return false
}

func TestANarrowDefShowsItsOwnVendorAndNoOther(t *testing.T) {
	var narrow []manifest.Manifest
	for _, m := range defs(t) {
		if m.Name == "claudecode" {
			narrow = append(narrow, m)
		}
	}
	slots := surveyCredentials(narrow, nil, envOf(nil))
	if !hasSlot(slots, "claudecode") {
		t.Error("no subscription slot for claudecode — it is the row an operator needs first")
	}
	if !hasSlot(slots, "anthropic") {
		t.Error("no api key slot for anthropic — the vendor claudecode declares")
	}
	for _, s := range slots {
		switch s.Label {
		case "claudecode", "anthropic":
		default:
			t.Errorf("claudecode's screen offers %q, which it cannot route to", s.Label)
		}
	}
}

func TestABroadDefShowsTheShortListNotTheRegistry(t *testing.T) {
	var broad []manifest.Manifest
	for _, m := range defs(t) {
		if m.Name == "opencode" {
			broad = append(broad, m)
		}
	}
	slots := surveyCredentials(broad, nil, envOf(map[string]string{"GROQ_API_KEY": "k"}))
	if len(slots) == 0 || len(slots) > 8 {
		t.Fatalf("opencode offers %d slots — the short list is what keeps the four that matter visible", len(slots))
	}
	if !hasSlot(slots, "groq") {
		t.Error("a key already on this host got no slot, so init cannot move it into the store")
	}
	if !hasSlot(slots, "anthropic") {
		t.Error("a def-backed provider got no slot")
	}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (R2, R4)
func TestInitWritesServiceIDsAndSignsSubscriptionsInThroughSbx(t *testing.T) {
	slots := surveyCredentials(defs(t), map[string]sbx.StoreEntry{"openai": {OAuth: true}}, envOf(nil))
	for _, s := range slots {
		if _, ok := provider.Lookup(s.Store); !ok {
			t.Errorf("%s stores as %q, which is no registry service — sbx never reads such an entry (P0 M2)", s.Label, s.Store)
		}
	}
	for def, svc := range map[string]string{"claudecode": "anthropic", "codex": "openai", "cursor": "cursor"} {
		sign := slotByKind(t, slots, def, kindSignIn)
		if sign.Store != svc {
			t.Errorf("%s signs in as %q, want %q", def, sign.Store, svc)
		}
	}
	if !slotByKind(t, slots, "codex", kindSignIn).Held {
		t.Error("openai (oauth configured) must read as codex signed in")
	}
	if slotByKind(t, slots, "claudecode", kindSignIn).Held {
		t.Error("nothing is stored for anthropic, yet claudecode reads as signed in")
	}
	if got := slotByKind(t, slots, "opencode", kindSubscription).Store; got != "opencode" {
		t.Errorf("opencode's key slot stores as %q, want the opencode service", got)
	}
	rows := credentialRows(slots)
	for i, s := range slots {
		if s.Kind == kindSignIn && (rows[i].Field || rows[i].Locked) {
			t.Errorf("%s's sign-in row takes typing or is locked; sbx's OAuth slot has no import path, and the row runs sbx's own sign-in (R4)", s.Label)
		}
	}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (P0 M2)
func TestStaleEntriesAreTheOnesSbxNeverReads(t *testing.T) {
	got := credentials.StaleStoreEntries(
		[]string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "anthropic", "claudecode", "cursor-sub", "github", "opencode", "cecli"},
		[]sbx.CustomSecret{{Env: "OPENCODE_API_KEY", Placeholder: "sbx-cs-a"}, {Env: "PLUVO_GUSTO_USER", Placeholder: "sbx-cs-b"}},
		[]string{"claudecode", "opencode"})
	var lines []string
	for _, a := range got {
		lines = append(lines, strings.Join(a, " "))
	}
	want := []string{
		"secret rm ANTHROPIC_API_KEY -f",
		"secret rm CLAUDE_CODE_OAUTH_TOKEN -f",
		"secret rm claudecode -f",
		"secret rm cursor-sub -f",
		"secret rm --placeholder sbx-cs-a -f",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("StaleStoreEntries:\n got %q\nwant %q", lines, want)
	}
}

func TestStateFollowsWhereTheCredentialRests(t *testing.T) {
	slots := surveyCredentials(defs(t),
		map[string]sbx.StoreEntry{"anthropic": {APIKey: true}},
		envOf(map[string]string{"OPENAI_API_KEY": "sk-live"}))

	held := slotOf(t, slots, "anthropic")
	if !held.Held {
		t.Error("a provider in the store is not marked held — its field would invite a needless retype")
	}
	if got := held.placeholder(); !strings.Contains(got, "type to replace") {
		t.Errorf("held placeholder = %q, want it to say what typing would do", got)
	}

	env := slotOf(t, slots, "openai")
	if env.Held {
		t.Error("a key that is only in this host's env is not in the store, and must not read as stored")
	}
	if got := env.placeholder(); !strings.Contains(got, "Enter to import") {
		t.Errorf("env-only placeholder = %q, want the one-key import", got)
	}

	empty := slotOf(t, slots, "cursor")
	if got := empty.placeholder(); !strings.Contains(got, "empty") {
		t.Errorf("empty placeholder = %q — a blank box says neither what it holds nor what typing does", got)
	}
}

func TestEveryFieldIsMaskedAndGroupedByDef(t *testing.T) {
	rows := credentialRows(surveyCredentials(defs(t), nil, envOf(nil)))
	headings := 0
	var keyHeading string
	for _, r := range rows {
		if !r.Field && len(r.Options) > 0 {
			continue // a sign-in row: sbx's OAuth slot takes options, never typing (R4)
		}
		if !r.Field {
			t.Fatalf("%q is not a field — every credential takes typing, not an option", r.Label)
		}
		if !r.Masked {
			t.Errorf("%q is unmasked; typing a key into it would put the key on the screen", r.Label)
		}
		if r.Placeholder == "" {
			t.Errorf("%q has no placeholder", r.Label)
		}
		if r.Divider {
			headings++
			if r.Heading == "" {
				t.Errorf("%q opens a group with no heading", r.Label)
			}
			if strings.Contains(r.Heading, "opencode") && strings.Contains(r.Heading, "api") {
				t.Errorf("usage keys headed %q — they are not opencode's; any agent can spend them", r.Heading)
			}
			if r.Heading == "usage api keys" {
				keyHeading = r.Heading
			}
		}
	}
	if headings < 2 {
		t.Errorf("%d group headings — subscription and api keys are different questions and say so", headings)
	}
	if keyHeading != "usage api keys" {
		t.Errorf("key group heading = %q, want usage api keys", keyHeading)
	}
	// Keys come last, as one block: a key heading between two subscriptions
	// would mean they were still attributed to whichever def listed them.
	sawKey := false
	nKey := 0
	for _, r := range rows {
		if r.Heading == "usage api keys" {
			nKey++
			sawKey = true
			continue
		}
		if sawKey && r.Divider && strings.Contains(r.Heading, "subscription") {
			t.Errorf("subscription %q follows usage keys — the keys must be one trailing block", r.Heading)
		}
	}
	if nKey != 1 {
		t.Errorf("%d usage-key headings, want one shared group", nKey)
	}
}

func TestVerifyStaysSilentWhereNoProbeIsKnown(t *testing.T) {
	if ok, detail := verifySlot(credSlot{Label: "cursor", Kind: kindKey, Store: "cursor"}, "key"); ok || detail != "" {
		t.Errorf("cursor has no cheap authenticated endpoint; got ok=%v detail=%q — "+
			"unverified must read as neither good nor bad", ok, detail)
	}
	if ok, detail := verifySlot(credSlot{Label: "claudecode", Kind: kindSubscription, Store: "claudecode"}, "t"); ok || detail != "" {
		t.Errorf("a plan token has no models endpoint to ask; got ok=%v detail=%q", ok, detail)
	}
}

func slotByKind(t *testing.T, slots []credSlot, label string, k credKind) credSlot {
	t.Helper()
	for _, s := range slots {
		if s.Label == label && s.Kind == k {
			return s
		}
	}
	t.Fatalf("no %v slot for %q", k, label)
	return credSlot{}
}

// The form and the slots are read positionally, so any drift between them
// hands one credential's value to another.
func TestRowsAndSlotsStayOneToOne(t *testing.T) {
	slots := surveyCredentials(defs(t), nil, envOf(map[string]string{"GROQ_API_KEY": "k"}))
	rows := credentialRows(slots)
	if len(rows) != len(slots) {
		t.Fatalf("%d rows for %d slots — the stage reads them by index", len(rows), len(slots))
	}
	for i := range slots {
		if rows[i].Label != slots[i].Label {
			t.Fatalf("row %d is %q but slot %d is %q", i, rows[i].Label, i, slots[i].Label)
		}
	}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (R4)
func TestSignInRowsOfferSbxsOwnSignInAndSignOut(t *testing.T) {
	slots := surveyCredentials(defs(t), map[string]sbx.StoreEntry{"openai": {OAuth: true}}, envOf(nil))
	for _, tc := range []struct {
		def  string
		want []string
	}{
		{"codex", []string{signedIn, signInAgain, signOut}},
		{"claudecode", []string{notSignedIn, signInNow}},
		{"cursor", []string{notSignedIn, signInNow}},
	} {
		r := signInRow(slotByKind(t, slots, tc.def, kindSignIn))
		if strings.Join(r.Options, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s offers %v, want %v", tc.def, r.Options, tc.want)
		}
		if r.Selected != 0 || r.Locked || r.Field {
			t.Errorf("%s: selected=%d locked=%v field=%v — the row opens on the choice that changes nothing", tc.def, r.Selected, r.Locked, r.Field)
		}
		for _, opt := range r.Options {
			if r.Help[opt] == "" {
				t.Errorf("%s: %q has no help line naming what enter runs", tc.def, opt)
			}
		}
	}
}

// SPEC: _spec/internal/sbx/credential-resolution.puml (R4)
func TestApplySignInRunsOnlySbxCommands(t *testing.T) {
	codex := credSlot{Label: "codex", Kind: kindSignIn, Store: "openai", Held: true}
	claude := credSlot{Label: "claudecode", Kind: kindSignIn, Store: "anthropic"}
	cursor := credSlot{Label: "cursor", Kind: kindSignIn, Store: "cursor"}
	for _, tc := range []struct {
		slot   credSlot
		choice string
		want   []string
	}{
		{codex, signedIn, nil},
		{codex, signInAgain, []string{"sbx secret set openai --oauth"}},
		{codex, signOut, []string{"sbx secret rm openai -f"}},
		{claude, notSignedIn, nil},
		{claude, signInNow, []string{"sbx run --name " + sbx.SignInSandbox + " claude <dir> -- auth login", "sbx rm --force " + sbx.SignInSandbox}},
		{cursor, signInNow, nil},
		{cursor, "", nil},
	} {
		var ran []string
		err := applySignIn(tc.slot, tc.choice, func(argv []string) error {
			line := strings.Join(argv, " ")
			if i := strings.Index(line, " claude "); i >= 0 {
				if j := strings.Index(line, " -- "); j > i {
					line = line[:i] + " claude <dir>" + line[j:]
				}
			}
			ran = append(ran, line)
			return nil
		})
		if err != nil {
			t.Errorf("%s %q: %v", tc.slot.Label, tc.choice, err)
		}
		if strings.Join(ran, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s %q ran %q, want %q", tc.slot.Label, tc.choice, ran, tc.want)
		}
	}
}

// The sign-in row for cursor and the usage key for cursor share a label.
func TestSignInChoiceIsReadByPosition(t *testing.T) {
	rows := []choiceui.Row{
		{Label: "cursor", Options: []string{notSignedIn, signInNow}, Selected: 1},
		{Label: "cursor", Field: true, Value: "k"},
	}
	if got := signInChoice(rows, 0); got != signInNow {
		t.Errorf("signInChoice(0) = %q, want %q", got, signInNow)
	}
	for _, i := range []int{1, 2} {
		if got := signInChoice(rows, i); got != "" {
			t.Errorf("signInChoice(%d) = %q, want empty", i, got)
		}
	}
}
