//go:build e2e

// SPEC: _spec/internal/secretref/secret-references.puml, _spec/tests/testing-strategy.puml

package e2e

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/secretref"
)

// TestHostKeychainReadsTheRealStore exercises the resolver against this machine's
// login Keychain — the one thing a stub cannot assert: that the service-name
// algorithm proveo reproduces still matches what `claude` writes.
//
//	go test -tags=e2e ./e2e/ -run HostKeychain -v
func TestHostKeychainReadsTheRealStore(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the login Keychain exists on darwin only")
	}
	if _, err := exec.LookPath("/usr/bin/security"); err != nil {
		t.Skip("/usr/bin/security not present")
	}

	r := &secretref.Resolver{Timeout: 10 * time.Second}
	got := credentials.ReadKeychainLogin("claudecode", credentials.OSLookupEnv, r, time.Now())

	if !got.Found {
		// Not a failure: a host that never ran `claude` interactively has no item.
		// The taxonomy outcome is what matters, and it must never be a hard error.
		t.Skipf("no claudecode login in this host's Keychain (outcome %q, %s)", got.Outcome, got.Detail)
	}

	if got.Service == "" {
		t.Error("a found login must name the service that answered")
	}
	if got.ExpiresAt.IsZero() {
		t.Error("the measured payload carries expiresAt; a found login should expose it")
	}
	// The whole credential, not a bare access token: without a refresh window a
	// long session dies mid-task on an auth error proveo caused.
	if got.RefreshExpiresAt.IsZero() {
		t.Error("no refreshTokenExpiresAt — the payload is not the whole credential")
	}
	// Nothing about the report or the struct may carry the token.
	if dump := got.Report() + "|" + got.KeychainAdvice(true, false); strings.Contains(dump, "sk-ant-") {
		t.Fatalf("a token reached operator-visible output: %q", dump)
	}
	t.Logf("service=%q usable=%v needsRefresh=%v subscription=%q",
		got.Service, got.Usable, got.NeedsRefresh, got.Subscription)
}

// TestHostKeychainMissingServiceIsNotFound pins the measured failure string the
// classifier keys on, against the real `security` binary rather than a stub.
func TestHostKeychainMissingServiceIsNotFound(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the login Keychain exists on darwin only")
	}
	r := &secretref.Resolver{Timeout: 10 * time.Second}
	res := r.Keychain("proveo-e2e-no-such-service-0000", "")
	if res.Outcome != secretref.NotFound {
		t.Fatalf("outcome = %q (detail %q), want %q", res.Outcome, res.Detail, secretref.NotFound)
	}
	if res.Value != "" {
		t.Error("a not-found read must return no value")
	}
}
