package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func TestPrintSubscriptionAuthHints(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	man := manifest.Manifest{Name: "claudecode", Subscription: true}
	missing := []manifest.EnvVar{{
		Name:        "CLAUDE_CODE_OAUTH_TOKEN",
		Description: "Claude Code OAuth token",
		Secret:      true,
	}}
	var out strings.Builder
	printSubscriptionAuthHints(man, missing, &out)
	got := out.String()
	for _, want := range []string{
		"CLAUDE_CODE_OAUTH_TOKEN",
		"claude setup-token",
		".env",
		"export CLAUDE_CODE_OAUTH_TOKEN=",
		"SAFE host location",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hint output missing %q; got:\n%s", want, got)
		}
	}
	if runtime.GOOS == "linux" && !strings.Contains(got, ".zshrc") {
		t.Errorf("expected zshrc path on linux, got:\n%s", got)
	}
}

func TestPrintSubscriptionAuthHintsCursorAndVariants(t *testing.T) {
	t.Setenv("SHELL", "/bin/bash")
	tests := []struct {
		harness string
		env     string
		want    []string
	}{
		{"cursor", "CURSOR_API_KEY", []string{"CURSOR_API_KEY", "agent login", "cursor.com/dashboard", "export CURSOR_API_KEY="}},
		{"claudecode-solidity", "CLAUDE_CODE_OAUTH_TOKEN", []string{"CLAUDE_CODE_OAUTH_TOKEN", "claude setup-token"}},
	}
	for _, tc := range tests {
		t.Run(tc.harness, func(t *testing.T) {
			var out strings.Builder
			printSubscriptionAuthHints(
				manifest.Manifest{Name: tc.harness, Subscription: true},
				[]manifest.EnvVar{{Name: tc.env, Secret: true}},
				&out,
			)
			got := out.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("%s hint missing %q; got:\n%s", tc.harness, want, got)
				}
			}
		})
	}
}

func TestPrintSubscriptionAuthHintsEmpty(t *testing.T) {
	var out strings.Builder
	printSubscriptionAuthHints(manifest.Manifest{Name: "claudecode"}, nil, &out)
	if out.Len() != 0 {
		t.Errorf("empty missing should print nothing, got %q", out.String())
	}
}

func TestHarnessFamily(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"claudecode", "claudecode"},
		{"claudecode-solidity", "claudecode"},
		{"claudecode-browser", "claudecode"},
		{"cursor-browser", "cursor"},
		{"opencode", "opencode"},
		{"unknown", "unknown"},
	}
	for _, tc := range tests {
		if got := harnessFamily(tc.in); got != tc.want {
			t.Errorf("harnessFamily(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFishExportInHints(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/fish")
	var out strings.Builder
	printSubscriptionAuthHints(
		manifest.Manifest{Name: "cursor"},
		[]manifest.EnvVar{{Name: "CURSOR_API_KEY", Secret: true}},
		&out,
	)
	if !strings.Contains(out.String(), `set -gx CURSOR_API_KEY`) {
		t.Errorf("fish hint should use set -gx; got:\n%s", out.String())
	}
}

// claudecodeMan is the shape the hint is read against: one declared secret, which is
// what the harness can actually authenticate with.
func claudecodeMan() manifest.Manifest {
	return manifest.Manifest{
		Name: "claudecode", Subscription: true,
		Env: []manifest.EnvVar{{
			Name:        "CLAUDE_CODE_OAUTH_TOKEN",
			Description: "Claude Code OAuth token — generate one with `claude setup-token`",
			Secret:      true,
		}},
	}
}

// The silent death is the failure with no tail and no transcript, and it used to
// reach the operator as nothing but a stopped sandbox — which reads as an
// infrastructure fault and sends them into `sbx exec` after a cause that is on the
// host. The hint has to name the credential, the command that mints one, and the
// store trap: an entry can exist holding an EMPTY value, and `sbx secret ls` shows
// the name either way.
func TestNoCredentialHintNamesTheRemedy(t *testing.T) {
	t.Parallel()
	man := claudecodeMan()
	// The failing shape: the variable reached the sandbox stated empty, no login on
	// disk, nothing written to the store.
	got := strings.Join(noCredentialHint(man, "claudecode", t.TempDir(),
		[]string{"CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_MODEL=claude-opus-5"}, nil, nil, nil), "\n")
	if got == "" {
		t.Fatal("a run with no credential must say so; the stopped sandbox is not a diagnosis")
	}
	for _, want := range []string{
		"no credential reached the agent",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"claude setup-token",
		"sbx secret set CLAUDE_CODE_OAUTH_TOKEN",
		"EMPTY value",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hint missing %q; got:\n%s", want, got)
		}
	}
}

// A hint that fires on every failure teaches the operator to skip the one time it
// was right, so each way a credential can legitimately arrive must silence it.
func TestNoCredentialHintStaysSilentWhenOneArrived(t *testing.T) {
	t.Parallel()
	man := claudecodeMan()

	for _, tc := range []struct {
		name    string
		env     []string
		secrets [][2]string
		lookup  func(string) string
	}{
		{
			name:    "written to the store with a real value",
			env:     []string{"CLAUDE_CODE_OAUTH_TOKEN="},
			secrets: [][2]string{{"CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat-real"}},
		},
		{
			name: "stated with a real value",
			env:  []string{"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat-real"},
		},
		{
			// Forward-by-name: docker copies the value out of proveo's own
			// environment at launch, so the lookup is what decides whether the bare
			// name carries anything.
			name:   "forwarded by name, lookup holds it",
			env:    []string{"CLAUDE_CODE_OAUTH_TOKEN"},
			lookup: func(string) string { return "sk-ant-oat-real" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if h := noCredentialHint(man, "claudecode", t.TempDir(), tc.env, tc.secrets, nil, tc.lookup); h != nil {
				t.Errorf("a credential DID reach the agent; hint must stay silent, got:\n%s",
					strings.Join(h, "\n"))
			}
		})
	}

	// A bare name whose lookup is empty carries nothing, so the hint must fire.
	if h := noCredentialHint(man, "claudecode", t.TempDir(),
		[]string{"CLAUDE_CODE_OAUTH_TOKEN"}, nil, nil, func(string) string { return "" }); h == nil {
		t.Error("a forwarded name with nothing behind it is not a credential")
	}
}

// A usable login on disk is a credential, and blaming the credential when one is
// sitting there sends the operator after the wrong thing.
func TestNoCredentialHintDefersToAPersistedLogin(t *testing.T) {
	t.Parallel()
	man := claudecodeMan()
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	live := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"real","expiresAt":%d}}`,
		time.Now().Add(8*time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}
	if h := noCredentialHint(man, "claudecode", home, []string{"CLAUDE_CODE_OAUTH_TOKEN="}, nil, nil, nil); h != nil {
		t.Errorf("a live login is a credential; hint must stay silent, got:\n%s", strings.Join(h, "\n"))
	}

	// The macOS-blanked file is NOT a login, so the hint must fire on it — this is
	// the exact shape that produced the silent death.
	blanked := `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,` +
		`"refreshTokenExpiresAt":4102444800000}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(blanked), 0o600); err != nil {
		t.Fatal(err)
	}
	if h := noCredentialHint(man, "claudecode", home, []string{"CLAUDE_CODE_OAUTH_TOKEN="}, nil, nil, nil); h == nil {
		t.Error("a blanked login cannot authenticate; the hint must name the remedy")
	}
}

// The hint's confident sentence was wrong on the run that most needed it.
//
// sbx's credential store is GLOBAL and outlives every run, so a token written last
// week is injected into a sandbox this run gave nothing to. proveo reads its OWN
// decision to answer "did a credential reach the agent", which cannot see that
// entry — so a run whose stored CLAUDE_CODE_OAUTH_TOKEN was live and answering 200
// from api.anthropic.com would have been told no credential reached the agent, and
// sent after the one thing that was working.
//
// The store cannot resolve it either: `sbx secret ls` prints the name and
// "(stored)", never the value, so an entry holding an empty string is
// indistinguishable from a live token. The hint therefore drops the claim and names
// the suspect.
func TestNoCredentialHintDoesNotBlameACredentialTheStoreMayHold(t *testing.T) {
	t.Parallel()
	man := claudecodeMan()
	env := []string{"CLAUDE_CODE_OAUTH_TOKEN="}

	// Nothing anywhere: the strong claim is earned.
	bare := strings.Join(noCredentialHint(man, "claudecode", t.TempDir(), env, nil, nil, nil), "\n")
	if !strings.Contains(bare, "no credential reached the agent") {
		t.Errorf("with nothing anywhere the hint must say so plainly, got:\n%s", bare)
	}

	// The store already lists it. Still a hint — this run sent nothing, which is
	// worth saying — but it must not assert what it cannot know.
	held := strings.Join(noCredentialHint(man, "claudecode", t.TempDir(), env, nil,
		[]string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "github"}, nil), "\n")
	if held == "" {
		t.Fatal("a run that sent no credential of its own is still worth explaining")
	}
	if strings.Contains(held, "no credential reached the agent") {
		t.Errorf("the store may hold a live token; the hint must not claim none arrived, got:\n%s", held)
	}
	for _, want := range []string{
		"CLAUDE_CODE_OAUTH_TOKEN", // the suspect is named
		"cannot read",             // and the limit of what proveo knows is stated
	} {
		if !strings.Contains(held, want) {
			t.Errorf("hint missing %q; got:\n%s", want, held)
		}
	}
	// A store entry for a credential this harness does not use says nothing about it.
	other := strings.Join(noCredentialHint(man, "claudecode", t.TempDir(), env, nil,
		[]string{"CURSOR_API_KEY", "github"}, nil), "\n")
	if !strings.Contains(other, "no credential reached the agent") {
		t.Errorf("another harness's stored key is not this one's credential, got:\n%s", other)
	}
}
