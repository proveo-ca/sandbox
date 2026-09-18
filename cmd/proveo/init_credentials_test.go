package main

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
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
		if m.Name == "cecli" {
			broad = append(broad, m)
		}
	}
	slots := surveyCredentials(broad, nil, envOf(map[string]string{"GROQ_API_KEY": "k"}))
	if len(slots) == 0 || len(slots) > 8 {
		t.Fatalf("cecli offers %d slots — the short list is what keeps the four that matter visible", len(slots))
	}
	if !hasSlot(slots, "groq") {
		t.Error("a key already on this host got no slot, so init cannot move it into the store")
	}
	if !hasSlot(slots, "anthropic") {
		t.Error("a def-backed provider got no slot")
	}
}

func TestTheTwoKindsNeverShareAStoreName(t *testing.T) {
	slots := surveyCredentials(defs(t), nil, envOf(nil))
	byStore := map[string][]string{}
	for _, s := range slots {
		byStore[s.Store] = append(byStore[s.Store], s.Label)
	}
	for store, labels := range byStore {
		if len(labels) > 1 {
			t.Errorf("store id %q is claimed by %v — one id cannot say which KIND it holds", store, labels)
		}
	}
	if got := slotOf(t, slots, "claudecode").Store; got != "claudecode" {
		t.Errorf("claudecode's subscription stores as %q, want the def name", got)
	}
	// cursor's def is named for its own provider, so both kinds carry the label
	// "cursor" and only the store name keeps them apart.
	var kinds []credKind
	for _, s := range slots {
		if s.Label == "cursor" {
			kinds = append(kinds, s.Kind)
		}
	}
	if len(kinds) == 2 {
		sub, key := slotByKind(t, slots, "cursor", kindSubscription), slotByKind(t, slots, "cursor", kindKey)
		if sub.Store != "cursor-sub" || key.Store != "cursor" {
			t.Errorf("cursor stores subscription=%q key=%q, want cursor-sub and cursor", sub.Store, key.Store)
		}
	}
}

func TestStateFollowsWhereTheCredentialRests(t *testing.T) {
	slots := surveyCredentials(defs(t),
		[]string{"anthropic"},
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
			if strings.Contains(r.Heading, "cecli") && strings.Contains(r.Heading, "api") {
				t.Errorf("usage keys headed %q — they are not cecli's; any agent can spend them", r.Heading)
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
