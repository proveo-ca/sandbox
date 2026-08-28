// SPEC: _spec/_paradigms/credential-boundary.puml
package credentials

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/ui"
)

func TestBrokerProviders(t *testing.T) {
	t.Parallel()
	cursorMan := manifest.Manifest{Provider: "cursor"}
	tests := []struct {
		name     string
		forwards bool
		man      manifest.Manifest
		detected []string
		lookup   func(string) string
		on       bool
		want     []string
	}{
		{"brokered + 1 provider + on", false, manifest.Manifest{}, []string{"anthropic"}, nil, true, []string{"anthropic"}},
		{"forwarded credentials never broker", true, manifest.Manifest{}, []string{"anthropic"}, nil, true, nil},
		// The row this feature exists for: several keys used to mean "ambiguous,
		// broker nothing", which handed every provider the sentinel. All of them
		// are now routed.
		{"two providers → both routed", false, manifest.Manifest{}, []string{"anthropic", "openai"}, nil, true, []string{"anthropic", "openai"}},
		{"roles spanning vendors → both routed", false, manifest.Manifest{}, []string{"moonshot", "xai"}, nil, true, []string{"moonshot", "xai"}},
		{"zero providers", false, manifest.Manifest{}, nil, nil, true, nil},
		{"broker disabled", false, manifest.Manifest{}, []string{"anthropic"}, nil, false, nil},
		// A vendor-locked harness stays narrow: the other keys are not inference
		// providers for it.
		{"cursor pin + multi-detect + host key", false, cursorMan, []string{"anthropic", "openai", "cursor"}, func(k string) string {
			if k == "CURSOR_API_KEY" {
				return "sk-cursor"
			}
			return ""
		}, true, []string{"cursor"}},
		{"cursor pin without key", false, cursorMan, []string{"anthropic", "openai"}, func(string) string { return "" }, true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lookup := tc.lookup
			if lookup == nil {
				lookup = func(string) string { return "" }
			}
			got := BrokerProviders(tc.forwards, tc.man, tc.detected, lookup, tc.on)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("BrokerProviders(...) mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBrokerOffReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		forwards   bool
		routed     []string
		detected   []string
		on         bool
		wantSubstr string // "" = expect no warning at all
	}{
		// Several providers is the supported shape now, so it must be SILENT — the
		// old "broker pins exactly one" warning was the symptom, not the diagnosis.
		{"two providers routed → silent", false, []string{"anthropic", "openai"}, []string{"anthropic", "openai"}, true, ""},
		{"keys present but none routable → explain", false, nil, []string{"anthropic", "openai"}, true, "anthropic, openai"},
		{"broker disabled → explain", false, nil, []string{"anthropic"}, false, "PROVEO_CREDENTIAL_BROKER"},
		{"broker armed → silent", false, []string{"anthropic"}, []string{"anthropic"}, true, ""},
		{"forwarded credentials → silent", true, nil, []string{"anthropic", "openai"}, true, ""},
		{"no keys at all → silent", false, nil, nil, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BrokerOffReason(tc.forwards, tc.routed, tc.detected, tc.on)
			if tc.wantSubstr == "" {
				if got != "" {
					t.Errorf("BrokerOffReason(...) = %q, want no warning", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("BrokerOffReason(...) = %q, want it to mention %q", got, tc.wantSubstr)
			}
			if !strings.Contains(got, entrypoint.DefaultSentinel) {
				t.Errorf("warning must name the sentinel the agent will get; got %q", got)
			}
		})
	}
}

// T2: WriteBrokerEnv writes the injected key to a 0600 file in a 0700 dir, and
// errors when no provider key is present (never writes an empty secret file).
func TestWriteBrokerEnv(t *testing.T) {
	// Isolate from the ambient environment: clear every provider key var.
	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}

	emptyLookup := func(string) string { return "" }
	if _, err := WriteBrokerEnv(filepath.Join(t.TempDir(), "inject"), emptyLookup); err == nil {
		t.Error("WriteBrokerEnv with no provider key must error, not write an empty file")
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-value")
	dir := filepath.Join(t.TempDir(), "inject")
	path, err := WriteBrokerEnv(dir, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("broker.env perm = %o, want 600", got)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("inject dir perm = %o, want 700", got)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "ANTHROPIC_API_KEY=sk-ant-test-value") {
		t.Errorf("broker.env content = %q, want the key=value line", b)
	}
}

func TestWriteBrokerEnvFromHostFile(t *testing.T) {
	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("CURSOR_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := WriteBrokerEnv(filepath.Join(t.TempDir(), "inject"), ProviderLookup(envPath))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "CURSOR_API_KEY=from-file") {
		t.Errorf("broker.env should include host-file key, got %q", b)
	}
}

func TestProviderDetectFromHostDotEnvOnly(t *testing.T) {
	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("CURSOR_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := ProviderLookup(envPath)
	detected := provider.Detect(lookup)
	if len(detected) != 1 || detected[0] != "cursor" {
		t.Fatalf("Detect(lookup) = %v, want [cursor]", detected)
	}
	if got := BrokerProviders(false, manifest.Manifest{Provider: "cursor"}, detected, lookup, true); len(got) != 1 || got[0] != "cursor" {
		t.Fatalf("BrokerProviders = %v, want [cursor]", got)
	}
}

func TestMoonshotDetectFromHostDotEnvOnly(t *testing.T) {
	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("MOONSHOT_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := ProviderLookup(envPath)
	detected := provider.Detect(lookup)
	if len(detected) != 1 || detected[0] != "moonshot" {
		t.Fatalf("Detect(lookup) = %v, want [moonshot]", detected)
	}
	if got := BrokerProviders(false, manifest.Manifest{}, detected, lookup, true); len(got) != 1 || got[0] != "moonshot" {
		t.Fatalf("BrokerProviders = %v, want [moonshot]", got)
	}
}

func TestCursorBrokerWithMultiProviderDotEnv(t *testing.T) {
	for _, k := range provider.KeyVars() {
		t.Setenv(k, "")
	}
	t.Setenv("CURSOR_API_KEY", "sk-cursor-host-only")
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("ANTHROPIC_API_KEY=sk-ant\nOPENAI_API_KEY=sk-oai\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := ProviderLookup(envPath)
	detected := provider.Detect(lookup)
	if len(detected) < 2 {
		t.Fatalf("Detect(lookup) = %v, want multiple providers", detected)
	}
	if got := BrokerProviders(false, manifest.Manifest{Provider: "cursor"}, detected, lookup, true); len(got) != 1 || got[0] != "cursor" {
		t.Fatalf("BrokerProviders = %v, want [cursor]", got)
	}
	path, err := WriteBrokerEnv(filepath.Join(t.TempDir(), "inject"), lookup)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "CURSOR_API_KEY=sk-cursor-host-only") {
		t.Errorf("broker.env = %q, want host CURSOR_API_KEY", b)
	}
}

func TestHydrateProcessEnvFromLookup(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "")
	lookup := func(string) string { return "from-file" }
	HydrateProcessEnv("CURSOR_API_KEY", lookup)
	if got := os.Getenv("CURSOR_API_KEY"); got != "from-file" {
		t.Fatalf("CURSOR_API_KEY = %q, want from-file", got)
	}
}

// The auth row exists only when the operator actually holds more than one
// credential for the provider this run will pin — otherwise there is no decision
// and the row would be inert.
func TestAvailableAuthVarsOnlyWhenThereIsAChoice(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}}}
	both := func(k string) string {
		return map[string]string{"ANTHROPIC_API_KEY": "sk", "CLAUDE_CODE_OAUTH_TOKEN": "oauth"}[k]
	}
	if got := AvailableAuthVars(man, both); len(got) != 2 {
		t.Errorf("with both credentials = %v, want two options", got)
	}
	only := func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk"}[k] }
	if got := AvailableAuthVars(man, only); len(got) != 1 {
		t.Errorf("with one credential = %v, want one (no row is rendered for <2)", got)
	}
	none := func(string) string { return "" }
	if got := AvailableAuthVars(man, none); len(got) != 0 {
		t.Errorf("with no credential = %v, want none", got)
	}
}

// The review tier's transport is a bind-mounted unix socket, which only works on a
// Linux host talking to a local daemon. Anywhere else the gate is unreachable and
// every connection is denied without a prompt, so the option must say which.

// The mounted-.env warning was dead for a release cycle: the guard returned on
// all three canonical tiers after broker/firewall/proxy were renamed. Nothing
// asserted it, which is why the rename went unnoticed.
func TestWarnMountedSecretsFiresOnTheOpenTierAndAlwaysOnSbx(t *testing.T) {
	dirWithEnv := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirWithEnv, ".env"), []byte("ANTHROPIC_API_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withKey := func(name string) string {
		if name == "ANTHROPIC_API_KEY" {
			return "sk-test"
		}
		return ""
	}
	noKey := func(string) string { return "" }

	cases := []struct {
		name, dir, mode string
		sandboxed       bool
		lookup          func(string) string
		wantWarning     bool
	}{
		{"open tier warns — the plain bridge has no DLP", dirWithEnv, "open", false, withKey, true},
		{"allowlist stays silent — proveo masks .env* there", dirWithEnv, "allowlist", false, withKey, false},
		{"review stays silent — same topology as allowlist", dirWithEnv, "review", false, withKey, false},
		{"mode is matched case-insensitively", dirWithEnv, "OPEN", false, withKey, true},
		{"no .env in the mounted tree", t.TempDir(), "open", false, withKey, false},
		{"no provider key on the host", dirWithEnv, "open", false, noKey, false},
		{"no mounted dir at all", "", "open", false, withKey, false},

		// The inversion this parameter exists for: sbx masks nothing, and the
		// in-container prelude sources the .env with `set -a`, so the tier that
		// silences docker must NOT silence the backend that actually reads the file.
		{"sbx warns on allowlist — nothing is masked there", dirWithEnv, "allowlist", true, withKey, true},
		{"sbx warns on review too", dirWithEnv, "review", true, withKey, true},
		{"sbx warns on open", dirWithEnv, "open", true, withKey, true},
		{"sbx still needs a key to be at risk", dirWithEnv, "allowlist", true, noKey, false},
		{"sbx still needs a .env", t.TempDir(), "allowlist", true, withKey, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := ui.Default
			ui.Default = ui.New(&buf)
			t.Cleanup(func() { ui.Default = restore })

			WarnMountedSecrets(tc.dir, tc.mode, tc.sandboxed, tc.lookup)

			got := strings.Contains(buf.String(), ".env is mounted") ||
				strings.Contains(buf.String(), ".env is inside the sandbox workspace")
			if got != tc.wantWarning {
				t.Errorf("WarnMountedSecrets(%q, %q, lookup) warned = %v, want %v (output %q)",
					tc.dir, tc.mode, got, tc.wantWarning, buf.String())
			}
		})
	}
}

// MissingEnv reads env vars only, so a completed login sitting in the proveo home
// read as "no auth" — and the refusal built on it would have blocked working runs.
func TestHasPersistedLoginSeesTheCredentialFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if HasPersistedLogin("claudecode", home) {
		t.Error("an empty home has no login")
	}
	if HasPersistedLogin("claudecode", "") {
		t.Error("no home root means no login")
	}

	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(cred, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if HasPersistedLogin("claudecode", home) {
		t.Error("an empty credential file is not a login")
	}
	if err := os.WriteFile(cred, []byte(`{"x":{"accessToken":"y"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasPersistedLogin("claudecode", home) {
		t.Error("a populated credential file is a login")
	}
	if HasPersistedLogin("opencode", home) {
		t.Error("a target with no known login file must not borrow another's")
	}
	// The macOS shape must not read as a login. It is the ordinary state of the
	// proveo home on this host, so getting it wrong is not an edge case: the run
	// announces itself authenticated and then has nothing to send.
	blanked := `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,` +
		`"refreshTokenExpiresAt":4102444800000}}`
	if err := os.WriteFile(cred, []byte(blanked), 0o600); err != nil {
		t.Fatal(err)
	}
	if HasPersistedLogin("claudecode", home) {
		t.Error("a credential file with blanked tokens is not a login, however live its stamps")
	}
}

// A login that cannot authenticate must not suppress the env token that can. The
// stamps on the macOS-blanked file are live, so the suppressor used to read it as
// the run's credential and drop CLAUDE_CODE_OAUTH_TOKEN — which is how a run
// reached the agent with every credential slot empty.

// A login that cannot authenticate must not suppress the env token that can. The
// stamps on the macOS-blanked file are live, so the suppressor used to read it as
// the run's credential and drop CLAUDE_CODE_OAUTH_TOKEN — which is how a run
// reached the agent with every credential slot empty.
func TestAuthSuppressorKeepsTheTokenWhenTheLoginIsBlanked(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "claudecode",
		Subscription: true,
		Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(dir, ".credentials.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(cred, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	live := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"real","expiresAt":%d}}`,
		time.Now().Add(8*time.Hour).UnixMilli())
	write(live)
	if !AuthSuppressor(man, "claudecode", "", home)("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Error("a login that CAN authenticate is the credential; the env token must be suppressed")
	}

	write(`{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,` +
		`"refreshTokenExpiresAt":4102444800000}}`)
	if AuthSuppressor(man, "claudecode", "", home)("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Error("a blanked login is not the credential; suppressing the env token leaves the run with none")
	}
}

// The picker and the backend selection must agree about PROVEO_SBX. They did not:
// sbx.Available() only reports whether the host CAN run sbx, so with the backend
// switched off the add-on stayed selectable and default-ticked while the run took
// docker — the prompt described a posture the run did not have.

// Anthropic can authenticate two ways. Handing sbx both put an API key and a
// subscription token in the same store, its proxy injected the key, and a
// subscription run billed per token — the auth row the operator answered was
// overridden somewhere they could not see.
func TestOnlyTheChosenAuthVarIsStored(t *testing.T) {
	t.Parallel()
	const oauth, apikey = "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"

	if !LosesToChosenAuth(apikey, oauth) {
		t.Error("the API key must lose when the operator chose the subscription token")
	}
	if !LosesToChosenAuth(oauth, apikey) {
		t.Error("and the reverse: the token must lose when the operator chose the key")
	}
	if LosesToChosenAuth(oauth, oauth) {
		t.Error("the chosen var must never be dropped")
	}
	// Only same-provider vars compete: an anthropic choice says nothing about
	// openai, and dropping an unrelated key removes reach the harness has.
	if LosesToChosenAuth("OPENAI_API_KEY", oauth) {
		t.Error("a different provider's key must survive an anthropic choice")
	}
	// No choice made: change nothing.
	if LosesToChosenAuth(apikey, "") {
		t.Error("without a chosen auth var nothing may be dropped")
	}
}

// An operator may log in on the HOST before launching; that credential reaches the
// container because HOME points at the mounted proveo home. When it is there it IS
// the answer, and proveo must not also hand sbx an API key whose proxy injection
// would override it — which is how a subscription run silently billed per token.

// An operator may log in on the HOST before launching; that credential reaches the
// container because HOME points at the mounted proveo home. When it is there it IS
// the answer, and proveo must not also hand sbx an API key whose proxy injection
// would override it — which is how a subscription run silently billed per token.
func TestHostLoginCountsAsTheChosenAuth(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{Name: "claudecode", Subscription: true, Env: []manifest.EnvVar{
		{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true},
	}}
	home := t.TempDir()

	// No explicit choice and no host login: nothing is implied, nothing is dropped.
	if got := EffectiveAuthVar(man, "claudecode", "", home); got != "" {
		t.Errorf("without a login or a choice the auth var is unknown, got %q", got)
	}

	// A host login stands in for the answer the operator never had to give.
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := EffectiveAuthVar(man, "claudecode", "", home); got != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("a persisted host login must select the harness credential, got %q", got)
	}
	if !LosesToChosenAuth("ANTHROPIC_API_KEY", EffectiveAuthVar(man, "claudecode", "", home)) {
		t.Error("with a host login present the competing API key must not be stored")
	}

	// An explicit answer always wins over the inferred one.
	if got := EffectiveAuthVar(man, "claudecode", "ANTHROPIC_API_KEY", home); got != "ANTHROPIC_API_KEY" {
		t.Errorf("the operator's own choice must win, got %q", got)
	}
}

// sandboxSpec must stay pure: a host login decides which credential the run uses,
// but that fact arrives through the input, never by reaching for the real
// filesystem. It read proveohome.Root() briefly and every result then depended on
// whether the developer happened to be logged in.

// A credential file under the mounted proveo home IS the login. Injecting the
// harness's own auth var alongside it does not add a second credential — it
// overrides the first, which is how a subscription run authenticated as the API.
func TestFileBackedLoginSuppressesEveryAuthVarForItsProvider(t *testing.T) {
	home := t.TempDir()
	cred := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cred, []byte(`{"oauth":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	man := manifest.Manifest{Env: []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}}}
	suppressed := AuthSuppressor(man, "claudecode", "", home)

	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if !suppressed(k) {
			t.Errorf("%s injected over a mounted login; it would override the subscription", k)
		}
	}
	if suppressed("OPENAI_API_KEY") {
		t.Error("an anthropic login must say nothing about another provider's reach")
	}
}

// An answered auth row is the operator's decision and still wins.

// An answered auth row is the operator's decision and still wins.
func TestChosenAuthVarSurvivesAPersistedLogin(t *testing.T) {
	home := t.TempDir()
	cred := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cred, []byte(`{"oauth":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	man := manifest.Manifest{Env: []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}}}
	suppressed := AuthSuppressor(man, "claudecode", "ANTHROPIC_API_KEY", home)

	if suppressed("ANTHROPIC_API_KEY") {
		t.Error("the operator's answer was dropped")
	}
	if !suppressed("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Error("the alternative to the answer must not be injected too")
	}
}

// With no login on disk nothing is suppressed: the env vars are the only auth.

// With no login on disk nothing is suppressed: the env vars are the only auth.
func TestNoPersistedLoginInjectsTheManifestSecret(t *testing.T) {
	man := manifest.Manifest{Env: []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}}}
	if AuthSuppressor(man, "claudecode", "", t.TempDir())("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatal("dropped the only credential the run had")
	}
}

// The sbx backend sets NEITHER home variable, and that is the fix rather than an
// omission: sbx runs its own agent user, mounts the session volumes under that
// user's home, and has its credential proxy write .credentials.json there.
// Redirecting HOME to the mounted host path orphaned all three — the agent read a
// stale mounted credential instead of the live proxy-managed one and reported
// "Not logged in".
//
// A stale value inherited from the environment must still be stripped: leaving one
// in place is the same orphaning by a different route.

// Interactive runs take no tail, so the transcript is the only record proveo can
// name. It must name THIS run's — an older one sends the reader to stale evidence.
func TestAgentTranscriptNamesOnlyThisRunsFile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "-w-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "old.jsonl")
	fresh := filepath.Join(dir, "new.jsonl")
	for _, f := range []string{stale, fresh} {
		if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	old := started.Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	later := started.Add(time.Second)
	if err := os.Chtimes(fresh, later, later); err != nil {
		t.Fatal(err)
	}

	if got := AgentTranscript("claudecode", home, started, time.Time{}); got != fresh {
		t.Fatalf("want this run's transcript %q, got %q", fresh, got)
	}
	// Nothing written this run, and nothing to point at.
	if got := AgentTranscript("claudecode", home, later.Add(time.Minute), time.Time{}); got != "" {
		t.Fatalf("named a transcript no run wrote: %q", got)
	}
	// A harness whose transcript location we have not established stays silent
	// rather than guessing a path that will read as "no evidence" forever.
	if got := AgentTranscript("cursor", home, old, time.Time{}); got != "" {
		t.Fatalf("guessed a location for an unmapped harness: %q", got)
	}
}

// The evidence channel manufactured its own evidence, and this is the case that
// cost a diagnosis.
//
// On a failed run proveo copies state out of the sandbox before looking for a
// transcript, and on a STOPPED sandbox `sbx exec` restarts the VM to do it — which
// re-runs the seed, which writes files. The run that exposed this was handed
// "66523790-…jsonl": zero bytes, created 17s AFTER the agent had died, belonging to
// no session that ever ran. It won because it was the newest .jsonl in the home,
// and because it existed at all it set `said`, which suppressed the credential hint
// written for precisely the failure that leaves nothing behind.
//
// So the window is closed at both ends and empty files are not evidence.

// The evidence channel manufactured its own evidence, and this is the case that
// cost a diagnosis.
//
// On a failed run proveo copies state out of the sandbox before looking for a
// transcript, and on a STOPPED sandbox `sbx exec` restarts the VM to do it — which
// re-runs the seed, which writes files. The run that exposed this was handed
// "66523790-…jsonl": zero bytes, created 17s AFTER the agent had died, belonging to
// no session that ever ran. It won because it was the newest .jsonl in the home,
// and because it existed at all it set `said`, which suppressed the credential hint
// written for precisely the failure that leaves nothing behind.
//
// So the window is closed at both ends and empty files are not evidence.
func TestAgentTranscriptRejectsTheHarvestsOwnArtifacts(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "-w-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string, at time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}

	started := time.Now().Add(-time.Minute)
	ended := started.Add(30 * time.Second)

	// What the agent said, during the run.
	real := write("real.jsonl", "{\"type\":\"user\"}\n", started.Add(10*time.Second))
	// The restart's leftovers: newer than everything, and empty.
	harvest := write("harvest.jsonl", "", ended.Add(17*time.Second))
	if got := AgentTranscript("claudecode", home, started, ended); got != real {
		t.Fatalf("want the run's own transcript %q, got %q", real, got)
	}

	// Non-empty but still after the run ended — a restarted session that opened a
	// transcript and wrote to it is still not this run's.
	write("harvest.jsonl", "{\"type\":\"user\"}\n", ended.Add(17*time.Second))
	if got := AgentTranscript("claudecode", home, started, ended); got != real {
		t.Fatalf("a transcript written after the run ended was named as the run's: %q", got)
	}
	_ = harvest

	// The failure this all exists for: the agent died before its first turn, so the
	// only .jsonl in the home is the harvest's empty one. The answer must be "no
	// transcript", because that emptiness is what releases the credential hint.
	if err := os.Remove(real); err != nil {
		t.Fatal(err)
	}
	write("harvest.jsonl", "", ended.Add(17*time.Second))
	if got := AgentTranscript("claudecode", home, started, ended); got != "" {
		t.Fatalf("an empty file created by the harvest was named as evidence: %q", got)
	}

	// A zero-byte transcript written DURING the run is equally no evidence: the
	// agent opened a session and said nothing, which is the case the hint answers.
	write("opened.jsonl", "", started.Add(5*time.Second))
	if got := AgentTranscript("claudecode", home, started, ended); got != "" {
		t.Fatalf("an empty transcript is not something the agent said: %q", got)
	}

	// An unbounded search still works — the success path has no restart to fence off.
	full := write("late.jsonl", "{\"type\":\"user\"}\n", ended.Add(time.Second))
	if got := AgentTranscript("claudecode", home, started, time.Time{}); got != full {
		t.Fatalf("a zero upper bound must mean unbounded, got %q", got)
	}
}

// Without --shell the harness's own sbx agent runs; the two must not be confused,
// because naming the wrong one is what skips the binding gate and drops the session.

// The persisted login must be NAMEABLE in the auth row. Until it was, the row listed
// only environment variables, so a remembered answer naming one of them outranked a
// login the operator established later — proveo forwarded a token the API refused
// while a working subscription sat mounted and unread.
func TestAuthRowOffersThePersistedLoginFirst(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cred := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cred, []byte(`{"claudeAiOauth":{"accessToken":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	man := manifest.Manifest{
		Name: "claudecode", Provider: "anthropic",
		Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}},
	}
	lookup := func(k string) string {
		return map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "tok", "ANTHROPIC_API_KEY": "key"}[k]
	}

	got := AvailableAuthVarsIn(man, lookup, "claudecode", home)
	if len(got) == 0 || got[0] != AuthVarLogin {
		t.Fatalf("the login must be offered first, got %v", got)
	}
	// Without one on disk the row is unchanged: nothing to name.
	if bare := AvailableAuthVarsIn(man, lookup, "claudecode", t.TempDir()); slices.Contains(bare, AuthVarLogin) {
		t.Errorf("offered a login that does not exist: %v", bare)
	}

	// Naming it suppresses that provider's variables, and only that provider's.
	suppressed := AuthSuppressor(man, "claudecode", AuthVarLogin, home)
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if !suppressed(k) {
			t.Errorf("%s injected over the login the operator named", k)
		}
	}
	if suppressed("OPENAI_API_KEY") {
		t.Error("an anthropic login must not remove reach to another provider")
	}
	// It is a sentinel, never an env var name.
	if v := EffectiveAuthVar(man, "claudecode", AuthVarLogin, home); v == AuthVarLogin {
		t.Errorf("the login sentinel leaked into an env var name: %q", v)
	}
}

// Existence is not validity. A dead credential is a file of exactly the same
// size as a live one, so stat-ing it let an expired login satisfy the guard that
// exists to stop a run the agent cannot complete — it reaches the login prompt,
// exits, and the sandbox stops with it, which the operator sees as an
// infrastructure failure rather than as "your login ran out".

// Existence is not validity. A dead credential is a file of exactly the same
// size as a live one, so stat-ing it let an expired login satisfy the guard that
// exists to stop a run the agent cannot complete — it reaches the login prompt,
// exits, and the sandbox stops with it, which the operator sees as an
// infrastructure failure rather than as "your login ran out".
func TestLoginUsableSeparatesLiveFromDeadCredentials(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 25, 16, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) int64 { return now.Add(d).UnixMilli() }

	for _, tc := range []struct {
		name             string
		body             string
		usable, needsRef bool
	}{
		{
			name:   "live access token",
			body:   fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":%d,"refreshTokenExpiresAt":%d}}`, ms(8*time.Hour), ms(700*time.Hour)),
			usable: true,
		},
		{
			// The agent renews this itself, with no prompt — refusing it would
			// block a run that works.
			name:     "stale access token, live refresh token",
			body:     fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":%d,"refreshTokenExpiresAt":%d}}`, ms(-time.Hour), ms(600*time.Hour)),
			usable:   true,
			needsRef: true,
		},
		{
			name: "both expired",
			body: fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":%d,"refreshTokenExpiresAt":%d}}`, ms(-700*time.Hour), ms(-time.Hour)),
		},
		{
			// A shape we cannot parse must NOT be guessed at: a false refusal is
			// worse than the failure it was meant to prevent.
			name:   "unrecognised shape",
			body:   `{"someOtherHarness":{"token":"x"}}`,
			usable: true,
		},
		{
			name:   "no expiry recorded",
			body:   `{"claudeAiOauth":{"accessToken":"x"}}`,
			usable: true,
		},
		{
			// A CLEARED stamp is not an absent one. A failed refresh writes
			// expiresAt:0, and reading that as "no expiry recorded" reported a
			// dead credential as healthy — the exact case that produced
			// "OAuth session expired and could not be refreshed".
			name:     "cleared access stamp, live refresh token",
			body:     fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":0,"refreshTokenExpiresAt":%d}}`, ms(600*time.Hour)),
			usable:   true,
			needsRef: true,
		},
		{
			name: "cleared access stamp, dead refresh token",
			body: fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":0,"refreshTokenExpiresAt":%d}}`, ms(-time.Hour)),
		},
		{
			// The macOS shape, and the one that cost a run: `claude` in the proveo
			// home moves the credential to the Keychain and rewrites the file with
			// its tokens BLANKED, leaving every stamp untouched. Judged on stamps
			// alone this reads as a healthy login needing a refresh, so the run
			// suppressed the env token that would have worked.
			name: "blanked tokens, live refresh stamp",
			body: fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,"refreshTokenExpiresAt":%d}}`, ms(600*time.Hour)),
		},
		{
			// Blank tokens are dead even while the ACCESS stamp is still in the
			// future: there is no token to send, so a live window proves nothing.
			name: "blanked tokens, live access stamp",
			body: fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":%d,"refreshTokenExpiresAt":%d}}`, ms(8*time.Hour), ms(700*time.Hour)),
		},
		{
			// Only the RENEWAL is missing: a real access token is present and its
			// stamp is live, so the run authenticates without needing the refresh.
			name:   "live access token, blanked refresh token",
			body:   fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"real","refreshToken":"","expiresAt":%d,"refreshTokenExpiresAt":%d}}`, ms(8*time.Hour), ms(700*time.Hour)),
			usable: true,
		},
		{
			// Nothing left to renew with, so the live refresh stamp describes a
			// renewal that cannot happen — not a login proveo may announce.
			name: "stale access token, blanked refresh token",
			body: fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"real","refreshToken":"","expiresAt":%d,"refreshTokenExpiresAt":%d}}`, ms(-time.Hour), ms(600*time.Hour)),
		},
		{
			// ABSENT is not BLANK. A shape that never carried the field must keep
			// falling through to the stamps, or the conservative reading that
			// protects unknown harnesses turns into a refusal.
			name:     "tokens absent, stale access stamp",
			body:     fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":%d,"refreshTokenExpiresAt":%d}}`, ms(-time.Hour), ms(600*time.Hour)),
			usable:   true,
			needsRef: true,
		},
		{name: "empty file", body: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), ".credentials.json")
			if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			usable, needsRef := loginUsable(p, now)
			if usable != tc.usable || needsRef != tc.needsRef {
				t.Errorf("loginUsable = (%v, %v), want (%v, %v)", usable, needsRef, tc.usable, tc.needsRef)
			}
		})
	}

	if usable, _ := loginUsable(filepath.Join(t.TempDir(), "absent.json"), now); usable {
		t.Error("a missing credential file must not read as a login")
	}
}

// sbx registers its MCP gateway from its OWN agent kit, with `claude mcp add
// --scope user`, inside a HOME that proveo mounts read-write. So an entry meant
// to live and die with a disposable sandbox lands in the operator's real home.
// Declining goes through sbx's own gate rather than by patching the file it
// writes — editing the config loses a race with a step that runs every start.

// A login only outranks an env token while it can still AUTHENTICATE. Suppressing
// a working token in favour of a dead file leaves the run with NO credential — and
// on macOS that is the normal way to end up stale, because the host's login lives
// in the keychain and the file under the proveo home is written only by the
// container, which is the one place that cannot be reached to refresh it.
func TestDeadLoginDoesNotSuppressAWorkingToken(t *testing.T) {
	man := manifest.Manifest{
		Name: "claudecode", Subscription: true,
		Env: []manifest.EnvVar{{Name: "ANTHROPIC_API_KEY", Secret: true}},
	}
	write := func(t *testing.T, body string) string {
		t.Helper()
		home := t.TempDir()
		dir := filepath.Join(home, ".claude")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return home
	}
	live := time.Now().Add(8 * time.Hour).UnixMilli()
	future := time.Now().Add(700 * time.Hour).UnixMilli()

	t.Run("a live login still outranks the token", func(t *testing.T) {
		home := write(t, fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":%d,"refreshTokenExpiresAt":%d}}`, live, future))
		if !AuthSuppressor(man, "claudecode", "", home)("ANTHROPIC_API_KEY") {
			t.Error("a usable login must still suppress an env token, or a subscription run silently bills per token")
		}
	})

	t.Run("a login needing renewal does not", func(t *testing.T) {
		// expiresAt:0 is what a failed refresh leaves behind.
		home := write(t, fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":0,"refreshTokenExpiresAt":%d}}`, future))
		if AuthSuppressor(man, "claudecode", "", home)("ANTHROPIC_API_KEY") {
			t.Error("a login that cannot authenticate must not suppress the only working credential")
		}
	})

	t.Run("an explicit login answer still wins", func(t *testing.T) {
		home := write(t, `{"claudeAiOauth":{"expiresAt":0}}`)
		if !AuthSuppressor(man, "claudecode", AuthVarLogin, home)("ANTHROPIC_API_KEY") {
			t.Error("the operator naming the login outranks its freshness — their answer stands")
		}
	})
}

// Where the work lands is not a detail an operator should have to infer, so it
// gets a posture row either way.

// The package's whole reason to exist as a boundary: a variable it decides to
// SUPPRESS must never reach the run carrying a value. Every earlier test pins one
// decision; this pins the invariant across all of them, so a future call site
// cannot read the suppressor and then inject the value anyway.
//
// It sweeps the shapes that actually differ: an operator's answer, a live login on
// disk, a blanked one (the macOS keychain husk), and no login at all.
func TestASuppressedVarNeverCarriesAValue(t *testing.T) {
	t.Parallel()
	const live = `{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":32503680000000}}`
	const husk = `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,` +
		`"refreshTokenExpiresAt":32503680000000}}`

	man := manifest.Manifest{
		Name: "claudecode", Subscription: true,
		Env: []manifest.EnvVar{
			{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true},
			{Name: "ANTHROPIC_API_KEY", Secret: true},
		},
	}
	lookup := func(string) string { return "a-real-looking-value" }

	for _, tc := range []struct{ name, cred, chosen string }{
		{"no login on disk", "", ""},
		{"live login", live, ""},
		{"blanked login (keychain husk)", husk, ""},
		{"operator answered a var", live, "ANTHROPIC_API_KEY"},
		{"operator answered the login", live, AuthVarLogin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			if tc.cred != "" {
				dir := filepath.Join(home, ".claude")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(tc.cred), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			suppressed := AuthSuppressor(man, "claudecode", tc.chosen, home)

			for _, e := range man.Env {
				if !e.Secret || !suppressed(e.Name) {
					continue
				}
				// Suppressed means OMITTED, not stated as empty: an agent reads a SET
				// variable as a chosen credential whatever it holds, so a blank one
				// occupies the slot the login needed.
				for _, got := range LoadedSecretNames(man, func(k string) string {
					if suppressed(k) {
						return ""
					}
					return lookup(k)
				}) {
					if got == e.Name {
						t.Errorf("%s was suppressed and still reached the run", e.Name)
					}
				}
			}
		})
	}
}
