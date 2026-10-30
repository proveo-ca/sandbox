// SPEC: _spec/_plans/init-credential-provisioning.puml, _spec/cmd/proveo/init-credential-wireframe.puml
// SPEC: _spec/cmd/proveo/init-credential-entry.puml, _spec/_paradigms/credential-boundary.puml

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/posture"
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
					out = append(out, credSlot{Label: m.Name, Kind: kindSignIn, Store: svc, Def: m.Name, Held: store[svc].OAuth})
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

func credentialRows(slots []credSlot) []choiceui.Row {
	rows := make([]choiceui.Row, 0, len(slots))
	lastGroup := ""
	for _, s := range slots {
		group := s.Def + " — subscription"
		if s.Kind == kindKey {
			group = "usage api keys"
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
			state := "not signed in"
			if s.Held {
				state = "signed in"
			}
			row = choiceui.Row{Label: s.Label, Options: []string{state}, Locked: true, Reason: s.hint()}
		}
		if group != lastGroup {
			row.Divider, row.Heading = true, group
			lastGroup = group
		}
		rows = append(rows, row)
	}
	return rows
}

// credentialStage is `proveo init`'s fourth stage.
func credentialStage(o initOptions) error {
	// The embedded registry, not the defs/ tree: init runs on a freshly installed
	// host where no repository exists.
	ms, err := loadManifests()
	if err != nil || len(ms) == 0 {
		return nil // nothing to provision for; the install is still good
	}
	slots := surveyCredentials(ms, sbx.StoreEntries(), os.Getenv)
	reportCredentials(slots)
	reportStaleEntries(credentials.StaleStoreEntries(sbx.StoredServices(), sbx.CustomSecrets(), defNames(ms)))
	if o.printOnly || len(slots) == 0 {
		return nil
	}
	if !interactiveTerminal() {
		ui.Notef("not a terminal — run `proveo init` from a shell to provision credentials")
		return nil
	}

	form := &choiceui.Form{
		Banner: choiceui.Banner(),
		Title:  "credentials — one field per credential, checked when sbx holds it",
		Header: []string{
			"A stored secret is host-wide and outlives every run; the agent never holds it.",
			"Without one an agent still launches and shows its own login — /login is the agent's, not proveo's.",
			"Typing replaces what is stored. An empty field changes nothing.",
		},
		Glyphs: posture.GlyphModeFrom(os.Getenv),
		Rows:   credentialRows(slots),
	}
	ok, err := form.Run()
	if err != nil || !ok {
		return err
	}
	for i, s := range slots {
		value := ""
		if i < len(form.Rows) {
			value = strings.TrimSpace(form.Rows[i].Value)
		}
		if s.Kind == kindSignIn {
			continue
		}
		if value == "" {
			// An amber field left alone is the import: the key is already on this
			// host, and asking for it again is asking someone to find it twice.
			if len(s.EnvVars) > 0 && !s.Held {
				value = strings.TrimSpace(os.Getenv(s.EnvVars[0]))
			}
			if value == "" {
				continue
			}
		}
		if err := provisionSlot(s, value); err != nil {
			ui.Warnf("%s: %v", s.Label, err)
		}
	}
	return nil
}

func reportCredentials(slots []credSlot) {
	ui.Section(ui.SectionSecrets)
	any := false
	for _, s := range slots {
		switch {
		case s.Held:
			any = true
			ui.Storef("%s — %s", s.Label, s.hint())
		case len(s.EnvVars) > 0:
			any = true
			ui.Warnf("%s — %s", s.Label, s.hint())
		}
	}
	if !any {
		ui.Notef("nothing in the store yet — every agent will meet its own login screen")
	}
}

// reportStaleEntries lists the store entries sbx never reads, each with the command that removes it.
func reportStaleEntries(argvs [][]string) {
	if len(argvs) == 0 {
		return
	}
	ui.Warnf("%d store entries sbx never reads — older proveo wrote them under env-var or def names; remove with:", len(argvs))
	for _, a := range argvs {
		ui.Notef("  %s %s", sbx.Binary, strings.Join(a, " "))
	}
}

func defNames(ms []manifest.Manifest) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out
}

// provisionSlot writes one credential under its service id: a reference as an sbx source, a value on stdin.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R2)
func provisionSlot(s credSlot, value string) error {
	flags, stdin := secretref.SbxSource(s.Store, value, os.Getenv)
	if len(flags) > 0 {
		if err := sbx.SecretSetSource(s.Store, "", flags, ""); err != nil {
			return fmt.Errorf("register %s: %w", s.Store, err)
		}
		ui.Storef("%s registered as %s from a source sbx resolves on the host (%s); proveo never reads the value", s.Label, s.Store, flags[0])
		return nil
	}
	if err := sbx.SecretSet(s.Store, stdin); err != nil {
		return fmt.Errorf("store as %s: %w", s.Store, err)
	}
	value = stdin
	ui.Storef("%s stored as %s", s.Label, s.Store)
	switch ok, detail := verifySlot(s, value); {
	case ok:
		ui.Hostf("%s: the provider accepted it", s.Label)
	case detail != "":
		ui.Warnf("%s: stored, but %s — present is not spendable", s.Label, detail)
	}
	return nil
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
