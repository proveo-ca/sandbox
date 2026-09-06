// SPEC: _spec/_paradigms/credential-boundary.puml,
// _spec/internal/credentials/credential-decisions.puml Package credentials
// decides what a run authenticates with, and writes nothing else.
//
// SPEC: _spec/_paradigms/credential-boundary.puml, _spec/internal/credentials/credential-decisions.puml
package credentials

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/proveohome"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/runner"
	"github.com/proveo-ca/proveo/internal/secretref"
	"github.com/proveo-ca/proveo/internal/ui"
)

func JoinDomains(env string, hosts []string) string {
	parts := strings.Fields(env)
	parts = append(parts, hosts...)
	return strings.Join(parts, " ")
}

func ReachableHosts(detected []string) []string {
	var out []string
	for _, name := range detected {
		if e, ok := provider.Lookup(name); ok {
			out = append(out, e.Hosts...)
		}
	}
	return out
}

func FilterProviders(detected []string, c manifest.Capabilities) []string {
	if len(c.Providers) == 0 {
		return detected
	}
	out := make([]string, 0, len(detected))
	for _, d := range detected {
		if c.AllowsProvider(d) {
			out = append(out, d)
		}
	}
	return out
}

// The auth row asks ONE question — is this run billed against a PLAN, or
// metered per token — and every harness but cecli answers it the same way. The
// variables and files behind each side are the HINT, resolved per host by
// AuthBacking. Declared riskier-first: that is the axis the prompt draws.
// SPEC: _spec/internal/credentials/credential-decisions.puml,
// _spec/internal/choiceui/wireframe.puml
const (
	// AuthUsage bills per token at each provider, against the operator's own keys.
	AuthUsage = "usage credits"
	// AuthSubscription bills against a plan: the vendor's own key or login.
	AuthSubscription = "subscription"
	// AuthLocal is the safe end and is not wired up yet: drawn and gated, because
	// the axis is where an operator finds out an option exists at all.
	// SPEC: _spec/internal/choiceui/wireframe.puml
	AuthLocal = "local model"
	// AuthVarLogin is no longer offered — a login IS a subscription — but
	// remembered answers in ~/.proveo still carry it, so it stays resolvable.
	// SPEC: _spec/internal/agentsettings/choice-cache.puml
	AuthVarLogin = "login (proveo home)"
)

// IsAuthSentinel reports whether an auth answer names a credential CLASS rather
// than an environment variable. Anything that reaches a `-e NAME` or the
// broker's preferred-variable hint has to be filtered through this first.
func IsAuthSentinel(v string) bool {
	switch strings.TrimSpace(v) {
	case AuthUsage, AuthSubscription, AuthLocal, AuthVarLogin:
		return true
	}
	return false
}

func AvailableAuthVars(man manifest.Manifest, lookup func(string) string) []string {
	return AvailableAuthVarsIn(man, lookup, "", "")
}

// AvailableAuthVarsIn lists the classes this run could actually be billed as,
// riskier first. Empty means nothing can authenticate at all.
func AvailableAuthVarsIn(man manifest.Manifest, lookup func(string) string, target, homeRoot string) []string {
	var out []string
	if len(ProviderKeyVars(man, lookup)) > 0 {
		out = append(out, AuthUsage)
	}
	if len(SubscriptionVars(man, lookup)) > 0 || HasPersistedLogin(target, homeRoot) {
		out = append(out, AuthSubscription)
	}
	return out
}

// HasUsableAuth reports whether ANYTHING can authenticate this run: a login on
// disk, the harness's own plan credential, or — where the harness reads them —
// the operator's per-provider keys. It is the guard on every "this harness has
// no credential" path, which is a question about the RUN and not about the
// declared variable. SPEC: _spec/internal/credentials/credential-decisions.puml
func HasUsableAuth(man manifest.Manifest, target, homeRoot string, lookup func(string) string) bool {
	return len(AvailableAuthVarsIn(man, lookup, target, homeRoot)) > 0
}

// SubscriptionVars lists the harness's OWN declared credentials that are set —
// the vendor's plan key or login token, in manifest order.
func SubscriptionVars(man manifest.Manifest, lookup func(string) string) []string {
	var out []string
	for _, e := range man.Env {
		if e.Secret && lookup != nil && strings.TrimSpace(lookup(e.Name)) != "" {
			out = append(out, e.Name)
		}
	}
	return out
}

// DeclaresSubscription reports whether the harness has a plan side at all. cecli
// is the one def that does not: it is an aider fork with no vendor of its own,
// so there is no question to put to the operator and no row to draw.
func DeclaresSubscription(man manifest.Manifest) bool { return len(subscriptionNames(man)) > 0 }

func subscriptionNames(man manifest.Manifest) map[string]bool {
	out := map[string]bool{}
	for _, e := range man.Env {
		if e.Secret {
			out[e.Name] = true
		}
	}
	return out
}

// ProviderKeyVars lists the per-provider keys this harness may actually use: set
// on the host, belonging to a provider the manifest allows, and not the
// harness's own declared credential.
func ProviderKeyVars(man manifest.Manifest, lookup func(string) string) []string {
	own := subscriptionNames(man)
	family := HarnessFamily(man.Name)
	var out []string
	for _, name := range FilterProviders(provider.Detect(lookup), man.Capabilities) {
		// AuthVarsFor, not AuthVars: another harness's plan credential is not a
		// key this run can spend. SPEC: _spec/internal/provider/provider-registry.puml
		for _, v := range provider.AuthVarsFor(name, family) {
			if own[v] || strings.TrimSpace(lookup(v)) == "" {
				continue
			}
			out = append(out, v)
		}
	}
	return out
}

// VendorPinnedWhy explains why a harness cannot be billed as usage credits, or
// "" when it can. A `providers:` list of exactly the harness's own vendor is the
// declaration that all inference transits that vendor — cursor, whose CLI has no
// bring-your-own-key path at all. SPEC: _spec/defs/cursor/cursor-paradigm.puml
func VendorPinnedWhy(man manifest.Manifest) string {
	if len(man.Capabilities.Providers) != 1 {
		return ""
	}
	vendor := strings.TrimSpace(man.Capabilities.Providers[0])
	for name := range subscriptionNames(man) {
		if !strings.EqualFold(ProviderOfKeyVar(name), vendor) {
			return ""
		}
	}
	return "all inference transits " + vendor + " — this CLI has no bring-your-own-key path, " +
		"so a provider key here authenticates nothing"
}

// AuthBacking is what each option is MADE of on this host: the variables it
// reads and the files it reads them from — three different credentials with
// three different bills. SPEC: _spec/internal/choiceui/wireframe.puml
func AuthBacking(man manifest.Manifest, lookup func(string) string, target, homeRoot, envFile string) map[string]string {
	inFile := ParseEnvFile(envFile)
	from := func(names []string) string {
		if len(names) == 0 {
			return ""
		}
		out := strings.Join(names, ", ")
		for _, n := range names {
			if _, ok := inFile[n]; ok {
				return out + " — host env, else " + envFile
			}
		}
		return out + " — host env"
	}

	backing := map[string]string{}
	if keys := ProviderKeyVars(man, lookup); len(keys) > 0 {
		backing[AuthUsage] = from(keys)
	}

	var plan []string
	if v := from(SubscriptionVars(man, lookup)); v != "" {
		plan = append(plan, v)
	}
	for _, rel := range subscriptionLoginFiles[target] {
		if homeRoot == "" {
			continue
		}
		if path := filepath.Join(homeRoot, rel); HasPersistedLogin(target, homeRoot) {
			plan = append(plan, "login "+path)
		}
	}
	if len(plan) > 0 {
		backing[AuthSubscription] = strings.Join(plan, " · ")
	}
	return backing
}

// AuthWhyUnavailable is the other half: what the operator would have to produce
// to make an option selectable. An option gated with no reason is a dead end.
func AuthWhyUnavailable(man manifest.Manifest, target, homeRoot string) map[string]string {
	why := map[string]string{}
	if pinned := VendorPinnedWhy(man); pinned != "" {
		why[AuthUsage] = pinned
	} else {
		why[AuthUsage] = "no provider key is set — export one (ANTHROPIC_API_KEY, OPENAI_API_KEY, …) " +
			"or put it in the project .env"
	}
	why[AuthLocal] = "coming soon — `--local-model` already runs the Ollama sidecar, " +
		"but nothing routes this row to it yet"
	var names []string
	for _, e := range man.Env {
		if e.Secret {
			names = append(names, e.Name)
		}
	}
	switch {
	case len(names) == 0:
		why[AuthSubscription] = man.Name + " has no plan of its own — it reads provider keys only"
	case len(subscriptionLoginFiles[target]) > 0:
		why[AuthSubscription] = "no " + strings.Join(names, " / ") + " is set, and the proveo home holds " +
			"no login (" + filepath.Join(homeRoot, subscriptionLoginFiles[target][0]) + ")"
	default:
		why[AuthSubscription] = "no " + strings.Join(names, " / ") + " is set"
	}
	return why
}

func GhConfigMount(getenv func(string) string) (runner.Mount, bool) {
	switch strings.ToLower(strings.TrimSpace(getenv("PROVEO_MOUNT_GH_CONFIG"))) {
	case "0", "off", "no", "false":
		return runner.Mount{}, false
	}
	dir := strings.TrimSpace(getenv("GH_CONFIG_DIR"))
	if dir == "" {
		home := getenv("HOME")
		if home == "" {
			return runner.Mount{}, false
		}
		dir = filepath.Join(home, ".config", "gh")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return runner.Mount{}, false
	}
	return runner.Mount{
		Host:      dir,
		Container: proveohome.ContainerHome + "/.config/gh",
		ReadOnly:  true,
	}, true
}

var agentTranscriptDirs = map[string][]string{
	"claudecode": {".claude/projects"},
}

func AgentTranscript(target, homeRoot string, since, until time.Time) string {
	if homeRoot == "" {
		return ""
	}
	newest, newestAt := "", since
	for _, rel := range agentTranscriptDirs[target] {
		root := filepath.Join(homeRoot, rel)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil //nolint:nilerr // an unreadable home is not a run failure
			}
			fi, err := d.Info()
			switch {
			case err != nil, fi.Size() == 0:
				return nil
			case !fi.ModTime().After(newestAt):
				return nil
			case !until.IsZero() && fi.ModTime().After(until):
				return nil // written after the run ended: the harvest's, not the run's
			}
			newest, newestAt = p, fi.ModTime()
			return nil
		})
	}
	return newest
}

var subscriptionLoginFiles = map[string][]string{
	"claudecode": {".claude/.credentials.json"},
}

// EffectiveAuthVar resolves an answer to the ONE variable the broker should
// prefer, or "" when the answer names no variable at all. It still accepts a
// bare variable name: remembered answers predate the two-class row.
// SPEC: _spec/internal/agentsettings/choice-cache.puml
func EffectiveAuthVar(man manifest.Manifest, target, chosen, homeRoot string, lookup func(string) string) string {
	switch v := strings.TrimSpace(chosen); v {
	case AuthUsage:
		// Several keys can back this at once — the role bridges point different
		// model slots at different vendors — so there is no single one to prefer.
		return ""
	case AuthSubscription:
		if names := SubscriptionVars(man, lookup); len(names) > 0 {
			return names[0]
		}
		// Nothing in the environment: the login on disk IS the subscription.
	case AuthVarLogin, "":
	default:
		return v
	}
	if !HasPersistedLogin(target, homeRoot) {
		return ""
	}
	for _, e := range man.Env {
		if e.Secret {
			return e.Name // the harness's declared plan credential
		}
	}
	return ""
}

func HasPersistedLogin(target, homeRoot string) bool {
	ok, _ := PersistedLogin(target, homeRoot)
	return ok
}

func PersistedLogin(target, homeRoot string) (ok, needsRefresh bool) {
	if homeRoot == "" {
		return false, false
	}
	for _, rel := range subscriptionLoginFiles[target] {
		if usable, refresh := loginUsable(filepath.Join(homeRoot, rel), time.Now()); usable {
			return true, refresh
		}
	}
	return false, false
}

type oauthCredential struct {
	ClaudeAIOauth struct {
		ExpiresAt             *int64   `json:"expiresAt"`             // ms since epoch
		RefreshTokenExpiresAt *int64   `json:"refreshTokenExpiresAt"` // ms since epoch
		AccessToken           *string  `json:"accessToken"`
		RefreshToken          *string  `json:"refreshToken"`
		Scopes                []string `json:"scopes"`
		SubscriptionType      string   `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
}

func tokenCleared(tok *string) bool { return tok != nil && *tok == "" }

func loginUsable(path string, now time.Time) (usable, needsRefresh bool) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return false, false
	}
	return loginUsableBytes(b, now)
}

func loginUsableBytes(b []byte, now time.Time) (usable, needsRefresh bool) {
	var c oauthCredential
	if json.Unmarshal(b, &c) != nil {
		return true, false // presence is all this file lets us honestly assert
	}
	o := &c.ClaudeAIOauth
	if tokenCleared(o.AccessToken) {
		return false, false
	}
	if o.ExpiresAt == nil {
		return true, false
	}
	if now.Before(time.UnixMilli(*o.ExpiresAt)) {
		return true, false
	}
	if r := o.RefreshTokenExpiresAt; r != nil && !tokenCleared(o.RefreshToken) && now.Before(time.UnixMilli(*r)) {
		return true, true
	}
	return false, false
}

// SPEC: _spec/internal/secretref/secret-references.puml
func LoginBlanked(target, homeRoot string) bool {
	if homeRoot == "" {
		return false
	}
	for _, rel := range subscriptionLoginFiles[target] {
		b, err := os.ReadFile(filepath.Join(homeRoot, rel))
		if err != nil || len(b) == 0 {
			continue
		}
		var c oauthCredential
		if json.Unmarshal(b, &c) != nil {
			continue
		}
		if tokenCleared(c.ClaudeAIOauth.AccessToken) {
			return true
		}
	}
	return false
}

func AuthSuppressor(man manifest.Manifest, target, chosen, homeRoot string, lookup func(string) string) func(string) bool {
	chosen = strings.TrimSpace(chosen)
	usableLogin, staleLogin := PersistedLogin(target, homeRoot)
	usableLogin = usableLogin && !staleLogin
	// An explicit "subscription" lands here too when a login is on disk: the
	// login IS the plan credential and out-ranks an env token of the same
	// provider. SPEC: _spec/_paradigms/credential-boundary.puml
	if chosen == AuthVarLogin || ((chosen == "" || chosen == AuthSubscription) && usableLogin) {
		owned := map[string]bool{}
		for _, e := range man.Env {
			if e.Secret {
				if prov := ProviderOfKeyVar(e.Name); prov != "" {
					owned[prov] = true
				}
			}
		}
		return func(k string) bool {
			prov := ProviderOfKeyVar(k)
			return prov != "" && owned[prov]
		}
	}
	if chosen == AuthUsage {
		// The operator's own keys win, so the harness's plan credential is the one
		// that must not arrive: set, it is what the agent reaches for first.
		own := subscriptionNames(man)
		return func(k string) bool { return own[k] }
	}
	auth := EffectiveAuthVar(man, target, chosen, homeRoot, lookup)
	if (chosen == AuthSubscription || subscriptionNames(man)[auth]) && VendorPinnedWhy(man) == "" {
		// The far side of the same choice: a harness that can read either must
		// not read BOTH once the operator has said which. Vendor-pinned
		// harnesses are exempt — they have no far side.
		return func(k string) bool {
			switch {
			case k == auth:
				return false
			case LosesToChosenAuth(k, auth):
				return true
			}
			return isProviderKeyVar(k) && ProviderOfKeyVar(k) != ProviderOfKeyVar(auth)
		}
	}
	return func(k string) bool { return LosesToChosenAuth(k, auth) }
}

func isProviderKeyVar(k string) bool {
	_, _, ok := ProviderForAuthVar(k)
	return ok
}

func LosesToChosenAuth(k, chosen string) bool {
	chosen = strings.TrimSpace(chosen)
	if chosen == "" || k == chosen {
		return false
	}
	prov := ProviderOfKeyVar(chosen)
	if prov == "" || ProviderOfKeyVar(k) != prov {
		return false
	}
	for _, alt := range provider.AuthVars(prov) {
		if alt == k {
			return true // same provider, different auth: the operator chose the other
		}
	}
	return false
}

func LoadedSecretNames(man manifest.Manifest, lookup func(string) string) []string {
	seen, out := map[string]bool{}, []string{}
	add := func(k string) {
		if k != "" && !seen[k] && strings.TrimSpace(lookup(k)) != "" {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, e := range man.Env {
		if e.Secret {
			add(e.Name)
		}
	}
	for _, name := range provider.Names() {
		if !man.Capabilities.AllowsProvider(name) {
			continue
		}
		e, _ := provider.Lookup(name)
		for _, k := range e.Detect {
			add(k)
		}
	}
	return out
}

func BrokerProviders(forwards bool, man manifest.Manifest, detected []string, lookup func(string) string, brokerOn bool) []string {
	if forwards || !brokerOn {
		return nil
	}
	if pin := strings.TrimSpace(man.Provider); pin != "" {
		e, ok := provider.Lookup(pin)
		if !ok {
			return nil
		}
		for _, v := range e.Detect {
			if strings.TrimSpace(lookup(v)) != "" {
				return []string{pin}
			}
		}
		return nil
	}
	return detected
}

func ConfigVarsFor(man manifest.Manifest) []string {
	out := append([]string(nil), entrypoint.ConfigVars...)
	seen := make(map[string]bool, len(out))
	for _, k := range out {
		seen[k] = true
	}
	for _, k := range man.Config {
		if k = strings.TrimSpace(k); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func BrokerOffReason(forwards bool, routed []string, detected []string, brokerOn bool) string {
	if forwards || len(routed) > 0 || len(detected) == 0 {
		return ""
	}
	if !brokerOn {
		return fmt.Sprintf("credential broker disabled (PROVEO_CREDENTIAL_BROKER) — the agent gets the %q "+
			"sentinel, not a working key", entrypoint.DefaultSentinel)
	}
	return fmt.Sprintf("credential broker OFF: %d key(s) detected (%s) but none is broker-injectable — "+
		"the agent will receive the %q sentinel and the provider will reject it. Use --credentials forward "+
		"to hand the real key to the container.",
		len(detected), strings.Join(detected, ", "), entrypoint.DefaultSentinel)
}

func ProviderOfKeyVar(envVar string) string {
	if name, _, ok := ProviderForAuthVar(envVar); ok {
		return name
	}
	return envVar
}

func ProviderForAuthVar(envVar string) (string, provider.AuthOption, bool) {
	for _, n := range provider.Names() {
		e, ok := provider.Lookup(n)
		if !ok {
			continue
		}
		for _, a := range e.Auth {
			if strings.EqualFold(a.EnvVar, envVar) {
				return e.Name, a, true
			}
		}
	}
	return "", provider.AuthOption{}, false
}

func WarnMountedSecrets(dir, mode string, sandboxed bool, lookup func(string) string) {
	if dir == "" {
		return
	}
	if !sandboxed {
		switch strings.ToLower(mode) {
		case "allowlist", "review":
			return // proveo masks .env* with /dev/null on these tiers
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err != nil {
		return
	}
	if len(provider.Detect(lookup)) == 0 {
		return
	}
	if sandboxed {
		ui.Warnf("%s/.env is inside the sandbox workspace and holds a provider key — the "+
			"agent's prelude sources it with `set -a`, so the key becomes agent environment "+
			"whatever egress tier you picked", dir)
		return
	}
	ui.Warnf("%s/.env is mounted and a provider key is set — the agent can read it directly; use --egress-mode firewall so egress DLP blocks the key from leaving", dir)
}

// ChildEnv carries the values a bare `-e NAME` needs the CHILD to inherit,
// keeping them out of proveo's own environ.
type ChildEnv struct {
	pairs []string
	seen  map[string]bool
}

// Add records name's value for the child, resolved through lookup.
func (c *ChildEnv) Add(name string, lookup func(string) string) {
	if c.seen[name] || strings.TrimSpace(os.Getenv(name)) != "" {
		return
	}
	v := ""
	if lookup != nil {
		v = strings.TrimSpace(lookup(name))
	}
	if v == "" {
		return
	}
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	c.seen[name] = true
	c.pairs = append(c.pairs, name+"="+v)
}

// Pairs is the "KEY=VALUE" list for one exec's cmd.Env, nil when empty.
func (c *ChildEnv) Pairs() []string { return c.pairs }

// Names lists what this ChildEnv carries, for reporting.
func (c *ChildEnv) Names() []string {
	out := make([]string, 0, len(c.pairs))
	for _, p := range c.pairs {
		name, _, _ := strings.Cut(p, "=")
		out = append(out, name)
	}
	return out
}

// Apply builds the environment for one exec: base, then these pairs, so a pair
// here overrides a same-named value in base.
func (c *ChildEnv) Apply(base []string) []string {
	if len(c.pairs) == 0 {
		return nil
	}
	out := make([]string, 0, len(base)+len(c.pairs))
	out = append(out, base...)
	return append(out, c.pairs...)
}

func ParseEnvFile(path string) map[string]string {
	out := map[string]string{}
	if path == "" {
		return out
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

func WriteBrokerEnv(dir string, lookup func(string) string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "broker.env")
	var b strings.Builder
	for _, name := range provider.KeyVars() {
		if v := strings.TrimSpace(lookup(name)); v != "" {
			b.WriteString(name + "=" + v + "\n")
		}
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("no provider key in host env")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func ProviderLookup(envFile string) func(string) string {
	return ProviderLookupWith(envFile, &secretref.Resolver{
		Getenv: os.Getenv,
		Announce: func(scheme string) {
			ui.Hostf("resolving a %s: secret reference on the host — approve the prompt if one appears", scheme)
		},
	}, ui.Warnf)
}

func ProviderLookupWith(envFile string, r *secretref.Resolver, warn func(string, ...any)) func(string) string {
	fileVals := ParseEnvFile(envFile)
	warned := map[string]bool{}
	return func(k string) string {
		raw := strings.TrimSpace(os.Getenv(k))
		if raw == "" {
			raw = strings.TrimSpace(fileVals[k])
		}
		if raw == "" {
			return ""
		}
		ref, isRef := secretref.Parse(raw)
		if !isRef || r == nil {
			return raw
		}
		res := r.Resolve(k, ref)
		if res.Outcome == secretref.OK {
			return res.Value
		}
		if warn != nil && !warned[k] {
			warned[k] = true
			warn("%s", secretref.Advice(k, res))
		}
		return ""
	}
}
