// SPEC: _spec/internal/secretref/secret-references.puml
package credentials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/secretref"
)

// envOf builds a LookupEnv over a map, distinguishing ABSENT from set-and-empty.
func envOf(m map[string]string) LookupEnv {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestKeychainAccount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "the ordinary case", env: map[string]string{"USER": "pluvo"}, want: "pluvo"},
		{name: "dots and dashes are legal", env: map[string]string{"USER": "first.last-1"}, want: "first.last-1"},
		{name: "a space forces the fallback", env: map[string]string{"USER": "ada lovelace"}, want: keychainAccountFallback},
		{name: "a slash forces the fallback", env: map[string]string{"USER": "domain\\user"}, want: keychainAccountFallback},
		{name: "absent forces the fallback", env: map[string]string{}, want: keychainAccountFallback},
		{name: "empty forces the fallback", env: map[string]string{"USER": ""}, want: keychainAccountFallback},
	}
	for _, tc := range tests {
		if got := KeychainAccount(envOf(tc.env)); got != tc.want {
			t.Errorf("%s: KeychainAccount = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestKeychainServicesMeasuredDefault pins the name measured on a default
// install: svce="Claude Code-credentials", no discriminator.
func TestKeychainServicesMeasuredDefault(t *testing.T) {
	t.Parallel()
	got := KeychainServices(envOf(map[string]string{"USER": "pluvo"}))
	if len(got) == 0 || got[0] != "Claude Code-credentials" {
		t.Fatalf("services = %v, want the measured name first", got)
	}
	// claude-code < 2.x stored under a bare "Claude Code" (#1470).
	if got[len(got)-1] != "Claude Code" {
		t.Errorf("services = %v, want the legacy name last", got)
	}
}

// TestKeychainServicesConfigDirDiscriminator reproduces claude's own hash.
func TestKeychainServicesConfigDirDiscriminator(t *testing.T) {
	t.Parallel()
	const dir = "/Users/pluvo/.claude-alt"
	sum := sha256.Sum256([]byte(dir))
	want := "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]

	got := KeychainServices(envOf(map[string]string{"CLAUDE_CONFIG_DIR": dir}))
	if len(got) != 3 || got[0] != want {
		t.Fatalf("services = %v, want %q first", got, want)
	}
	// A host that set CLAUDE_CONFIG_DIR after logging in has the plain name.
	if got[1] != "Claude Code-credentials" {
		t.Errorf("services[1] = %q, want the unhashed name", got[1])
	}
}

// TestKeychainServicesStorageOverride is claude's three-way switch; the middle
// case is the one worth pinning.
func TestKeychainServicesStorageOverride(t *testing.T) {
	t.Parallel()
	sum := sha256.Sum256([]byte("/srv/secure"))
	hashed := "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "storage dir set: it is what gets hashed",
			env:  map[string]string{"CLAUDE_SECURESTORAGE_CONFIG_DIR": "/srv/secure", "CLAUDE_CONFIG_DIR": "/ignored"},
			want: hashed,
		},
		{
			name: "storage dir set-but-EMPTY: no discriminator, config dir notwithstanding",
			env:  map[string]string{"CLAUDE_SECURESTORAGE_CONFIG_DIR": "", "CLAUDE_CONFIG_DIR": "/Users/x/.claude-alt"},
			want: "Claude Code-credentials",
		},
		{
			name: "neither set: the default name",
			env:  map[string]string{},
			want: "Claude Code-credentials",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := KeychainServices(envOf(tc.env)); got[0] != tc.want {
				t.Errorf("services[0] = %q, want %q", got[0], tc.want)
			}
		})
	}
}

// TestKeychainServiceOverrideIsTheWholeAnswer: an override must not be merely
// one candidate among four.
func TestKeychainServiceOverrideIsTheWholeAnswer(t *testing.T) {
	t.Parallel()
	got := KeychainServices(envOf(map[string]string{
		"PROVEO_KEYCHAIN_SERVICE": "my hand-made item",
		"CLAUDE_CONFIG_DIR":       "/somewhere",
	}))
	if len(got) != 1 || got[0] != "my hand-made item" {
		t.Errorf("services = %v, want exactly the override", got)
	}
}

// measuredPayload is the shape read out of a real Keychain item on macOS 26.6.2
// / claude 2.1.259, with the two tokens replaced.
func measuredPayload(accessTok, refreshTok string, expires, refreshExpires time.Time) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,`+
		`"expiresAt":%d,"refreshTokenExpiresAt":%d,`+
		`"scopes":["user:file_upload","user:inference","user:mcp_servers","user:profile","user:sessions:claude_code"],`+
		`"subscriptionType":"team","rateLimitTier":"default_claude_ai"}}`,
		accessTok, refreshTok, expires.UnixMilli(), refreshExpires.UnixMilli())
}

// keychainStub answers `security` reads from a service→payload table, so the
// candidate walk is exercised without a real store.
type keychainStub struct {
	items map[string]string
	// stderr, when set for a service, is returned instead of a payload.
	stderr map[string]string
	argv   [][]string
}

func (k *keychainStub) resolver() *secretref.Resolver {
	return &secretref.Resolver{
		GOOS: "darwin",
		Exec: func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
			k.argv = append(k.argv, append([]string{name}, args...))
			svc := ""
			for i, a := range args {
				if a == "-s" && i+1 < len(args) {
					svc = args[i+1]
				}
			}
			if e, ok := k.stderr[svc]; ok {
				return nil, []byte(e), fmt.Errorf("exit status 1")
			}
			if v, ok := k.items[svc]; ok {
				return []byte(v + "\n"), nil, nil
			}
			return nil, []byte("security: SecKeychainSearchCopyNext: " +
				"The specified item could not be found in the keychain."), fmt.Errorf("exit status 44")
		},
	}
}

func TestReadKeychainLoginLiveCredential(t *testing.T) {
	t.Parallel()
	now := time.Now()
	k := &keychainStub{items: map[string]string{
		"Claude Code-credentials": measuredPayload("sk-ant-oat01-a", "sk-ant-ort01-b",
			now.Add(2*time.Hour), now.Add(11*24*time.Hour)),
	}}
	got := ReadKeychainLogin("claudecode", envOf(map[string]string{"USER": "pluvo"}), k.resolver(), now)
	if !got.Found || !got.Usable || got.NeedsRefresh {
		t.Fatalf("login = %+v", got)
	}
	if got.Service != "Claude Code-credentials" {
		t.Errorf("Service = %q", got.Service)
	}
	if got.Subscription != "team" {
		t.Errorf("Subscription = %q, want team", got.Subscription)
	}
	if len(got.Scopes) != 5 {
		t.Errorf("Scopes = %v", got.Scopes)
	}
	// The report names the credential and its clock, and never the token.
	report := got.Report()
	for _, want := range []string{"live login", "Claude Code-credentials", "team", "access valid until", "refresh"} {
		if !strings.Contains(report, want) {
			t.Errorf("report %q lacks %q", report, want)
		}
	}
	for _, secret := range []string{"sk-ant-oat01-a", "sk-ant-ort01-b"} {
		if strings.Contains(report, secret) {
			t.Fatalf("report leaked a token: %q", report)
		}
	}
	// The struct itself holds no credential — parsed, judged, dropped. Asserted
	// over the rendered struct so a field added later cannot quietly retain one.
	if dump := fmt.Sprintf("%+v", got); strings.Contains(dump, "sk-ant-") {
		t.Fatalf("KeychainLogin retained a token: %s", dump)
	}
}

// TestReadKeychainLoginStaleAccessLiveRefresh: a stale access token beside a live
// refresh token is a login, not a dead credential.
func TestReadKeychainLoginStaleAccessLiveRefresh(t *testing.T) {
	t.Parallel()
	now := time.Now()
	k := &keychainStub{items: map[string]string{
		"Claude Code-credentials": measuredPayload("sk-ant-oat01-a", "sk-ant-ort01-b",
			now.Add(-time.Hour), now.Add(11*24*time.Hour)),
	}}
	got := ReadKeychainLogin("claudecode", envOf(nil), k.resolver(), now)
	if !got.Found || !got.Usable || !got.NeedsRefresh {
		t.Fatalf("login = %+v", got)
	}
	if !strings.Contains(got.Report(), "can still be renewed") {
		t.Errorf("report = %q", got.Report())
	}
}

// TestReadKeychainLoginRejectsBlankedTokens: the blanked shape must not read as
// a login from the store either.
func TestReadKeychainLoginRejectsBlankedTokens(t *testing.T) {
	t.Parallel()
	now := time.Now()
	k := &keychainStub{items: map[string]string{
		"Claude Code-credentials": measuredPayload("", "", now.Add(time.Hour), now.Add(24*time.Hour)),
	}}
	got := ReadKeychainLogin("claudecode", envOf(nil), k.resolver(), now)
	if got.Found {
		t.Fatalf("a blanked credential must not read as a login: %+v", got)
	}
	if got.Report() != "" {
		t.Errorf("report = %q, want silence", got.Report())
	}
}

// TestReadKeychainLoginAcceptsHexPayload: claude writes with `-X <hex>`, so
// `security -w` can return either encoding.
func TestReadKeychainLoginAcceptsHexPayload(t *testing.T) {
	t.Parallel()
	now := time.Now()
	json := measuredPayload("sk-ant-oat01-a", "sk-ant-ort01-b", now.Add(time.Hour), now.Add(24*time.Hour))
	k := &keychainStub{items: map[string]string{
		"Claude Code-credentials": hex.EncodeToString([]byte(json)),
	}}
	got := ReadKeychainLogin("claudecode", envOf(nil), k.resolver(), now)
	if !got.Found || !got.Usable {
		t.Fatalf("hex payload = %+v", got)
	}
}

// TestReadKeychainLoginWalksCandidates: not-found and an unparseable payload both
// continue the walk.
func TestReadKeychainLoginWalksCandidates(t *testing.T) {
	t.Parallel()
	now := time.Now()
	k := &keychainStub{items: map[string]string{
		"Claude Code-credentials": `{"someOtherTool":{"token":"x"}}`,
		"Claude Code": measuredPayload("sk-ant-oat01-a", "sk-ant-ort01-b",
			now.Add(time.Hour), now.Add(24*time.Hour)),
	}}
	got := ReadKeychainLogin("claudecode", envOf(map[string]string{"CLAUDE_CONFIG_DIR": "/alt"}), k.resolver(), now)
	if !got.Found || got.Service != "Claude Code" {
		t.Fatalf("login = %+v", got)
	}
}

// TestReadKeychainLoginStopsOnNonNotFound is the taxonomy's continuation rule:
// every outcome but not-found ends the walk.
func TestReadKeychainLoginStopsOnNonNotFound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		stderr  string
		want    secretref.Outcome
		wantSay string
	}{
		{
			name:    "denied",
			stderr:  "security: SecKeychainFindGenericPassword: User canceled the operation.",
			want:    secretref.Denied,
			wantSay: "you denied access",
		},
		{
			name:    "no GUI",
			stderr:  "security: SecKeychainFindGenericPassword: User interaction is not allowed.",
			want:    secretref.NoGUI,
			wantSay: "cannot ask",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := &keychainStub{stderr: map[string]string{"Claude Code-credentials": tc.stderr}}
			got := ReadKeychainLogin("claudecode", envOf(nil), k.resolver(), time.Now())
			if got.Found || got.Outcome != tc.want {
				t.Fatalf("login = %+v, want outcome %q", got, tc.want)
			}
			if len(k.argv) > 2 {
				t.Errorf("kept asking after %q: %d probes", tc.want, len(k.argv))
			}
			advice := got.KeychainFailureAdvice()
			if !strings.Contains(advice, tc.wantSay) {
				t.Errorf("advice = %q, want it to say %q", advice, tc.wantSay)
			}
			// Every outcome is a warning and a no-op, never a refusal.
			if strings.Contains(strings.ToLower(advice), "refus") {
				t.Errorf("advice refuses the run: %q", advice)
			}
		})
	}
}

// TestNoLoginOnThisHostIsSilent: a host that never used `claude` is not a fault.
func TestNoLoginOnThisHostIsSilent(t *testing.T) {
	t.Parallel()
	k := &keychainStub{}
	got := ReadKeychainLogin("claudecode", envOf(nil), k.resolver(), time.Now())
	if got.Found {
		t.Fatalf("login = %+v", got)
	}
	if got.Report() != "" || got.KeychainFailureAdvice() != "" {
		t.Errorf("report=%q advice=%q, want both silent", got.Report(), got.KeychainFailureAdvice())
	}
}

// TestUnknownHarnessReadsNothing: a target with no established store location is
// not probed at all.
func TestUnknownHarnessReadsNothing(t *testing.T) {
	t.Parallel()
	k := &keychainStub{items: map[string]string{"Claude Code-credentials": measuredPayload(
		"sk-ant-oat01-a", "sk-ant-ort01-b", time.Now().Add(time.Hour), time.Now().Add(time.Hour))}}
	for _, target := range []string{"cursor", "opencode", "cecli"} {
		got := ReadKeychainLogin(target, envOf(nil), k.resolver(), time.Now())
		if got.Found {
			t.Errorf("%s: read a claudecode credential: %+v", target, got)
		}
	}
	if len(k.argv) != 0 {
		t.Errorf("probed the store for a harness with no known entry: %v", k.argv)
	}
	if got := ReadKeychainLogin("claudecode-solidity", envOf(nil), k.resolver(), time.Now()); !got.Found {
		t.Errorf("claudecode-solidity should resolve to the claudecode entry: %+v", got)
	}
}

// TestKeychainAdviceNamesTheBackend: the report must name what can reach the
// credential, on both backends.
func TestKeychainAdviceNamesTheBackend(t *testing.T) {
	t.Parallel()
	live := KeychainLogin{Found: true, Usable: true}

	sbxAdvice := live.KeychainAdvice(true, false)
	if !strings.Contains(sbxAdvice, "sbx secret ls") {
		t.Errorf("sbx advice = %q, want it to name sbx's own store", sbxAdvice)
	}
	dockerAdvice := live.KeychainAdvice(false, false)
	if !strings.Contains(dockerAdvice, ".credentials.json") {
		t.Errorf("docker advice = %q, want it to name the file the agent reads", dockerAdvice)
	}
	if !strings.Contains(dockerAdvice, "will not write") {
		t.Errorf("docker advice = %q, want the refusal stated", dockerAdvice)
	}
	if got := live.KeychainAdvice(false, true); got != "" {
		t.Errorf("advice with a file login = %q, want silence", got)
	}
	dead := KeychainLogin{Found: true, Usable: false}
	if got := dead.KeychainAdvice(true, false); got != "" {
		t.Errorf("advice for a dead login = %q, want silence", got)
	}
}

// TestKeychainNeverFeedsSuppression pins the sbx inversion: the host store must
// reach neither PersistedLogin nor AuthSuppressor.
func TestKeychainNeverFeedsSuppression(t *testing.T) {
	t.Parallel()
	if HasPersistedLogin("claudecode", "") {
		t.Fatal("HasPersistedLogin true with no proveo home — the store leaked into it")
	}
	man := manifest.Manifest{Env: []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}}}
	suppress := AuthSuppressor(man, "claudecode", "", "")
	if suppress("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Error("the host store suppressed the env token — the sbx inversion is live")
	}
	if suppress("ANTHROPIC_API_KEY") {
		t.Error("the host store suppressed an API key")
	}
}
