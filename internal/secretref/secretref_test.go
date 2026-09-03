// SPEC: _spec/internal/secretref/secret-references.puml
package secretref

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseSchemes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw        string
		wantScheme string
		wantArg    string
		wantRef    bool
	}{
		{raw: "keychain:", wantScheme: SchemeKeychain, wantRef: true},
		{raw: "keychain:Claude Code-credentials", wantScheme: SchemeKeychain, wantArg: "Claude Code-credentials", wantRef: true},
		{raw: "cmd:op read op://Private/x/key", wantScheme: SchemeCmd, wantArg: "op read op://Private/x/key", wantRef: true},
		{raw: "pass:llm/anthropic", wantScheme: SchemePass, wantArg: "llm/anthropic", wantRef: true},
		{raw: "env:OTHER", wantScheme: SchemeEnv, wantArg: "OTHER", wantRef: true},
		{raw: "  keychain:svc  ", wantScheme: SchemeKeychain, wantArg: "svc", wantRef: true},
		// Literal secrets: a real key can carry a colon.
		{raw: "sk-ant-oat01-abc"},
		{raw: "sk-ant-api03-a:b:c"},
		{raw: "vault:secret/x"}, // an unknown scheme is a literal
		{raw: "https://example.com/token"},
		{raw: ""},
	}
	for _, tc := range tests {
		ref, ok := Parse(tc.raw)
		if ok != tc.wantRef {
			t.Errorf("Parse(%q) ok = %v, want %v", tc.raw, ok, tc.wantRef)
			continue
		}
		if ok && (ref.Scheme != tc.wantScheme || ref.Arg != tc.wantArg) {
			t.Errorf("Parse(%q) = %+v, want {%s %s}", tc.raw, ref, tc.wantScheme, tc.wantArg)
		}
	}
}

// fakeExec records the argv it was handed and replays a canned answer.
type fakeExec struct {
	calls  [][]string
	stdout string
	stderr string
	err    error
	block  time.Duration
}

func (f *fakeExec) run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.block > 0 {
		select {
		case <-time.After(f.block):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	return []byte(f.stdout), []byte(f.stderr), f.err
}

func TestKeychainArgvAndValue(t *testing.T) {
	t.Parallel()
	f := &fakeExec{stdout: "sk-ant-secret\n"}
	r := &Resolver{GOOS: "darwin", Exec: f.run}

	res := r.Keychain("Claude Code-credentials", "pluvo")
	if res.Outcome != OK || res.Value != "sk-ant-secret" {
		t.Fatalf("Keychain = %+v", res)
	}
	got := strings.Join(f.calls[0], " ")
	want := "/usr/bin/security find-generic-password -w -s Claude Code-credentials -a pluvo"
	if got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
	// An empty account must not render a bare `-a`.
	f.calls = nil
	if res := r.Keychain("svc", ""); res.Outcome != OK {
		t.Fatalf("Keychain without account = %+v", res)
	}
	if strings.Contains(strings.Join(f.calls[0], " "), "-a") {
		t.Errorf("empty account rendered a flag: %v", f.calls[0])
	}
}

// TestKeychainTrimsOnlyTrailingNewline: `security -w` appends a newline, and a
// secret's interior is never ours to touch.
func TestKeychainTrimsOnlyTrailingNewline(t *testing.T) {
	t.Parallel()
	f := &fakeExec{stdout: "  spaces inside  and a tab\t\r\n"}
	r := &Resolver{GOOS: "darwin", Exec: f.run}
	if got := r.Keychain("svc", "").Value; got != "  spaces inside  and a tab\t" {
		t.Errorf("value = %q", got)
	}
}

// TestBareKeychainUsesVariableName: a bare "keychain:" reads the entry named
// after the variable.
func TestBareKeychainUsesVariableName(t *testing.T) {
	t.Parallel()
	f := &fakeExec{stdout: "v\n"}
	r := &Resolver{GOOS: "darwin", Exec: f.run}
	ref, _ := Parse("keychain:")
	if res := r.Resolve("ANTHROPIC_API_KEY", ref); res.Outcome != OK || res.Source != "ANTHROPIC_API_KEY" {
		t.Fatalf("bare keychain = %+v", res)
	}
	if !strings.Contains(strings.Join(f.calls[0], " "), "-s ANTHROPIC_API_KEY") {
		t.Errorf("argv = %v", f.calls[0])
	}
}

// TestFailureTaxonomy covers the classifier. The not-found line is measured
// (exit 44, macOS 26); the other two are documented upstream.
func TestFailureTaxonomy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		stderr string
		want   Outcome
	}{
		{
			name:   "measured not-found",
			stderr: "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.",
			want:   NotFound,
		},
		{
			name:   "not-found via the other operation name",
			stderr: "security: SecKeychainFindGenericPassword: The specified item could not be found in the keychain.",
			want:   NotFound,
		},
		{
			name:   "no GUI session (ssh/CI)",
			stderr: "security: SecKeychainFindGenericPassword: User interaction is not allowed.",
			want:   NoGUI,
		},
		{
			name:   "operator denied the dialog",
			stderr: "security: SecKeychainFindGenericPassword: User canceled the operation.",
			want:   Denied,
		},
		{
			name:   "unrecognised: quoted, never labelled",
			stderr: "security: something nobody has seen yet",
			want:   Failed,
		},
		{name: "no stderr at all", stderr: "", want: Failed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeExec{stderr: tc.stderr, err: errors.New("exit status 44")}
			r := &Resolver{GOOS: "darwin", Exec: f.run}
			res := r.Keychain("svc", "")
			if res.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", res.Outcome, tc.want)
			}
			if res.Value != "" {
				t.Error("a failed resolve must return no value")
			}
			if a := Advice("ANTHROPIC_API_KEY", res); !strings.Contains(a, "ANTHROPIC_API_KEY") {
				t.Errorf("advice does not name the variable: %q", a)
			}
			if tc.want == Failed && tc.stderr != "" && !strings.Contains(Advice("K", res), tc.stderr) {
				t.Errorf("Failed must quote the resolver verbatim: %q", Advice("K", res))
			}
		})
	}
}

// TestBoundedExec: an unanswered modal must not hang proveo before launch.
func TestBoundedExec(t *testing.T) {
	t.Parallel()
	f := &fakeExec{block: 5 * time.Second, stdout: "too late"}
	r := &Resolver{GOOS: "darwin", Exec: f.run, Timeout: 20 * time.Millisecond}
	start := time.Now()
	res := r.Keychain("svc", "")
	if res.Outcome != TimedOut {
		t.Fatalf("outcome = %q, want %q", res.Outcome, TimedOut)
	}
	if res.Value != "" {
		t.Error("a timed-out resolve must return no value")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the bound did not hold: waited %s", elapsed)
	}
}

// TestAnnounceFiresOnceBeforeAnyExec: announced before it can block, and once.
func TestAnnounceFiresOnceBeforeAnyExec(t *testing.T) {
	t.Parallel()
	f := &fakeExec{stdout: "v\n"}
	var announced []string
	execCount := 0
	r := &Resolver{
		GOOS: "darwin",
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			if len(announced) == 0 {
				t.Error("exec ran before Announce")
			}
			execCount++
			return f.run(ctx, name, args...)
		},
		Announce: func(scheme string) { announced = append(announced, scheme) },
	}
	r.Keychain("a", "")
	r.Keychain("b", "")
	r.Keychain("c", "")
	if len(announced) != 1 || announced[0] != SchemeKeychain {
		t.Errorf("announced = %v, want one %q", announced, SchemeKeychain)
	}
	if execCount != 3 {
		t.Errorf("execCount = %d, want 3", execCount)
	}
}

// TestResolveMemoisesPerVariable: the lookup is called in loops, so one resolve.
func TestResolveMemoisesPerVariable(t *testing.T) {
	t.Parallel()
	f := &fakeExec{stdout: "v\n"}
	r := &Resolver{GOOS: "darwin", Exec: f.run}
	ref, _ := Parse("keychain:svc")
	for range 5 {
		if res := r.Resolve("K", ref); res.Value != "v" {
			t.Fatalf("resolve = %+v", res)
		}
	}
	if len(f.calls) != 1 {
		t.Errorf("exec ran %d times, want 1", len(f.calls))
	}
	// Cached per (variable, reference), so a second variable asks again.
	if res := r.Resolve("OTHER", ref); res.Outcome != OK {
		t.Fatalf("second variable = %+v", res)
	}
	if len(f.calls) != 2 {
		t.Errorf("exec ran %d times, want 2", len(f.calls))
	}
}

// TestKeychainUnsupportedOffDarwin: no `security` binary there, so it answers
// rather than shelling out, and names the escape hatch.
func TestKeychainUnsupportedOffDarwin(t *testing.T) {
	t.Parallel()
	f := &fakeExec{stdout: "v\n"}
	r := &Resolver{GOOS: "linux", Exec: f.run}
	res := r.Keychain("svc", "")
	if res.Outcome != Unsupported {
		t.Fatalf("outcome = %q, want %q", res.Outcome, Unsupported)
	}
	if len(f.calls) != 0 {
		t.Errorf("shelled out anyway: %v", f.calls)
	}
	if a := Advice("K", res); !strings.Contains(a, "cmd:") {
		t.Errorf("advice should name the escape hatch: %q", a)
	}
}

func TestCmdAndPassArgv(t *testing.T) {
	t.Parallel()
	f := &fakeExec{stdout: "secret\n"}
	r := &Resolver{GOOS: "linux", Exec: f.run}

	ref, _ := Parse("cmd:op read op://Private/anthropic/key")
	if res := r.Resolve("K", ref); res.Outcome != OK || res.Value != "secret" {
		t.Fatalf("cmd = %+v", res)
	}
	want := "sh -c op read op://Private/anthropic/key"
	if got := strings.Join(f.calls[0], " "); got != want {
		t.Errorf("cmd argv = %q, want %q", got, want)
	}

	f.calls = nil
	ref, _ = Parse("pass:llm/anthropic")
	if res := r.Resolve("K2", ref); res.Outcome != OK {
		t.Fatalf("pass = %+v", res)
	}
	if got := strings.Join(f.calls[0], " "); got != "pass show llm/anthropic" {
		t.Errorf("pass argv = %q", got)
	}
}

func TestEnvSchemeNeedsAName(t *testing.T) {
	t.Parallel()
	r := &Resolver{Getenv: func(k string) string {
		if k == "OTHER" {
			return "from-other"
		}
		return ""
	}}
	ref, _ := Parse("env:OTHER")
	if res := r.Resolve("K", ref); res.Value != "from-other" {
		t.Fatalf("env: = %+v", res)
	}
	ref, _ = Parse("env:MISSING")
	if res := r.Resolve("K", ref); res.Outcome != NotFound {
		t.Errorf("missing env: = %+v", res)
	}
	ref, _ = Parse("env:")
	if res := r.Resolve("K", ref); res.Outcome != Failed {
		t.Errorf("bare env: = %+v", res)
	}
}

// TestEmptyEntryIsNotFound: a blank value must not occupy the credential slot.
func TestEmptyEntryIsNotFound(t *testing.T) {
	t.Parallel()
	f := &fakeExec{stdout: "\n"}
	r := &Resolver{GOOS: "darwin", Exec: f.run}
	res := r.Keychain("svc", "")
	if res.Outcome != NotFound || res.Value != "" {
		t.Fatalf("empty entry = %+v", res)
	}
}
