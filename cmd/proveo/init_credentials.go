// SPEC: _spec/_plans/init-credential-provisioning.puml, _spec/cmd/proveo/init-credential-wireframe.puml
// SPEC: _spec/cmd/proveo/init-credential-entry.puml, _spec/_paradigms/credential-boundary.puml

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/backend/sandbox"
	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
	"github.com/proveo-ca/proveo/internal/secretref"
	"github.com/proveo-ca/proveo/internal/ui"
)

// credKind says which of the two things a slot holds.
type credKind int

const (
	kindKey credKind = iota
	kindSubscription
	kindSignIn
)

// credSlot is one line of the screen: a credential, where it rests today, and
// the name it takes in the store.
type credSlot struct {
	Label   string // what the operator reads: the provider, or the def for a subscription
	Kind    credKind
	Store   string   // the service id `sbx secret set` is given
	Def     string   // the def this slot belongs to, for grouping
	Held    bool     // the store answers for it
	KeyHeld bool     // a sign-in slot whose service also holds an api key, which outranks the login
	EnvVars []string // detect vars set on this host: the surface being retired
}

func (s credSlot) placeholder() string {
	switch {
	case len(s.EnvVars) > 0 && !s.Held:
		return "this host's env only — Enter to import"
	case s.Held:
		return "stored — type to replace"
	case s.Kind == kindSubscription:
		return "empty — type a token"
	}
	return "empty — type a key"
}

func (s credSlot) hint() string {
	switch {
	case len(s.EnvVars) > 0 && !s.Held:
		return strings.Join(s.EnvVars, ", ") + " is set here; init moves it into the store"
	case s.Kind == kindSignIn && s.Held:
		return "sbx holds the login as " + s.Store + " (oauth configured)"
	case s.Kind == kindSignIn:
		return "sign in with " + sbx.SignIn(s.Store) + " — sbx keeps the login as " + s.Store
	case s.Held:
		return "stored as " + s.Store
	case s.Kind == kindSubscription:
		return subscriptionHowTo(s.Def)
	}
	return "sbx holds nothing for it; leaving it empty changes nothing"
}

// subscriptionHowTo reads the hint table the run path already prints, so init
// and a refused run name the same command.
func subscriptionHowTo(def string) string {
	for _, h := range credentials.SubscriptionAuthHints[credentials.HarnessFamily(def)] {
		switch {
		case h.Login != "" && h.HowTo != "":
			return h.HowTo + " (or `" + h.Login + "` on the host)"
		case h.HowTo != "":
			return h.HowTo
		}
	}
	return "the agent's own login, run once — proveo never sees the token"
}

// narrowProviders is the def's own vendor list. An empty list means the def
// routes anywhere — opencode — and gets the short list instead.
func narrowProviders(m manifest.Manifest) []string {
	if len(m.Capabilities.Providers) == 0 || len(m.Capabilities.Providers) > 3 {
		return nil
	}
	return m.Capabilities.Providers
}

// surveyCredentials builds the screen: one group per def, its subscription
// first, then the api keys that def can actually spend.
func surveyCredentials(ms []manifest.Manifest, store map[string]sbx.StoreEntry, getenv func(string) string) []credSlot {
	envVarsOf := func(name string) []string {
		e, ok := provider.Lookup(name)
		if !ok {
			return nil
		}
		var out []string
		for _, v := range e.Detect {
			if strings.TrimSpace(getenv(v)) != "" {
				out = append(out, v)
			}
		}
		return out
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Name < ms[j].Name })
	var out []credSlot
	seen := map[string]bool{}
	for _, m := range ms {
		own := narrowProviders(m)
		if m.Subscription {
			if svc := sbx.SubscriptionService(sbx.BuiltinAgent(credentials.HarnessFamily(m.Name))); svc != "" {
				if !seen["sign-in "+svc] {
					seen["sign-in "+svc] = true
					out = append(out, credSlot{Label: m.Name, Kind: kindSignIn, Store: svc, Def: m.Name, Held: store[svc].OAuth, KeyHeld: store[svc].APIKey})
				}
			} else if svc := keyServiceOf(m); svc != "" && !seen[svc] {
				seen[svc] = true
				out = append(out, credSlot{
					Label: m.Name, Kind: kindSubscription, Store: svc, Def: m.Name,
					Held: store[svc].APIKey, EnvVars: envVarsOf(svc),
				})
			}
		}
		keys := own
		if keys == nil {
			keys = broadShortList(m, getenv)
		}
		for _, name := range keys {
			if seen[name] {
				continue
			}
			e, ok := provider.Lookup(name)
			if !ok || len(e.Auth) == 0 {
				continue // B4: a provider the proxy cannot inject needs no store entry
			}
			seen[name] = true
			out = append(out, credSlot{
				Label: name, Kind: kindKey, Store: name, Def: m.Name,
				Held: store[name].APIKey, EnvVars: envVarsOf(name),
			})
		}
	}
	return groupSlots(out)
}

// groupSlots puts every subscription first, then the usage keys. The keys are
// not owned by the def that happened to list them — any agent can spend one.
func groupSlots(slots []credSlot) []credSlot {
	var subs, keys []credSlot
	for _, s := range slots {
		if s.Kind == kindKey {
			keys = append(keys, s)
			continue
		}
		subs = append(subs, s)
	}
	return append(subs, keys...)
}

// keyServiceOf is the service of a def's first secret that is an API key.
func keyServiceOf(m manifest.Manifest) string {
	for _, e := range m.Env {
		if !e.Secret {
			continue
		}
		if svc, kind := credentials.StoreName(e.Name); kind == credentials.StoreAPIKey {
			return svc
		}
	}
	return ""
}

// broadShortList is what a def that routes anywhere shows: the providers other
// defs run on, plus anything this host already carries a key for.
func broadShortList(m manifest.Manifest, getenv func(string) string) []string {
	var out []string
	for _, name := range provider.Names() {
		e, ok := provider.Lookup(name)
		if !ok || len(e.Auth) == 0 {
			continue
		}
		if !m.Capabilities.AllowsProvider(name) {
			continue
		}
		wanted := subscriptionDefOf(name) != ""
		for _, v := range e.Detect {
			if strings.TrimSpace(getenv(v)) != "" {
				wanted = true
			}
		}
		if wanted {
			out = append(out, name)
		}
	}
	return out
}

var subscriptionDefs = map[string]string{
	"anthropic": "claudecode",
	"openai":    "codex",
	"cursor":    "cursor",
	"opencode":  "opencode",
}

func subscriptionDefOf(providerName string) string { return subscriptionDefs[providerName] }

// The credentials page's two sections.
const (
	headSubscriptions = "subscriptions"
	headKeys          = "API keys"
)

// credentialRows are page 3's rows, one per slot: the subscriptions section, then the API keys section.
// SPEC: _spec/cmd/proveo/init-credential-wireframe.puml
func credentialRows(slots []credSlot) []choiceui.Row {
	rows := make([]choiceui.Row, 0, len(slots))
	lastGroup := ""
	for _, s := range slots {
		group := headSubscriptions
		if s.Kind == kindKey {
			group = headKeys
		}
		row := choiceui.Row{
			Label:       s.Label,
			Field:       true,
			Masked:      true,
			Held:        s.Held,
			Warn:        len(s.EnvVars) > 0 && !s.Held,
			Placeholder: s.placeholder(),
			Reason:      s.hint(),
		}
		if s.Kind == kindSignIn {
			row = signInRow(s)
		}
		if group != lastGroup {
			row.Divider, row.Heading = true, group
			lastGroup = group
		}
		rows = append(rows, row)
	}
	return rows
}

// The choices a sign-in row offers; the first of each pair changes nothing.
const (
	signedIn    = "signed in"
	notSignedIn = "not signed in"
	signInAgain = "sign in again"
	signInNow   = "sign in"
	signOut     = "sign out"
)

// signInRow offers sbx's own sign-in and sign-out for a subscription's OAuth slot.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R4)
func signInRow(s credSlot) choiceui.Row {
	if s.Held {
		return choiceui.Row{Label: s.Label, Options: []string{signedIn, signInAgain, signOut}, Cols: cols(3), CellRunes: cellRunes, Help: map[string]string{
			signedIn:    s.hint() + " — sbx lists it as present; that is not a check that it still refreshes",
			signInAgain: signInHelp(s),
			signOut:     "after enter: `" + sbx.Binary + " " + strings.Join(sbx.SignOutArgs(s.Store), " ") + "` — sbx asks before it removes the login and any api key stored as " + s.Store + ", for every sandbox",
		}}
	}
	return choiceui.Row{Label: s.Label, Options: []string{notSignedIn, signInNow}, Cols: cols(2), CellRunes: cellRunes, Help: map[string]string{
		notSignedIn: "leave it; " + s.Label + " shows its own login at its next run, and sbx keeps what it signs in",
		signInNow:   signInHelp(s),
	}}
}

func signInHelp(s credSlot) string {
	argv := sbx.SignInArgs(s.Store, "<empty dir>")
	out := "after enter: init names the step — " + sbx.SignIn(s.Store)
	switch {
	case sbx.SignInRuns(argv):
		out = "after enter: `" + sbx.Binary + " " + strings.Join(argv, " ") + "` — sbx keeps the login as " + s.Store + ", and init removes the sandbox"
	case argv != nil:
		out = "after enter: `" + sbx.Binary + " " + strings.Join(argv, " ") + "` — a browser sign-in sbx keeps as " + s.Store
	}
	if s.KeyHeld {
		out += "; the api key stored as " + s.Store + " still outranks it in every sandbox"
	}
	return out
}

// applySignIn runs the sbx command a sign-in row's choice asks for, then reads the store back. Its
// start line prints at once; what it leaves to say comes back as items that need the operator.
func applySignIn(view walkView, s credSlot, choice string, run func(argv []string) error, held func(service string) (held, known bool)) ([]sandbox.Item, error) {
	view = viewOr(view)
	switch choice {
	case signOut:
		view.Status(s.Label, choiceui.MarkRunning, "signing out")
		return nil, run(append([]string{sbx.Binary}, sbx.SignOutArgs(s.Store)...))
	case signInAgain, signInNow:
	default:
		return nil, nil
	}
	if sbx.SignInArgs(s.Store, "<dir>") == nil {
		return []sandbox.Item{{Warn: true, Text: s.Label + " — not signed in", Detail: []string{"sign in with " + sbx.SignIn(s.Store) + "; sbx keeps the login as " + s.Store}}}, nil
	}
	dir := ""
	if sbx.SignInRuns(sbx.SignInArgs(s.Store, "<dir>")) {
		var err error
		if dir, err = os.MkdirTemp("", sbx.SignInSandbox+"-"); err != nil {
			return nil, err
		}
		defer func() { _ = os.RemoveAll(dir) }()
	}
	argv := sbx.SignInArgs(s.Store, dir)
	view.Status(s.Label, choiceui.MarkRunning, "signing in through sbx")
	var items []sandbox.Item
	err := run(append([]string{sbx.Binary}, argv...))
	if sbx.SignInRuns(argv) {
		rm := sbx.RemoveArgs(sbx.SignInSandbox)
		switch rmErr := run(append([]string{sbx.Binary}, rm...)); {
		case rmErr != nil && err == nil:
			items = append(items, sandbox.Item{Warn: true, Text: s.Label + " — sign-in sandbox left",
				Detail: append(errDetail(rmErr), "`"+sbx.Binary+" "+strings.Join(rm, " ")+"` removes it")})
		case rmErr != nil:
			items = append(items, sandbox.Item{Warn: true, Text: s.Label + " — sign-in sandbox may be left",
				Detail: []string{"if " + sbx.SignInSandbox + " remains, `" + sbx.Binary + " " + strings.Join(rm, " ") + "` removes it"}})
		}
	}
	if err != nil {
		return items, err
	}
	switch ok, known := held(s.Store); {
	case !known:
		items = append(items, sandbox.Item{Warn: true, Text: s.Label + " — login unconfirmed", Detail: []string{"`" + sbx.Binary + " secret ls` could not be read"}})
	case !ok:
		return items, fmt.Errorf("the sign-in ended, but sbx lists no OAuth login for %s", s.Store)
	}
	return items, nil
}

// slotKey names a slot across visits of the credentials screen.
func slotKey(s credSlot) string { return fmt.Sprintf("%d|%s|%s", s.Kind, s.Store, s.Label) }

// keepAnswers records what a screen left behind on a step back; restoreAnswers puts it back.
func keepAnswers(slots []credSlot, rows []choiceui.Row, typed map[string]string) {
	for i, s := range slots {
		if i >= len(rows) {
			break
		}
		switch {
		case s.Kind == kindSignIn:
			typed[slotKey(s)] = signInChoice(rows, i)
		case rows[i].Value != "":
			typed[slotKey(s)] = rows[i].Value
		default:
			delete(typed, slotKey(s))
		}
	}
}

func restoreAnswers(slots []credSlot, rows []choiceui.Row, typed map[string]string) {
	for i, s := range slots {
		v, ok := typed[slotKey(s)]
		if !ok || i >= len(rows) {
			continue
		}
		if s.Kind == kindSignIn {
			if j := slices.Index(rows[i].Options, v); j >= 0 {
				rows[i].Selected = j
			}
			continue
		}
		rows[i].Value = v
	}
}

// cardPage is what an init page shares with the card: where it shows, and the done rows of the
// pages before it.
type cardPage struct {
	view walkView
	done []choiceui.Row
}

// credentialStage is `proveo init`'s credentials page, page 3 of the card; typed carries its answers
// across a step back. A forward exit, Skip, and an init that cannot ask leave the result what the
// store holds then; a step back and a quit leave it as it was. done is the page's done row.
func credentialStage(o initOptions, at choiceui.Step, typed map[string]string, rec *initRecord, rep *initReport, page cardPage) (stepResult, pageDone, error) {
	// The embedded registry, not the defs/ tree: init runs on a freshly installed
	// host where no repository exists.
	ms, err := loadManifests()
	if err != nil || len(ms) == 0 {
		return stepNone, nothingToAsk, nil // nothing to provision for; the install is still good
	}
	survey := func() []credSlot { return surveyCredentials(ms, sbx.StoreEntries(), os.Getenv) }
	report := func(slots []credSlot, did map[string]closedRow, items []sandbox.Item, howTo []string) {
		_, warns := credentialFacts(slots)
		warns = append(warns, staleItems(credentials.StaleStoreEntries(sbx.StoredServices(), sbx.CustomSecrets(), defNames(ms)))...)
		rep.setCredentials(credsHeld{slots: slots, did: did, items: append(warns, items...), howTo: howTo})
	}
	slots := survey()
	if o.printOnly || len(slots) == 0 {
		report(slots, nil, nil, nil)
		return stepNone, nothingToAsk, nil
	}
	if o.yes || !viewOr(page.view).card() {
		report(slots, nil, nil, rep.howTo("`proveo init` from a terminal provisions them"))
		return stepNone, nothingToAsk, nil
	}

	rows := credentialRows(slots)
	restoreAnswers(slots, rows, typed)
	form := credentialForm(at, rows, page.done, o.skipSetup)
	view := viewOr(page.view)
	own := func(f *choiceui.Form) []choiceui.Row { return f.Rows[len(page.done) : len(page.done)+len(slots)] }
	form.EnterWord = func(f *choiceui.Form) string { return runWord(len(answeredSlots(slots, own(f), 0))) }
	later := pageDone{mark: choiceui.MarkLater, outcome: "later"}
	out, err := view.Ask(form)
	switch {
	case err != nil:
		clear(typed)
		return stepNext, later, stopAt(labelCredentials, err)
	case out == choiceui.Cancelled || out == choiceui.Skipped:
		clear(typed)
		report(slots, nil, nil, nil)
		return stepNext, pageDone{mark: choiceui.MarkLater, outcome: heldDone(slots, nil).outcome}, nil
	case out == choiceui.Quit:
		clear(typed)
		return stepQuit, later, nil
	}
	answers := slices.Clone(own(form))
	if out == choiceui.Back {
		if typed != nil {
			keepAnswers(slots, answers, typed)
		}
		return stepBack, later, nil
	}
	clear(typed)
	held := func(service string) (bool, bool) { e := sbx.StoreEntries(); return e[service].OAuth, e != nil }
	view.Plan(actingLabels(slots, answers))
	did, items, err := applyCredentials(view, slots, answers, credentialRun(view), held, rec)
	after := survey()
	report(after, did, items, nil)
	done := heldDone(after, did)
	if err != nil {
		return stepNext, done, stopAt(labelCredentials, err)
	}
	if rec != nil {
		rec.did(labelCredentials)
	}
	return stepNext, done, nil
}

// credentialRun runs a page-3 command for slot s: the sign-in sandbox's removal with its output
// kept, every other one handed the terminal under a frame; a sign-in's frame names the slot.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (RUN IN PLACE)
func credentialRun(view walkView) func(s credSlot, argv []string) error {
	return func(s credSlot, argv []string) error {
		if len(argv) > 1 && argv[1] == "rm" {
			return runArgvCaptured(argv)
		}
		c := command(argv[0], argv[1:]...)
		shown, why, done := commandLine(c), "sbx's screen", signedIn
		switch {
		case slices.Contains(argv, "secret"):
			why, done = "sbx asks y/N", "signed out"
		case len(argv) > 1 && argv[1] == "run":
			shown, why = filepath.Base(argv[0])+" run "+s.Label, s.Label+"'s sign-in"
		}
		return view.HandAs(shown, c, []string{why}, said(choiceui.MarkDone, done))
	}
}

// credentialForm is page 3, "Store agent credentials": the pages before it as done rows, the
// subscriptions and API keys sections, the Skip button, then what lies ahead. The cursor opens on
// the top row with an answer.
// SPEC: _spec/cmd/proveo/init-credential-wireframe.puml
func credentialForm(at choiceui.Step, rows, done []choiceui.Row, skipSetup bool) *choiceui.Form {
	all := slices.Concat(done, rows, aheadRows(4, setupAhead(skipSetup)))
	f := initCard(at, "Store agent credentials", []string{"Secrets live in sbx's host-wide store; agents never hold them."}, all)
	f.Skip = "Skip"
	return f
}

// actingLabels are the labels of the rows whose answer runs something, in row order.
func actingLabels(slots []credSlot, rows []choiceui.Row) []string {
	var out []string
	for i, s := range slots {
		if acts(s, rows, i) {
			out = append(out, s.Label)
		}
	}
	return out
}

// acts reports whether row i's answer for s runs something: a sign-in, a sign-out, a key to store.
func acts(s credSlot, rows []choiceui.Row, i int) bool {
	if s.Kind == kindSignIn {
		switch signInChoice(rows, i) {
		case signInNow, signInAgain, signOut:
			return true
		}
		return false
	}
	return slotValue(s, rows, i) != ""
}

// heldDone is page 3's done row: what sbx holds after it, and the rows that failed.
func heldDone(slots []credSlot, did map[string]closedRow) pageDone {
	held := 0
	for _, s := range slots {
		if s.Held {
			held++
		}
	}
	failed := 0
	for _, d := range did {
		if d.mark == choiceui.MarkFailed {
			failed++
		}
	}
	d := pageDone{mark: choiceui.MarkDone, outcome: fmt.Sprintf("%d held · %d not held", held, len(slots)-held)}
	if failed > 0 {
		d.mark, d.outcome = choiceui.MarkNeeds, d.outcome+fmt.Sprintf(" · %d failed", failed)
	}
	return d
}

// applyCredentials signs in and stores what each row answered, in order, each row running in place
// on the card; did is what each acting row came to, by slotKey. A Ctrl+C that ends a sign-in, or
// stops the run, is its error once the row it came on is done; the answered rows after that one
// read not run, and rec holds them as not run.
// SPEC: _spec/cmd/proveo/init-sbx-bootstrap.puml (RUN IN PLACE)
func applyCredentials(view walkView, slots []credSlot, rows []choiceui.Row, run func(s credSlot, argv []string) error, held func(string) (bool, bool), rec *initRecord) (map[string]closedRow, []sandbox.Item, error) {
	view = viewOr(view)
	did := map[string]closedRow{}
	var items []sandbox.Item
	say := func(s credSlot, m choiceui.Mark, outcome string, detail []string) {
		view.Status(s.Label, m, outcome)
		did[slotKey(s)] = closedRow{mark: m, label: s.Label, outcome: outcome, detail: detail}
	}
	stop := handoffErr{err: context.Canceled, interrupted: true}
	for i, s := range slots {
		if !acts(s, rows, i) {
			continue
		}
		if view.Context().Err() != nil {
			credentialsNotRun(view, slots, rows, i, rec, did)
			return did, items, stop
		}
		if s.Kind == kindSignIn {
			said, err := applySignIn(view, s, signInChoice(rows, i), func(argv []string) error { return run(s, argv) }, held)
			if at := slices.IndexFunc(said, func(it sandbox.Item) bool { return it.Text == s.Label+" — "+notSignedIn }); at >= 0 && err == nil {
				say(s, choiceui.MarkNeeds, notSignedIn, said[at].Detail)
				items = append(items, slices.Delete(said, at, at+1)...)
				continue
			}
			items = append(items, said...)
			if interrupted(err) {
				say(s, choiceui.MarkNeeds, "stopped · Ctrl+C ended it", nil)
				credentialsNotRun(view, slots, rows, i+1, rec, did)
				return did, items, err
			}
			if err != nil {
				say(s, choiceui.MarkNeeds, notSignedIn, errDetail(err))
				continue
			}
			outcome := signedIn
			if signInChoice(rows, i) == signOut {
				outcome = "signed out"
			}
			say(s, choiceui.MarkDone, outcome, nil)
			continue
		}
		view.Status(s.Label, choiceui.MarkRunning, "storing in sbx")
		detail, warn, err := provisionSlot(s, slotValue(s, rows, i))
		if err != nil {
			say(s, choiceui.MarkFailed, "not stored", errLines(err))
			continue
		}
		items = append(items, warn...)
		view.Status(s.Label, choiceui.MarkDone, "stored as "+s.Store)
		did[slotKey(s)] = closedRow{mark: choiceui.MarkDone, label: s.Label, outcome: storedText(s, rows, i, detail)}
	}
	if view.Context().Err() != nil {
		return did, items, stop
	}
	return did, items, nil
}

// storedText is what the result says of a slot this init stored: imported from this host's env,
// registered from a reference, or stored as its service.
func storedText(s credSlot, rows []choiceui.Row, i int, detail []string) string {
	what := s.kindWord()
	switch {
	case len(detail) > 0 && strings.HasPrefix(detail[0], "registered from "):
		return what + " " + strings.Replace(detail[0], "; ", " · ", 1)
	case i < len(rows) && strings.TrimSpace(rows[i].Value) == "":
		return what + " imported from this host's env"
	}
	return what + " stored as " + s.Store
}

// kindWord is what a slot holds: a login, a token, or a key.
func (s credSlot) kindWord() string {
	switch s.Kind {
	case kindSignIn:
		return "login"
	case kindSubscription:
		return "token"
	}
	return "key"
}

// credentialsNotRun marks each row at or after from that answered something to run as not run,
// and records it in rec and did.
func credentialsNotRun(view walkView, slots []credSlot, rows []choiceui.Row, from int, rec *initRecord, did map[string]closedRow) {
	for i := from; i < len(slots); i++ {
		if acts(slots[i], rows, i) {
			view.Status(slots[i].Label, choiceui.MarkLater, "not run")
			did[slotKey(slots[i])] = closedRow{mark: choiceui.MarkLater, label: slots[i].Label, outcome: "not run"}
		}
	}
	rec.unreached(answeredSlots(slots, rows, from)...)
}

// slotValue is what row i stores for s: the typed value, else the key this host's env holds and
// the store does not (an amber field left alone is the import); "" stores nothing.
func slotValue(s credSlot, rows []choiceui.Row, i int) string {
	if i < len(rows) {
		if v := strings.TrimSpace(rows[i].Value); v != "" {
			return v
		}
	}
	if len(s.EnvVars) > 0 && !s.Held {
		return strings.TrimSpace(os.Getenv(s.EnvVars[0]))
	}
	return ""
}

// answeredSlots names the rows from i on that the operator answered with something to run.
func answeredSlots(slots []credSlot, rows []choiceui.Row, from int) []string {
	var out []string
	for i := from; i < len(slots); i++ {
		s, choice := slots[i], signInChoice(rows, i)
		switch {
		case s.Kind == kindKey && slotValue(s, rows, i) != "":
			out = append(out, s.Label+" key")
		case s.Kind == kindSubscription && slotValue(s, rows, i) != "":
			out = append(out, s.Label+" token")
		case s.Kind == kindSignIn && choice == signOut:
			out = append(out, s.Label+" sign-out")
		case s.Kind == kindSignIn && (choice == signInNow || choice == signInAgain):
			out = append(out, s.Label+" sign-in")
		}
	}
	return out
}

// signInChoice is row i's selected option, read by position: a sign-in row and a usage key can share a label.
func signInChoice(rows []choiceui.Row, i int) string {
	if i >= len(rows) || rows[i].Selected < 0 || rows[i].Selected >= len(rows[i].Options) {
		return ""
	}
	return rows[i].Options[rows[i].Selected]
}

// credentialFacts are what the store holds, one fact per credential, and a ! for a key this host's
// env holds and the store does not.
func credentialFacts(slots []credSlot) ([]ui.Fact, []sandbox.Item) {
	var facts []ui.Fact
	var warns []sandbox.Item
	for _, s := range slots {
		switch {
		case s.Held:
			facts = append(facts, ui.Fact{Role: ui.RoleStore, Key: s.Label, Value: s.heldText()})
		case len(s.EnvVars) > 0:
			warns = append(warns, sandbox.Item{Warn: true, Text: s.Label + " — " + strings.Join(s.EnvVars, ", ") + " set here, not stored"})
		}
	}
	if len(facts) == 0 && len(warns) == 0 {
		facts = append(facts, ui.Fact{Role: ui.RoleStore, Key: "store", Value: "empty"})
	}
	return facts, warns
}

// heldText is what sbx holds for a held slot: "login held by sbx as openai", "key stored as groq".
func (s credSlot) heldText() string {
	if s.Kind == kindSignIn {
		return "login held by sbx as " + s.Store
	}
	return s.kindWord() + " stored as " + s.Store
}

// credsHeld is what page 3 leaves the result: the slots as sbx holds them after it, what each acting
// row came to by slotKey, the items that need the operator, and the how-to an init with no
// terminal keeps.
type credsHeld struct {
	slots []credSlot
	did   map[string]closedRow
	items []sandbox.Item
	howTo []string
}

// rows are the result's credential rows: one per slot sbx holds or a row acted on, in the page's
// order, then one row for what sbx does not hold.
// SPEC: _spec/cmd/proveo/init-preflight-and-result.puml (THE CLOSED CHECKLIST)
func (c credsHeld) rows() []closedRow {
	var rows []closedRow
	notHeld := map[credKind][]string{}
	for _, s := range c.slots {
		d, acted := c.did[slotKey(s)]
		switch {
		case acted && d.mark != choiceui.MarkDone:
			rows = append(rows, d)
		case s.Held && acted && s.Kind != kindSignIn:
			rows = append(rows, d)
		case s.Held:
			rows = append(rows, closedRow{mark: choiceui.MarkDone, label: s.Label, outcome: s.heldText()})
		case acted:
			rows = append(rows, d)
		default:
			notHeld[s.Kind] = append(notHeld[s.Kind], s.Label)
		}
	}
	var parts []string
	for _, k := range []credKind{kindSignIn, kindSubscription, kindKey} {
		if names := notHeld[k]; len(names) > 0 {
			parts = append(parts, strings.Join(names, ", ")+" "+map[credKind]string{kindSignIn: "sign-in", kindSubscription: "token", kindKey: "key"}[k]+plural(len(names)))
		}
	}
	if len(parts) > 0 {
		rows = append(rows, closedRow{mark: choiceui.MarkLater, label: "not held", outcome: strings.Join(parts, " · "), detail: c.howTo})
	}
	return rows
}

// plural is "s" for a count other than one.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// staleItems is one ⚠ for the store entries sbx never reads, with the command that removes each.
func staleItems(argvs [][]string) []sandbox.Item {
	if len(argvs) == 0 {
		return nil
	}
	it := sandbox.Item{Warn: true, Text: fmt.Sprintf("store — %d entries sbx never reads", len(argvs))}
	for _, a := range argvs {
		it.Detail = append(it.Detail, "`"+sbx.Binary+" "+strings.Join(a, " ")+"`")
	}
	return []sandbox.Item{it}
}

func defNames(ms []manifest.Manifest) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out
}

// provisionSlot writes one credential under its service id: a reference as an sbx source, a value
// on stdin. detail is what it did, for the stored credential's fact; a key the provider refuses is a ⚠.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R2)
func provisionSlot(s credSlot, value string) (detail []string, warn []sandbox.Item, err error) {
	flags, stdin := secretref.SbxSource(s.Store, value, os.Getenv)
	if len(flags) > 0 {
		if err := sbx.SecretSetSource(s.Store, "", flags, ""); err != nil {
			return nil, nil, fmt.Errorf("register %s: %w", s.Store, err)
		}
		return []string{"registered from " + strings.TrimSpace(value) + "; sbx resolves it on the host"}, nil, nil
	}
	if err := sbx.SecretSet(s.Store, stdin); err != nil {
		return nil, nil, fmt.Errorf("store as %s: %w", s.Store, err)
	}
	switch ok, why := verifySlot(s, stdin); {
	case ok:
		return []string{"stored now; the provider accepted it"}, nil, nil
	case why != "":
		return []string{"stored now"}, []sandbox.Item{{Warn: true, Text: s.Label + " — stored, not spendable", Detail: []string{why}}}, nil
	}
	return []string{"stored now"}, nil, nil
}

// verifyProbes is the table of authenticated probes, one per provider.
var verifyProbes = map[string]struct {
	URL    string
	Header map[string]string
}{
	"anthropic":  {URL: "https://api.anthropic.com/v1/models", Header: map[string]string{"anthropic-version": "2023-06-01"}},
	"openai":     {URL: "https://api.openai.com/v1/models"},
	"openrouter": {URL: "https://openrouter.ai/api/v1/models"},
	"groq":       {URL: "https://api.groq.com/openai/v1/models"},
	"mistral":    {URL: "https://api.mistral.ai/v1/models"},
	"xai":        {URL: "https://api.x.ai/v1/models"},
	"opencode":   {URL: "https://opencode.ai/zen/v1/models"},
}

var verifyClient = &http.Client{Timeout: 15 * time.Second}

func verifySlot(s credSlot, value string) (ok bool, detail string) {
	if s.Kind != kindKey {
		return false, "" // a plan token has no models endpoint to ask
	}
	probe, known := verifyProbes[s.Store]
	if !known {
		return false, ""
	}
	req, err := http.NewRequest(http.MethodGet, probe.URL, nil)
	if err != nil {
		return false, err.Error()
	}
	res, hasAuth := provider.Resolve(s.Store, func(string) string { return value })
	switch {
	case hasAuth && res.Header != "" && res.Bearer:
		req.Header.Set(res.Header, "Bearer "+value)
	case hasAuth && res.Header != "":
		req.Header.Set(res.Header, value)
	default:
		req.Header.Set("authorization", "Bearer "+value)
	}
	for k, v := range probe.Header {
		req.Header.Set(k, v)
	}
	resp, err := verifyClient.Do(req)
	if err != nil {
		return false, "could not reach " + probe.URL
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))
	if resp.StatusCode/100 == 2 {
		return true, ""
	}
	return false, fmt.Sprintf("the provider answered HTTP %d", resp.StatusCode)
}
