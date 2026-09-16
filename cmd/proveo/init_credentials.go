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
	"github.com/proveo-ca/proveo/internal/ui"
)

// credKind says which of the two things a slot holds. One store id cannot carry
// both: a proxy attaching an OAuth token into an x-api-key header answers 401 to
// a credential that is good.
type credKind int

const (
	kindKey credKind = iota
	kindSubscription
)

// credSlot is one line of the screen: a credential, where it rests today, and
// the name it takes in the store.
type credSlot struct {
	Label   string // what the operator reads: the provider, or the def for a subscription
	Kind    credKind
	Store   string   // the name `sbx secret set` is given
	Also    []string // the env var names the forward path and every harness read
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
	case s.Held:
		return "stored as " + strings.Join(append([]string{s.Store}, s.Also...), " and ")
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

// subscriptionStore keeps the two kinds apart. A subscription takes the def
// name; where the def is NAMED for its provider that would collide with the api
// key's id, so it takes <service>-sub instead.
func subscriptionStore(providerName, def string) string {
	if def != "" && def != providerName {
		return def
	}
	if def != "" {
		return def + "-sub"
	}
	return providerName + "-sub"
}

// narrowProviders is the def's own vendor list. An empty list means the def
// routes anywhere — opencode and cecli — and gets the short list instead.
func narrowProviders(m manifest.Manifest) []string {
	if len(m.Capabilities.Providers) == 0 || len(m.Capabilities.Providers) > 3 {
		return nil
	}
	return m.Capabilities.Providers
}

// surveyCredentials builds the screen: one group per def, its subscription
// first, then the api keys that def can actually spend. A def that routes
// anywhere gets the short list — providers a def runs on, plus any key already
// on this host — because drawing all thirty buries the four that decide whether
// any agent starts.
func surveyCredentials(ms []manifest.Manifest, stored []string, getenv func(string) string) []credSlot {
	held := map[string]bool{}
	for _, s := range stored {
		held[strings.ToLower(strings.TrimSpace(s))] = true
	}
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
	keyVarsOf := func(name string) []string {
		e, ok := provider.Lookup(name)
		if !ok {
			return nil
		}
		var out []string
		for _, v := range e.Detect {
			if strings.HasSuffix(v, "_API_KEY") {
				out = append(out, v)
			}
		}
		return out
	}
	storedUnder := func(names ...string) bool {
		for _, n := range names {
			if held[strings.ToLower(n)] {
				return true
			}
		}
		return false
	}

	sort.Slice(ms, func(i, j int) bool { return ms[i].Name < ms[j].Name })
	var out []credSlot
	seen := map[string]bool{}
	for _, m := range ms {
		own := narrowProviders(m)
		if m.Subscription {
			vendor := ""
			if len(own) > 0 {
				vendor = own[0]
			}
			store := subscriptionStore(vendor, m.Name)
			if !seen[store] {
				seen[store] = true
				out = append(out, credSlot{
					Label: m.Name, Kind: kindSubscription, Store: store, Def: m.Name,
					Held: storedUnder(store), EnvVars: subscriptionEnvVars(m, getenv),
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
			vars := keyVarsOf(name)
			out = append(out, credSlot{
				Label: name, Kind: kindKey, Store: name, Also: vars, Def: m.Name,
				Held: storedUnder(append([]string{name}, vars...)...), EnvVars: envVarsOf(name),
			})
		}
	}
	return out
}

// subscriptionEnvVars are the non-key vars that carry a plan token, so a token
// already exported on this host can be imported rather than retyped.
func subscriptionEnvVars(m manifest.Manifest, getenv func(string) string) []string {
	var out []string
	for _, e := range m.Env {
		if !e.Secret || strings.HasSuffix(e.Name, "_API_KEY") {
			continue
		}
		if strings.TrimSpace(getenv(e.Name)) != "" {
			out = append(out, e.Name)
		}
	}
	return out
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
			group = s.Def + " — api keys"
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
		if group != lastGroup {
			row.Divider, row.Heading = true, group
			lastGroup = group
		}
		rows = append(rows, row)
	}
	return rows
}

// credentialStage is `proveo init`'s fourth stage, and the last screen of the
// install: the one place a credential is provisioned, so nobody has to know
// which env var a harness reads. It offers; it never gates the run, because an
// agent without a credential still launches and shows its own login.
func credentialStage(o initOptions) error {
	// The embedded registry, not the defs/ tree: init runs on a freshly installed
	// host where no repository exists.
	ms, err := loadManifests()
	if err != nil || len(ms) == 0 {
		return nil // nothing to provision for; the install is still good
	}
	slots := surveyCredentials(ms, sbx.StoredSecretNames(), os.Getenv)
	reportCredentials(slots)
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
	// By index, not by label: a def named for its own provider gives the
	// subscription and the key the same label — cursor and cursor — and a
	// lookup by name would hand one field's value to the other credential.
	for i, s := range slots {
		value := ""
		if i < len(form.Rows) {
			value = strings.TrimSpace(form.Rows[i].Value)
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

// provisionSlot writes one credential under BOTH names: the service id the
// broker path reads, and the env var name the forward path and every harness
// read. Until that map lands, one secret occupies two entries — and an operator
// reading `sbx secret ls` would otherwise find a duplicate they did not create.
func provisionSlot(s credSlot, value string) error {
	names := append([]string{s.Store}, s.Also...)
	for _, n := range names {
		if err := sbx.SecretSet(n, value); err != nil {
			return fmt.Errorf("store as %s: %w", n, err)
		}
	}
	ui.Storef("%s stored as %s", s.Label, strings.Join(names, " and "))
	switch ok, detail := verifySlot(s, value); {
	case ok:
		ui.Hostf("%s: the provider accepted it", s.Label)
	case detail != "":
		ui.Warnf("%s: stored, but %s — present is not spendable", s.Label, detail)
	}
	return nil
}

// verifyProbes is how init answers "spendable" rather than "present". A
// provider with no cheap authenticated endpoint is reported as unverified,
// never as good and never as bad.
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
