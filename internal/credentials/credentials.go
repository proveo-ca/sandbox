// SPEC: _spec/_paradigms/credential-boundary.puml, _spec/_plans/main-decomposition-moves.puml
//
// Package credentials decides what a run authenticates with, and writes nothing
// else. Every function here is pure over (manifest, lookup, homeRoot): no run
// state, no cobra, no docker — which is why it is move 2 of the decomposition and
// why the plan goldens cannot change when it lands.
//
// WriteBrokerEnv is the one writer, and keeps its 0600. WarnMountedSecrets takes
// its writer rather than reaching for a package global, so a caller's tests can
// capture what it says.
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

// AvailableAuthVars lists the credentials the operator holds for the provider
// this run will pin.
// AuthVarLogin names the credential the operator already established as a FILE in
// the proveo home. It is offered beside the environment variables because it is a
// third way to authenticate and, until it was listed, an unnameable one: the row
// showed only env vars, so a remembered answer naming one of them outranked a login
// the operator had made later — and proveo forwarded a token the API refused while
// a working subscription sat mounted and unread.
const AuthVarLogin = "login (proveo home)"

// AvailableAuthVars lists the credentials the operator holds for this harness, the
// persisted login first when there is one: it is the answer that needs no value
// exported, so it belongs where a default lands.
func AvailableAuthVars(man manifest.Manifest, lookup func(string) string) []string {
	return AvailableAuthVarsIn(man, lookup, "", "")
}

func AvailableAuthVarsIn(man manifest.Manifest, lookup func(string) string, target, homeRoot string) []string {
	out := envAuthVars(man, lookup)
	if HasPersistedLogin(target, homeRoot) {
		return append([]string{AuthVarLogin}, out...)
	}
	return out
}

func envAuthVars(man manifest.Manifest, lookup func(string) string) []string {
	detected := FilterProviders(provider.Detect(lookup), man.Capabilities)
	if len(detected) != 1 {
		if pin := strings.TrimSpace(man.Provider); pin != "" {
			detected = []string{pin}
		} else {
			return nil
		}
	}
	var out []string
	for _, v := range provider.AuthVars(detected[0]) {
		if strings.TrimSpace(lookup(v)) != "" {
			out = append(out, v)
		}
	}
	return out
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

// agentTranscriptDirs are where a harness writes its session transcripts inside the
// mounted proveo home, relative to it. Keyed by target: each CLI chooses its own.
var agentTranscriptDirs = map[string][]string{
	"claudecode": {".claude/projects"},
}

// AgentTranscript names the session transcript written during this run, if any.
//
// It is better evidence than a captured tail. A tail holds what reached the
// terminal; the transcript holds what the agent received and said — which is where
// "Credit balance is too low" appeared after a run showed nothing but a stopped
// sandbox.
//
// The window is closed at BOTH ends, and the upper bound is not defensive tidiness.
// The copy-out that brings a transcript to the host runs on the failure path, and
// on a stopped sandbox `sbx exec` restarts the VM to do it — which re-runs the
// seed. That restart writes files of its own. One run reported
// "66523790-…jsonl" as what the agent said: zero bytes, created at 15:12:13.694,
// seventeen seconds AFTER the agent it was supposed to be quoting had already died,
// belonging to no session that ever ran (the run's own session was 1a972241, and it
// has no transcript anywhere). Ranking by mtime alone, it was the newest file in
// the home and therefore won.
//
// Empty is not evidence either. A zero-byte file satisfied "a transcript exists",
// which suppressed the credential hint written for exactly the failure that leaves
// nothing behind — so the one run that most needed an explanation got a path to an
// empty file instead.
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

// subscriptionLoginFiles are where a completed login persists inside the proveo
// home the sandbox mounts. Keyed by target because each harness stores its own.
//
// An operator may authenticate on the HOST before launching — `claude setup-token`,
// or a normal interactive login — and that credential reaches the container because
// HOME points at the proveo home, which is mounted. When it is present it is the
// operator's answer, and proveo must not hand sbx a competing API key that its
// proxy would inject instead (which is how a subscription run silently billed per
// token).
//
// cursor is absent deliberately: its manifest declares only CURSOR_API_KEY, and its
// CLI keeps no credential file we have established — ~/.cursor/cli-config.json is
// configuration, not auth. Add it here once the location is known rather than
// guessing, or a missing file will read as "no login" forever.
var subscriptionLoginFiles = map[string][]string{
	"claudecode": {".claude/.credentials.json"},
}

// EffectiveAuthVar is the credential the run should authenticate with: the row the
// operator answered if there was one, otherwise the manifest's own secret when a
// host login is already sitting in the proveo home.
func EffectiveAuthVar(man manifest.Manifest, target, chosen, homeRoot string) string {
	if v := strings.TrimSpace(chosen); v != "" && v != AuthVarLogin {
		return v
	}
	if !HasPersistedLogin(target, homeRoot) {
		return ""
	}
	for _, e := range man.Env {
		if e.Secret {
			return e.Name // the harness's declared subscription credential
		}
	}
	return ""
}

// HasPersistedLogin reports whether a login already exists for target under the
// proveo home. It is the half of the auth picture MissingEnv cannot see: the env
// var is one way to be authenticated and the credential file is the other.
func HasPersistedLogin(target, homeRoot string) bool {
	ok, _ := PersistedLogin(target, homeRoot)
	return ok
}

// PersistedLogin reports whether the credential can still authenticate, and
// whether it must be refreshed first.
//
// Existence is NOT validity, and the difference is the whole point of the guard
// this feeds. A dead credential is a file of exactly the same size as a live
// one, so stat-ing it let an expired login satisfy the check that exists to stop
// a run the agent cannot complete — it reaches the login prompt, exits, and the
// sandbox stops with it, which surfaces as an infrastructure failure rather than
// as "your login ran out".
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

// oauthCredential is the shape claudecode persists. The stamps say whether the
// credential is live; the tokens are read only for EMPTINESS, never for value —
// a stamp cannot tell you the token beside it was taken away.
type oauthCredential struct {
	ClaudeAIOauth struct {
		// Pointers because ABSENT and ZERO mean opposite things here: a missing
		// field is a shape we do not understand (assume usable), while an explicit
		// 0 is a stamp that has been cleared — a token deliberately invalidated,
		// which is exactly the state a failed refresh leaves behind.
		ExpiresAt             *int64 `json:"expiresAt"`             // ms since epoch
		RefreshTokenExpiresAt *int64 `json:"refreshTokenExpiresAt"` // ms since epoch
		// Pointers for the same reason, and it is load-bearing: logging in on
		// macOS moves the credential to the KEYCHAIN and rewrites this file with
		// its tokens blanked, leaving every stamp in place. An absent field is a
		// shape we do not judge; an explicit "" is a token that was removed.
		AccessToken  *string `json:"accessToken"`
		RefreshToken *string `json:"refreshToken"`
	} `json:"claudeAiOauth"`
}

// tokenCleared reports whether a token field is present and blank — removed,
// rather than belonging to a shape this check does not recognise.
func tokenCleared(tok *string) bool { return tok != nil && *tok == "" }

// loginUsable classifies one credential file.
//
// An UNRECOGNISED file is reported usable on purpose. This check exists to catch
// a credential that is definitely dead; inferring "expired" from a shape we
// cannot parse would refuse runs that work, and a false refusal is worse than
// the failure it was meant to prevent — the operator has no way to argue with it.
func loginUsable(path string, now time.Time) (usable, needsRefresh bool) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return false, false
	}
	var c oauthCredential
	if json.Unmarshal(b, &c) != nil {
		return true, false // presence is all this file lets us honestly assert
	}
	o := &c.ClaudeAIOauth
	// A file with its tokens BLANKED is not a login, however live the stamps look.
	// This is the ordinary state of the proveo home on macOS: `claude` there writes
	// the credential to the Keychain and leaves the file with "" tokens and every
	// stamp intact. Reading the stamps alone reported that as a login needing a
	// refresh, so the run announced itself authenticated, suppressed the env token
	// that would have worked, and the agent died with nothing to send.
	if tokenCleared(o.AccessToken) {
		return false, false
	}
	if o.ExpiresAt == nil {
		return true, false
	}
	if now.Before(time.UnixMilli(*o.ExpiresAt)) {
		return true, false
	}
	// A stale access token beside a LIVE refresh token is still a login: the
	// agent renews it at startup with no prompt, which is exactly what happened
	// on the run that reported "Login expired" and then carried on working. A
	// CLEARED stamp (0) lands here too, which is correct — it needs the same
	// refresh, and saying so is what tells the operator why the agent stalled.
	//
	// A blanked refresh token is the exception: there is nothing to renew WITH, so
	// the stamp describes a renewal that cannot happen.
	if r := o.RefreshTokenExpiresAt; r != nil && !tokenCleared(o.RefreshToken) && now.Before(time.UnixMilli(*r)) {
		return true, true
	}
	return false, false
}

// AuthSuppressor reports which auth vars must NOT be injected for this run.
//
// Two ways of being authenticated compete, and they do not merge: an env token
// OVERRIDES a credential file rather than sitting beside it. So when the operator's
// login already exists as a file under the mounted proveo home, that file IS the
// credential and every auth var for its provider is suppressed — otherwise a
// setup-token exported on the host authenticates the run as the API while the
// mounted subscription login goes unread, which is what "Claude API" on a
// subscription run was reporting. When the operator answered the auth row instead,
// their answer stands and only its alternatives are suppressed.
func AuthSuppressor(man manifest.Manifest, target, chosen, homeRoot string) func(string) bool {
	chosen = strings.TrimSpace(chosen)
	// The login is the credential — either named outright, or the only answer when
	// the operator gave none and one exists. Then NO variable for its providers may
	// be injected: an env token supersedes the file rather than joining it.
	// A login only outranks an env token while it can still AUTHENTICATE. A file
	// that needs a renewal this backend cannot perform is not the credential — it
	// is a dead one, and suppressing a working token in its favour leaves the run
	// with no credential at all. That is not hypothetical: on macOS the host's
	// login lives in the KEYCHAIN, so the file under the proveo home is written
	// only by the container and can go stale with no host-side way to refresh it.
	usableLogin, staleLogin := PersistedLogin(target, homeRoot)
	usableLogin = usableLogin && !staleLogin
	if chosen == AuthVarLogin || (chosen == "" && usableLogin) {
		// Scoped to the providers the login actually authenticates — read off the
		// harness's own declared secrets. Scoping it to the manifest's capabilities
		// instead reached too far: a manifest that declares none allows every
		// provider, so an anthropic login would have suppressed the openai key too
		// and quietly removed reach the harness legitimately has.
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
	auth := EffectiveAuthVar(man, target, chosen, homeRoot)
	return func(k string) bool { return LosesToChosenAuth(k, auth) }
}

// LosesToChosenAuth reports whether key var k is a rejected alternative to the auth
// var the operator picked. Only vars of the SAME provider compete: an anthropic
// choice says nothing about openai, and dropping an unrelated key would silently
// remove reach the harness legitimately has.
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

// ProviderOfKeyVar names the provider a credential env var belongs to, or the var
// itself when the registry does not claim it — so an unknown key is judged by the
// capability list rather than silently dropped.
func ProviderOfKeyVar(envVar string) string {
	if name, _, ok := ProviderForAuthVar(envVar); ok {
		return name
	}
	return envVar
}

// ProviderForAuthVar maps a credential env var back to the provider that accepts
// it and the auth option describing how — the reverse of the registry's usual
// direction, and what lets a Kit name a service for a secret proveo only knows by
// variable name.
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

func WarnMountedSecrets(dir, mode string, lookup func(string) string) {
	if dir == "" {
		return
	}
	switch strings.ToLower(mode) {
	case "allowlist", "review":
		return
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err != nil {
		return
	}
	if len(provider.Detect(lookup)) == 0 {
		return
	}
	ui.Warnf("%s/.env is mounted and a provider key is set — the agent can read it directly; use --egress-mode firewall so egress DLP blocks the key from leaving", dir)
}

func HydrateProcessEnv(name string, lookup func(string) string) {
	if strings.TrimSpace(os.Getenv(name)) != "" {
		return
	}
	if v := strings.TrimSpace(lookup(name)); v != "" {
		_ = os.Setenv(name, v)
	}
}

// ParseEnvFile reads a KEY=VALUE env file (project .env shape). Missing => empty.
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

// WriteBrokerEnv writes present provider keys to a 0600 file the egress proxy
// mounts. lookup may include host-side .env values not in the process env.
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

// ProviderLookup prefers the process env, then a host-side KEY=VALUE file
// (project .env / PROVEO_EGRESS_ENV_FILE) for detection and broker.env writing.
func ProviderLookup(envFile string) func(string) string {
	fileVals := ParseEnvFile(envFile)
	return func(k string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return fileVals[k]
	}
}
