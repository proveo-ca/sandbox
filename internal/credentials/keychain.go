// SPEC: _spec/internal/secretref/secret-references.puml,
// _spec/_paradigms/credential-boundary.puml
//
// SPEC: _spec/internal/secretref/secret-references.puml, _spec/_paradigms/credential-boundary.puml
package credentials

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/secretref"
)

var keychainAccountOK = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

const keychainAccountFallback = "claude-code-user"

// LookupEnv is the environment shape the service-name algorithm needs: it gives
// an ABSENT variable and one set to "" different answers, which os.Getenv
// cannot express.
type LookupEnv func(string) (string, bool)

func OSLookupEnv(name string) (string, bool) { return os.LookupEnv(name) }

func KeychainAccount(look LookupEnv) string {
	user, _ := look("USER")
	user = strings.TrimSpace(user)
	if user == "" || !keychainAccountOK.MatchString(user) {
		return keychainAccountFallback
	}
	return user
}

func KeychainServices(look LookupEnv) []string {
	if override, _ := look("PROVEO_KEYCHAIN_SERVICE"); strings.TrimSpace(override) != "" {
		return []string{strings.TrimSpace(override)}
	}
	const base = "Claude Code" + keychainOAuthSuffix + "-credentials"
	out := []string{}
	if h := keychainConfigHash(look); h != "" {
		out = append(out, base+"-"+h)
	}
	out = append(out, base, "Claude Code")
	return out
}

const keychainOAuthSuffix = ""

func keychainConfigHash(look LookupEnv) string {
	storage, storageSet := look("CLAUDE_SECURESTORAGE_CONFIG_DIR")
	storage = strings.TrimSpace(storage)
	configDir, _ := look("CLAUDE_CONFIG_DIR")
	configDir = strings.TrimSpace(configDir)

	var dir string
	switch {
	case storageSet && storage == "":
		return "" // set-but-empty switches the discriminator off
	case storageSet:
		dir = storage
	case configDir == "":
		return "" // the default config dir carries none
	default:
		dir = configDir
	}
	sum := sha256.Sum256([]byte(norm.NFC.String(dir)))
	return hex.EncodeToString(sum[:])[:8]
}

var keychainLoginServices = map[string]func(LookupEnv) []string{
	"claudecode": KeychainServices,
}

// KeychainLogin is what the host store holds for this run — metadata only.
type KeychainLogin struct {
	Found   bool
	Service string
	Outcome secretref.Outcome
	Detail  string

	Usable       bool
	NeedsRefresh bool

	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
	Scopes           []string
	Subscription     string
}

func ReadKeychainLogin(target string, look LookupEnv, r *secretref.Resolver, now time.Time) KeychainLogin {
	services, ok := keychainLoginServices[HarnessFamily(target)]
	if !ok || r == nil {
		return KeychainLogin{}
	}
	account := KeychainAccount(look)
	last := KeychainLogin{Outcome: secretref.NotFound}
	for _, svc := range services(look) {
		for _, acct := range []string{account, ""} {
			res := r.Keychain(svc, acct)
			if res.Outcome != secretref.OK {
				if res.Outcome == secretref.NotFound {
					last = KeychainLogin{Service: svc, Outcome: secretref.NotFound}
					continue
				}
				return KeychainLogin{Service: svc, Outcome: res.Outcome, Detail: res.Detail}
			}
			login, parsed := parseKeychainCredential(res.Value, now)
			if !parsed {
				last = KeychainLogin{Service: svc, Outcome: secretref.NotFound,
					Detail: "the entry holds no recognisable login"}
				continue
			}
			login.Service = svc
			return login
		}
	}
	return last
}

func parseKeychainCredential(payload string, now time.Time) (KeychainLogin, bool) {
	raw := []byte(strings.TrimSpace(payload))
	if len(raw) == 0 {
		return KeychainLogin{}, false
	}
	if decoded, err := hex.DecodeString(string(raw)); err == nil && len(decoded) > 0 {
		raw = decoded
	}
	var c oauthCredential
	if json.Unmarshal(raw, &c) != nil {
		return KeychainLogin{}, false
	}
	o := &c.ClaudeAIOauth
	if o.AccessToken == nil || strings.TrimSpace(*o.AccessToken) == "" {
		return KeychainLogin{}, false
	}
	usable, needsRefresh := loginUsableBytes(raw, now)
	out := KeychainLogin{
		Found:        true,
		Outcome:      secretref.OK,
		Usable:       usable,
		NeedsRefresh: needsRefresh,
		Scopes:       o.Scopes,
		Subscription: strings.TrimSpace(o.SubscriptionType),
	}
	if o.ExpiresAt != nil {
		out.ExpiresAt = time.UnixMilli(*o.ExpiresAt)
	}
	if o.RefreshTokenExpiresAt != nil {
		out.RefreshExpiresAt = time.UnixMilli(*o.RefreshTokenExpiresAt)
	}
	return out, true
}

// Report is the line proveo prints for what the host store holds, empty when
// there is nothing worth saying.
func (k KeychainLogin) Report() string {
	if !k.Found {
		return ""
	}
	var b strings.Builder
	b.WriteString("macOS Keychain holds a ")
	switch {
	case k.Usable && !k.NeedsRefresh:
		b.WriteString("live login")
	case k.Usable && k.NeedsRefresh:
		b.WriteString("login whose access token has expired but can still be renewed")
	default:
		b.WriteString("login that can no longer authenticate")
	}
	b.WriteString(" (" + k.Service)
	if k.Subscription != "" {
		b.WriteString(", " + k.Subscription)
	}
	b.WriteString(")")
	if !k.ExpiresAt.IsZero() {
		fmt.Fprintf(&b, ", access valid %s", until(k.ExpiresAt))
	}
	if !k.RefreshExpiresAt.IsZero() {
		fmt.Fprintf(&b, ", refresh %s", until(k.RefreshExpiresAt))
	}
	return b.String()
}

func until(t time.Time) string {
	return "until " + t.Local().Format("2006-01-02 15:04")
}

// KeychainAdvice explains what the host store's login can and cannot do for
// THIS backend — the half that stops Report from being trivia.
func (k KeychainLogin) KeychainAdvice(sbxBackend, fileLogin bool) string {
	if !k.Found || !k.Usable {
		return ""
	}
	if fileLogin {
		return "" // the run already has a credential it can use
	}
	if sbxBackend {
		return "the sandbox cannot read it — sbx keeps its own credential in its store " +
			"(`sbx secret ls`), and proveo has no supported way to write a subscription " +
			"login there; run /login once inside a proveo run, or export a token"
	}
	return "the container cannot read it — in there the credential IS " +
		"~/.claude/.credentials.json, and proveo will not write a plaintext token into " +
		"the mounted proveo home; run /login once inside a proveo run, or export a token"
}

// KeychainFailureAdvice turns a non-OK read into the sentence the taxonomy
// prescribes.
func (k KeychainLogin) KeychainFailureAdvice() string {
	if k.Found {
		return ""
	}
	switch k.Outcome {
	case secretref.NotFound:
		if k.Detail == "" {
			return ""
		}
		return fmt.Sprintf("host Keychain: %s (%s) — name the right item with "+
			"PROVEO_KEYCHAIN_SERVICE", k.Detail, k.Service)
	case secretref.Denied:
		return "host Keychain: you denied access — continuing with the credential you had"
	case secretref.NoGUI:
		return "host Keychain: this session cannot ask (ssh/CI has no way to show the " +
			"prompt) — export the token instead"
	case secretref.TimedOut:
		return "host Keychain: no answer (" + k.Detail + ") — continuing with the credential you had"
	case secretref.Unsupported, secretref.OK:
		return ""
	}
	detail := k.Detail
	if detail == "" {
		detail = "no output"
	}
	return "host Keychain: could not read it — " + detail
}

// SPEC: _spec/internal/sbx/oauth-provisioning.puml
func NeedsSandboxLogin(man manifest.Manifest, sbxBackend, fileLogin bool, stored []string, lookup func(string) string) bool {
	if !sbxBackend || !man.Subscription || fileLogin {
		return false
	}
	if len(StoreHolds(man, stored)) > 0 {
		return false
	}
	for _, e := range man.Env {
		if e.Secret && lookup != nil && strings.TrimSpace(lookup(e.Name)) != "" {
			return false
		}
	}
	return true
}

// SandboxLoginHint is what proveo says when the host store holds a login the
// sandbox cannot reach.
func (k KeychainLogin) SandboxLoginHint(argv string) []string {
	lines := []string{
		"the sandbox has no credential of its own, and sbx's OAuth slot has no import path",
	}
	if k.Found && k.Usable {
		lines = append(lines,
			"  your host Keychain DOES hold a live login — it just cannot cross into the sandbox")
	}
	if argv != "" {
		lines = append(lines, "  sign in once inside a sandbox; sbx's proxy keeps the token host-side:",
			"      "+argv)
	}
	return lines
}
