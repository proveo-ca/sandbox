// SPEC: _spec/internal/credentials/credential-decisions.puml
package credentials

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func lookupOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// opencodeMan is the shape the def declares: its own plan credential, and no
// provider pin — it reaches its gateway OR whatever the operator holds a key for.
func opencodeMan() manifest.Manifest {
	return manifest.Manifest{
		Name: "opencode", Subscription: true,
		Env: []manifest.EnvVar{{Name: "OPENCODE_API_KEY", Secret: true}},
	}
}

func cursorMan() manifest.Manifest {
	return manifest.Manifest{
		Name: "cursor", Subscription: true, Provider: "cursor",
		Env:          []manifest.EnvVar{{Name: "CURSOR_API_KEY", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"cursor"}},
	}
}

// cecli is the def with no plan of its own: an aider fork that reads provider
// keys and nothing else.
func cecliMan() manifest.Manifest { return manifest.Manifest{Name: "cecli"} }

// A host holding the plan key AND provider keys is the ordinary shape for
// opencode, and it was the one shape the row could not be built for: the old
// gate wanted exactly one detected provider, so two keys produced no row and
// OPENCODE_API_KEY read as a variable proveo had never heard of.
func TestBothBillingClassesAreOffered(t *testing.T) {
	t.Parallel()
	got := AvailableAuthVars(opencodeMan(), lookupOf(map[string]string{
		"OPENCODE_API_KEY":  "zen",
		"ANTHROPIC_API_KEY": "sk",
		"OPENAI_API_KEY":    "oa",
	}))
	want := []string{AuthUsage, AuthSubscription}
	if !slices.Equal(got, want) {
		t.Errorf("available = %v, want %v (riskier first)", got, want)
	}
}

// The row is about the CLASS, so however many keys back a side it stays one
// option: the keys over there are plural on purpose — the role bridges point
// different model slots at different vendors.
func TestClassesDoNotMultiplyWithKeys(t *testing.T) {
	t.Parallel()
	got := AvailableAuthVars(opencodeMan(), lookupOf(map[string]string{
		"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk",
		"OPENAI_API_KEY": "oa", "GROQ_API_KEY": "gq", "XAI_API_KEY": "xa",
	}))
	if len(got) != 2 {
		t.Errorf("available = %v, want exactly the two classes", got)
	}
}

// Only what is actually held is offered. A side with nothing behind it is not a
// choice the operator can make, and internal/run gates it with the reason.
func TestOnlyHeldClassesAreAvailable(t *testing.T) {
	t.Parallel()
	man := opencodeMan()
	if got := AvailableAuthVars(man, lookupOf(map[string]string{"ANTHROPIC_API_KEY": "sk"})); !slices.Equal(got, []string{AuthUsage}) {
		t.Errorf("with only a provider key, available = %v, want %v", got, []string{AuthUsage})
	}
	// The gateway key alone offers BOTH: opencode-go/<m> spends the Go plan,
	// opencode/<m> spends the Zen balance, and one OPENCODE_API_KEY buys either.
	if got := AvailableAuthVars(man, lookupOf(map[string]string{"OPENCODE_API_KEY": "zen"})); !slices.Equal(got, []string{AuthUsage, AuthSubscription}) {
		t.Errorf("with only the gateway key, available = %v, want both sides", got)
	}
	if got := AvailableAuthVars(man, lookupOf(nil)); len(got) != 0 {
		t.Errorf("with nothing set, available = %v, want none", got)
	}
}

// The claudecode row was two variable names, which made it a spelling question:
// the operator had to already know which of them is the plan.
func TestSameProviderAlternativesBecomeClasses(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name: "claudecode", Subscription: true,
		Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}},
	}
	lookup := lookupOf(map[string]string{"ANTHROPIC_API_KEY": "sk", "CLAUDE_CODE_OAUTH_TOKEN": "oauth"})
	if got := AvailableAuthVars(man, lookup); !slices.Equal(got, []string{AuthUsage, AuthSubscription}) {
		t.Errorf("available = %v, want both classes", got)
	}
	// And each still resolves to the variable the broker has to prefer.
	if v := EffectiveAuthVar(man, "claudecode", AuthSubscription, "", lookup); v != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("subscription resolved to %q, want CLAUDE_CODE_OAUTH_TOKEN", v)
	}
}

// cursor's CLI has no bring-your-own-key path at all, so the manifest's
// single-vendor providers list filters the operator's keys out.
// "Usage credits" means two different things and cursor separates them. It has
// no BRING-YOUR-OWN-KEY path — an ANTHROPIC_API_KEY authenticates nothing there
// — but it does bill metered: Cursor spends the plan's included usage first and
// usage-based overage after, on one CURSOR_API_KEY. So both sides are offered,
// and VendorPinnedWhy stays the explanation for the BYOK half only.
func TestVendorPinnedHarnessStillHasAMeteredSide(t *testing.T) {
	t.Parallel()
	man := cursorMan()
	got := AvailableAuthVars(man, lookupOf(map[string]string{
		"CURSOR_API_KEY": "cur", "ANTHROPIC_API_KEY": "sk",
	}))
	if !slices.Equal(got, []string{AuthUsage, AuthSubscription}) {
		t.Errorf("available = %v, want both sides — the plan, then overage", got)
	}
	// The operator's own key is still not a thing cursor can send.
	if keys := ProviderKeyVars(man, lookupOf(map[string]string{"ANTHROPIC_API_KEY": "sk"})); len(keys) != 0 {
		t.Errorf("cursor was offered BYOK keys it cannot send: %v", keys)
	}
	if why := VendorPinnedWhy(man); why == "" || !strings.Contains(why, "cursor") {
		t.Errorf("VendorPinnedWhy = %q, want a reason naming the vendor", why)
	}
	if why := VendorPinnedWhy(opencodeMan()); why != "" {
		t.Errorf("opencode reported as vendor-pinned: %q", why)
	}
}

// cecli has no plan side, so there is no question to put to the operator.
func TestAHarnessWithNoPlanAsksNothing(t *testing.T) {
	t.Parallel()
	if DeclaresSubscription(cecliMan()) {
		t.Error("cecli reported as having a plan of its own")
	}
	for _, m := range []manifest.Manifest{opencodeMan(), cursorMan()} {
		if !DeclaresSubscription(m) {
			t.Errorf("%s reported as having no plan", m.Name)
		}
	}
}

// The choice has to BITE, or it is decoration. Naming the plan withholds the
// operator's keys; naming usage withholds the plan credential.
func TestNamingOneSideWithholdsTheOther(t *testing.T) {
	t.Parallel()
	man := opencodeMan()
	lookup := lookupOf(map[string]string{"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk"})

	sub := AuthSuppressor(man, "opencode", AuthSubscription, "", lookup)
	if sub("OPENCODE_API_KEY") {
		t.Error("suppressed the credential the operator chose")
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GROQ_API_KEY"} {
		if !sub(k) {
			t.Errorf("%s survived the plan the operator chose", k)
		}
	}

	usage := AuthSuppressor(man, "opencode", AuthUsage, "", lookup)
	if !usage("OPENCODE_API_KEY") {
		t.Error("the plan credential survived the operator choosing usage credits")
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if usage(k) {
			t.Errorf("%s was withheld on the side that chose it", k)
		}
	}
}

// A vendor-pinned harness has no far side, so choosing its plan must not start
// suppressing keys it was never going to read anyway.
func TestVendorPinnedChoiceSuppressesNothingExtra(t *testing.T) {
	t.Parallel()
	s := AuthSuppressor(cursorMan(), "cursor", AuthSubscription, "",
		lookupOf(map[string]string{"CURSOR_API_KEY": "cur", "ANTHROPIC_API_KEY": "sk"}))
	if s("ANTHROPIC_API_KEY") {
		t.Error("a cursor run withheld a key it was never going to read anyway")
	}
}

// Usage credits name no single variable — several keys back it at once — so the
// broker's preferred-variable hint has to get "" rather than the plan credential
// the operator just declined.
func TestUsageResolvesToNoVariable(t *testing.T) {
	t.Parallel()
	lookup := lookupOf(map[string]string{"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk"})
	if v := EffectiveAuthVar(opencodeMan(), "opencode", AuthUsage, "", lookup); v != "" {
		t.Errorf("EffectiveAuthVar = %q, want empty", v)
	}
	for _, s := range []string{AuthUsage, AuthSubscription, AuthLocal, AuthVarLogin} {
		if !IsAuthSentinel(s) {
			t.Errorf("%q is not recognised as a credential class", s)
		}
	}
	if IsAuthSentinel("OPENCODE_API_KEY") {
		t.Error("a real variable was classified as a credential class")
	}
}

// A remembered answer written before this row had classes holds a variable name,
// and it has to keep meaning what it meant.
func TestAVariableNameFromAnOlderCacheStillResolves(t *testing.T) {
	t.Parallel()
	man := opencodeMan()
	lookup := lookupOf(map[string]string{"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk"})
	if v := EffectiveAuthVar(man, "opencode", "ANTHROPIC_API_KEY", "", lookup); v != "ANTHROPIC_API_KEY" {
		t.Errorf("EffectiveAuthVar = %q, want the cached variable name", v)
	}
}

// The hint has to name what the option is MADE of, and where it came from: a
// key that lives only in the project .env is not "host env", and saying so sends
// the operator looking in the wrong file.
func TestBackingNamesTheVariablesAndTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=sk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := AuthBacking(opencodeMan(), lookupOf(map[string]string{
		"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk",
	}), "opencode", "", envFile)

	if !strings.Contains(b[AuthUsage], "ANTHROPIC_API_KEY") {
		t.Errorf("usage backing = %q, want the key named", b[AuthUsage])
	}
	if !strings.Contains(b[AuthUsage], envFile) {
		t.Errorf("usage backing = %q, want the .env it actually lives in", b[AuthUsage])
	}
	if !strings.Contains(b[AuthSubscription], "OPENCODE_API_KEY") {
		t.Errorf("subscription backing = %q, want the plan key named", b[AuthSubscription])
	}
	if strings.Contains(b[AuthSubscription], envFile) {
		t.Errorf("subscription backing = %q, but that key is not in the file", b[AuthSubscription])
	}
}

// A login on disk IS the subscription, so the hint has to name the file — that
// is the credential the run will spend, and it is nowhere in the environment.
func TestBackingNamesTheLoginFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	path := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"live"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	man := manifest.Manifest{
		Name: "claudecode", Subscription: true,
		Env: []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
	}
	b := AuthBacking(man, lookupOf(nil), "claudecode", home, "")
	if !strings.Contains(b[AuthSubscription], path) {
		t.Errorf("subscription backing = %q, want the login file named", b[AuthSubscription])
	}
	if got := AvailableAuthVarsIn(man, lookupOf(nil), "claudecode", home); !slices.Equal(got, []string{AuthSubscription}) {
		t.Errorf("available = %v, want the plan (a login is one)", got)
	}
}

// Every gated option needs a reason, or the row is a dead end.
func TestEveryClassCanSayWhyItIsUnavailable(t *testing.T) {
	t.Parallel()
	why := AuthWhyUnavailable(cursorMan(), "cursor", "")
	for _, opt := range []string{AuthUsage, AuthSubscription, AuthLocal} {
		if strings.TrimSpace(why[opt]) == "" {
			t.Errorf("%q gates with no reason", opt)
		}
	}
	if !strings.Contains(why[AuthUsage], "cursor") {
		t.Errorf("cursor's usage reason = %q, want the vendor named", why[AuthUsage])
	}
	if !strings.Contains(why[AuthLocal], "coming soon") {
		t.Errorf("local reason = %q, want it marked coming soon", why[AuthLocal])
	}
	if oc := AuthWhyUnavailable(cecliMan(), "cecli", "")[AuthSubscription]; !strings.Contains(oc, "provider keys only") {
		t.Errorf("cecli's subscription reason = %q, want it to say there is no plan", oc)
	}
}

// HasUsableAuth is the guard on every "no credential" path, and the whole point
// is that it answers for the RUN rather than for the vendor's variable.
func TestHasUsableAuthCountsProviderKeys(t *testing.T) {
	t.Parallel()
	man := opencodeMan()
	if !HasUsableAuth(man, "opencode", "", lookupOf(map[string]string{"ANTHROPIC_API_KEY": "sk"})) {
		t.Error("an opencode run backed by ANTHROPIC_API_KEY was reported as having no credential")
	}
	if HasUsableAuth(man, "opencode", "", lookupOf(nil)) {
		t.Error("an empty host was reported as having a credential")
	}
	// cursor cannot use it, so for cursor it is not a credential.
	if HasUsableAuth(cursorMan(), "cursor", "", lookupOf(map[string]string{"ANTHROPIC_API_KEY": "sk"})) {
		t.Error("a cursor run counted a key its CLI has no way to send")
	}
}

// The sandbox refusal used to print claudecode's instructions whatever harness
// hit it — a cursor run with no key was told to run `claude setup-token`.
func TestSandboxRefusalSpeaksForTheHarnessItRefused(t *testing.T) {
	t.Parallel()
	cursor := SandboxAuthRefusal(cursorMan(), "cursor", "", lookupOf(nil))
	switch {
	case cursor == "":
		t.Fatal("a cursor run with no credential at all was not refused")
	case strings.Contains(cursor, "claude setup-token"), strings.Contains(cursor, "CLAUDE_CODE_OAUTH_TOKEN"):
		t.Errorf("cursor was handed claudecode's instructions:\n%s", cursor)
	case !strings.Contains(cursor, "CURSOR_API_KEY"):
		t.Errorf("the refusal never names the credential to obtain:\n%s", cursor)
	}
	// Vendor-pinned: offering "export a provider key instead" would be a lie.
	if strings.Contains(cursor, "ANTHROPIC_API_KEY") {
		t.Errorf("cursor was offered a BYOK path its CLI does not have:\n%s", cursor)
	}

	oc := SandboxAuthRefusal(opencodeMan(), "opencode", "", lookupOf(nil))
	if !strings.Contains(oc, "OPENCODE_API_KEY") || !strings.Contains(oc, "ANTHROPIC_API_KEY") {
		t.Errorf("opencode's refusal names neither side of its choice:\n%s", oc)
	}

	// And it does not fire at all while something can authenticate.
	if why := SandboxAuthRefusal(opencodeMan(), "opencode", "",
		lookupOf(map[string]string{"ANTHROPIC_API_KEY": "sk"})); why != "" {
		t.Errorf("refused a run that had a usable credential:\n%s", why)
	}
}

// The same question, asked by the sbx login hint: a provider key the harness
// can use means the sandbox is not credential-less.
func TestSandboxLoginHintYieldsToAProviderKey(t *testing.T) {
	t.Parallel()
	man := opencodeMan()
	if NeedsSandboxLogin(man, true, false, nil, lookupOf(map[string]string{"ANTHROPIC_API_KEY": "sk"})) {
		t.Error("told an opencode run to log in while it held a key it can use")
	}
	if !NeedsSandboxLogin(man, true, false, nil, lookupOf(nil)) {
		t.Error("a genuinely credential-less sandbox run was not flagged")
	}
}

// One harness must never be offered another's plan credential as if it were
// spendable. An opencode row listing CURSOR_API_KEY is what made this visible:
// the key is real, it is set, and opencode's gateway will refuse it — cursor's
// endpoint answers cursor-agent and nothing else, and CLAUDE_CODE_OAUTH_TOKEN
// authenticates Claude Code and nothing else.
// SPEC: _spec/internal/provider/provider-registry.puml
func TestAnotherHarnessPlanCredentialIsNotAProviderKey(t *testing.T) {
	t.Parallel()
	held := lookupOf(map[string]string{
		"ANTHROPIC_API_KEY": "sk", "OPENAI_API_KEY": "oa",
		"CURSOR_API_KEY": "cur", "CLAUDE_CODE_OAUTH_TOKEN": "oauth",
		"OPENCODE_API_KEY": "zen",
	})
	got := ProviderKeyVars(opencodeMan(), held)
	for _, banned := range []string{"CURSOR_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "OPENCODE_API_KEY"} {
		if slices.Contains(got, banned) {
			t.Errorf("opencode was offered %s as a key it could spend: %v", banned, got)
		}
	}
	for _, want := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is a general provider key and was dropped: %v", want, got)
		}
	}
	// Its own plan credential is still ITS plan, not a provider key.
	if v := ProviderKeyVars(cursorMan(), held); slices.Contains(v, "CURSOR_API_KEY") {
		t.Errorf("cursor's own plan key was classed as usage credits: %v", v)
	}
}

// A stored or mounted credential OUTRANKS an ambient .env value, and the
// suppressor is only half of enforcing that: the caller must consult it
// everywhere a variable can be set, not just where the manifest declares one.
//
// run.go had two loops. The first, over the manifest's declared env, honoured
// the suppressor. The second, over provider.KeyVars(), did not — and its
// "already added?" guard only skipped names the FIRST loop had ACCEPTED, so a
// variable the first loop declined fell through and got a sentinel anyway.
//
// A sentinel in that slot is not a harmless placeholder. An agent reads a SET
// variable as a chosen credential whatever it holds, so it displaces the login
// on disk — which is the misbilling this whole boundary exists to prevent:
// a subscription run authenticating as the API.
// SPEC: _spec/_paradigms/credential-boundary.puml
func TestAMountedLoginOutranksAnAmbientEnvValue(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cred := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cred, []byte(`{"claudeAiOauth":{"accessToken":"live"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	man := manifest.Manifest{
		Name: "claudecode", Subscription: true,
		Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}},
	}
	// Both of anthropic's credentials exported, as an ordinary host has them.
	lookup := lookupOf(map[string]string{
		"CLAUDE_CODE_OAUTH_TOKEN": "tok", "ANTHROPIC_API_KEY": "sk", "OPENAI_API_KEY": "oa",
	})
	suppress := AuthSuppressor(man, "claudecode", "", home, lookup)

	// The login is the credential, so BOTH anthropic variables must be withheld —
	// the declared one and the one only provider.KeyVars() knows about.
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if !suppress(k) {
			t.Errorf("%s is not withheld, so it lands beside a mounted login and displaces it", k)
		}
	}
	// And only that provider's: a login for anthropic says nothing about openai.
	if suppress("OPENAI_API_KEY") {
		t.Error("an anthropic login removed reach to a different provider")
	}
}

// The same precedence with no login on disk: nothing is withheld, because there
// is no stored credential to outrank anything.
func TestWithoutAStoredCredentialTheEnvStands(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name: "claudecode", Subscription: true,
		Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}},
	}
	lookup := lookupOf(map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "tok"})
	suppress := AuthSuppressor(man, "claudecode", "", t.TempDir(), lookup)
	if suppress("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Error("withheld the only credential the run has")
	}
}
