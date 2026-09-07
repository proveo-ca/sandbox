// SPEC: _spec/_paradigms/credential-boundary.puml
package credentials

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/proveo-ca/proveo/internal/secretref"
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

// TestChildEnvKeepsValueOutOfProveoEnviron is the (c) neighbour: the value
// reaches the launch exec and never proveo's own environment.
func TestChildEnvKeepsValueOutOfProveoEnviron(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "")
	var c ChildEnv
	c.Add("CURSOR_API_KEY", func(string) string { return "from-file" })

	if got := os.Getenv("CURSOR_API_KEY"); got != "" {
		t.Fatalf("proveo's own environ was mutated: CURSOR_API_KEY = %q", got)
	}
	if got := c.Pairs(); len(got) != 1 || got[0] != "CURSOR_API_KEY=from-file" {
		t.Fatalf("Pairs = %v", got)
	}
	if got := c.Names(); len(got) != 1 || got[0] != "CURSOR_API_KEY" {
		t.Errorf("Names = %v", got)
	}
	// The pair lands after base, so it wins over a stale same-named value.
	env := c.Apply([]string{"CURSOR_API_KEY=stale", "PATH=/bin"})
	if env[len(env)-1] != "CURSOR_API_KEY=from-file" {
		t.Errorf("Apply = %v, want the pair last", env)
	}
}

// TestChildEnvSkipsWhatTheChildAlreadyInherits: restating an exported value
// would put the secret somewhere it already was.
func TestChildEnvSkipsWhatTheChildAlreadyInherits(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "exported")
	var c ChildEnv
	c.Add("CURSOR_API_KEY", func(string) string { return "from-file" })
	c.Add("MISSING_KEY", func(string) string { return "" })
	if got := c.Pairs(); len(got) != 0 {
		t.Fatalf("Pairs = %v, want none", got)
	}
	// Nothing to add means nil, which leaves os/exec passing the environment
	// through rather than rebuilding it.
	if got := c.Apply([]string{"PATH=/bin"}); got != nil {
		t.Errorf("Apply = %v, want nil", got)
	}
}

// TestProviderLookupResolvesReferences: a stored reference resolves; an
// unresolvable one warns once and yields "", never the reference text.
func TestProviderLookupResolvesReferences(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "keychain:Some Service")
	t.Setenv("OPENAI_API_KEY", "keychain:Missing Service")
	t.Setenv("XAI_API_KEY", "sk-xai-literal:with-colon")

	var warnings []string
	r := &secretref.Resolver{
		GOOS: "darwin",
		Exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			for i, a := range args {
				if a == "-s" && i+1 < len(args) && args[i+1] == "Some Service" {
					return []byte("sk-ant-resolved\n"), nil, nil
				}
			}
			return nil, []byte("security: SecKeychainSearchCopyNext: " +
				"The specified item could not be found in the keychain."), errors.New("exit status 44")
		},
	}
	lookup := ProviderLookupWith("", r, func(f string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(f, a...))
	})

	if got := lookup("ANTHROPIC_API_KEY"); got != "sk-ant-resolved" {
		t.Errorf("resolved value = %q", got)
	}
	if got := lookup("XAI_API_KEY"); got != "sk-xai-literal:with-colon" {
		t.Errorf("literal with a colon = %q, want it untouched", got)
	}
	for range 3 {
		if got := lookup("OPENAI_API_KEY"); got != "" {
			t.Fatalf("unresolvable reference = %q, want empty", got)
		}
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], "OPENAI_API_KEY") || !strings.Contains(warnings[0], "Missing Service") {
		t.Errorf("warning = %q", warnings[0])
	}
}

// SPEC: _spec/internal/credentials/credential-decisions.puml
func TestAvailableAuthVarsOnlyWhenThereIsAChoice(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name:         "claudecode",
		Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}},
	}
	both := func(k string) string {
		return map[string]string{"ANTHROPIC_API_KEY": "sk", "CLAUDE_CODE_OAUTH_TOKEN": "oauth"}[k]
	}
	if got := AvailableAuthVars(man, both); !slices.Equal(got, []string{AuthUsage, AuthSubscription}) {
		t.Errorf("with both credentials = %v, want both classes, riskier first", got)
	}
	only := func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk"}[k] }
	if got := AvailableAuthVars(man, only); !slices.Equal(got, []string{AuthUsage}) {
		t.Errorf("with one credential = %v, want usage credits alone", got)
	}
	none := func(string) string { return "" }
	if got := AvailableAuthVars(man, none); len(got) != 0 {
		t.Errorf("with no credential = %v, want none", got)
	}
}

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
	blanked := `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,` +
		`"refreshTokenExpiresAt":4102444800000}}`
	if err := os.WriteFile(cred, []byte(blanked), 0o600); err != nil {
		t.Fatal(err)
	}
	if HasPersistedLogin("claudecode", home) {
		t.Error("a credential file with blanked tokens is not a login, however live its stamps")
	}
}

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
	if !AuthSuppressor(man, "claudecode", "", home, nil)("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Error("a login that CAN authenticate is the credential; the env token must be suppressed")
	}

	write(`{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,` +
		`"refreshTokenExpiresAt":4102444800000}}`)
	if AuthSuppressor(man, "claudecode", "", home, nil)("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Error("a blanked login is not the credential; suppressing the env token leaves the run with none")
	}
}

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

func TestHostLoginCountsAsTheChosenAuth(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{Name: "claudecode", Subscription: true, Env: []manifest.EnvVar{
		{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true},
	}}
	home := t.TempDir()

	// No explicit choice and no host login: nothing is implied, nothing is dropped.
	if got := EffectiveAuthVar(man, "claudecode", "", home, nil); got != "" {
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
	if got := EffectiveAuthVar(man, "claudecode", "", home, nil); got != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("a persisted host login must select the harness credential, got %q", got)
	}
	if !LosesToChosenAuth("ANTHROPIC_API_KEY", EffectiveAuthVar(man, "claudecode", "", home, nil)) {
		t.Error("with a host login present the competing API key must not be stored")
	}

	// An explicit answer always wins over the inferred one.
	if got := EffectiveAuthVar(man, "claudecode", "ANTHROPIC_API_KEY", home, nil); got != "ANTHROPIC_API_KEY" {
		t.Errorf("the operator's own choice must win, got %q", got)
	}
}

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
	suppressed := AuthSuppressor(man, "claudecode", "", home, nil)

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
	suppressed := AuthSuppressor(man, "claudecode", "ANTHROPIC_API_KEY", home, nil)

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
	if AuthSuppressor(man, "claudecode", "", t.TempDir(), nil)("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatal("dropped the only credential the run had")
	}
}

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
	if !slices.Contains(got, AuthSubscription) {
		t.Fatalf("a mounted login is not offered as the plan, got %v", got)
	}
	if b := AuthBacking(man, lookup, "claudecode", home, "")[AuthSubscription]; !strings.Contains(b, cred) {
		t.Errorf("the plan hint does not name the login that will be spent: %q", b)
	}
	// With no login on disk the plan side is the declared token alone.
	if b := AuthBacking(man, lookup, "claudecode", t.TempDir(), "")[AuthSubscription]; strings.Contains(b, "login ") {
		t.Errorf("named a login that does not exist: %q", b)
	}

	// Answering "subscription" with a login on disk resolves to the FILE, and
	// suppresses that provider's variables — only that provider's.
	if s := AuthSuppressor(man, "claudecode", AuthSubscription, home, lookup); !s("ANTHROPIC_API_KEY") ||
		!s("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Error("an env token was injected over the login the operator's answer selected")
	}

	// Naming it suppresses that provider's variables, and only that provider's.
	suppressed := AuthSuppressor(man, "claudecode", AuthVarLogin, home, nil)
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if !suppressed(k) {
			t.Errorf("%s injected over the login the operator named", k)
		}
	}
	if suppressed("OPENAI_API_KEY") {
		t.Error("an anthropic login must not remove reach to another provider")
	}
	// It is a sentinel, never an env var name.
	if v := EffectiveAuthVar(man, "claudecode", AuthVarLogin, home, nil); v == AuthVarLogin {
		t.Errorf("the login sentinel leaked into an env var name: %q", v)
	}
}

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
		if !AuthSuppressor(man, "claudecode", "", home, nil)("ANTHROPIC_API_KEY") {
			t.Error("a usable login must still suppress an env token, or a subscription run silently bills per token")
		}
	})

	t.Run("a login needing renewal does not", func(t *testing.T) {
		// expiresAt:0 is what a failed refresh leaves behind.
		home := write(t, fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":0,"refreshTokenExpiresAt":%d}}`, future))
		if AuthSuppressor(man, "claudecode", "", home, nil)("ANTHROPIC_API_KEY") {
			t.Error("a login that cannot authenticate must not suppress the only working credential")
		}
	})

	t.Run("an explicit login answer still wins", func(t *testing.T) {
		home := write(t, `{"claudeAiOauth":{"expiresAt":0}}`)
		if !AuthSuppressor(man, "claudecode", AuthVarLogin, home, nil)("ANTHROPIC_API_KEY") {
			t.Error("the operator naming the login outranks its freshness — their answer stands")
		}
	})
}

// Where the work lands is not a detail an operator should have to infer, so it
// gets a posture row either way.

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
			suppressed := AuthSuppressor(man, "claudecode", tc.chosen, home, nil)

			for _, e := range man.Env {
				if !e.Secret || !suppressed(e.Name) {
					continue
				}
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
