// SPEC: _spec/internal/secretref/secret-references.puml
package secretref

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	SchemeKeychain = "keychain"
	SchemeCmd      = "cmd"
	SchemeEnv      = "env"
	SchemePass     = "pass"
)

// Outcome is the failure taxonomy: each value sends the operator somewhere
// different, which is why they are distinguished rather than collapsed.
type Outcome string

const (
	OK          Outcome = "ok"
	NotFound    Outcome = "not-found"
	Denied      Outcome = "denied"
	NoGUI       Outcome = "no-gui"
	TimedOut    Outcome = "timed-out"
	Unsupported Outcome = "unsupported"
	Failed      Outcome = "failed"
)

// Ref is a parsed reference. Arg is empty for a bare "keychain:".
type Ref struct {
	Scheme string
	Arg    string
}

// Parse splits a raw value into a reference. ok is false for a literal secret.
func Parse(raw string) (Ref, bool) {
	scheme, arg, found := strings.Cut(strings.TrimSpace(raw), ":")
	if !found {
		return Ref{}, false
	}
	switch scheme {
	case SchemeKeychain, SchemeCmd, SchemeEnv, SchemePass:
		return Ref{Scheme: scheme, Arg: strings.TrimSpace(arg)}, true
	}
	return Ref{}, false
}

// Result is what one resolution learned. Value is empty unless Outcome is OK;
// Source and Detail never carry the secret.
type Result struct {
	Value   string
	Outcome Outcome
	Source  string
	Detail  string
}

// DefaultTimeout bounds one resolver exec.
const DefaultTimeout = 20 * time.Second

type Resolver struct {
	GOOS    string
	Getenv  func(string) string
	Timeout time.Duration
	Exec    func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
	// Announce is called at most once, before the first exec-backed resolve.
	Announce func(scheme string)

	once   sync.Once
	mu     sync.Mutex
	cached map[string]Result
}

func (r *Resolver) goos() string {
	if r.GOOS != "" {
		return r.GOOS
	}
	return runtime.GOOS
}

func (r *Resolver) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return DefaultTimeout
}

// Resolve resolves ref on behalf of variable name, memoised per (name, ref).
func (r *Resolver) Resolve(name string, ref Ref) Result {
	key := name + "\x00" + ref.Scheme + "\x00" + ref.Arg
	r.mu.Lock()
	if res, ok := r.cached[key]; ok {
		r.mu.Unlock()
		return res
	}
	r.mu.Unlock()

	res := r.resolve(name, ref)

	r.mu.Lock()
	if r.cached == nil {
		r.cached = map[string]Result{}
	}
	r.cached[key] = res
	r.mu.Unlock()
	return res
}

func (r *Resolver) resolve(name string, ref Ref) Result {
	switch ref.Scheme {
	case SchemeEnv:
		src := ref.Arg
		if src == "" {
			// A bare "env:" would name the variable being resolved, forever.
			return Result{Outcome: Failed, Source: name, Detail: `"env:" needs a variable name (env:OTHER_VAR)`}
		}
		if r.Getenv == nil {
			return Result{Outcome: Failed, Source: src, Detail: "no environment to read"}
		}
		if v := strings.TrimSpace(r.Getenv(src)); v != "" {
			return Result{Value: v, Outcome: OK, Source: src}
		}
		return Result{Outcome: NotFound, Source: src}

	case SchemeKeychain:
		service := ref.Arg
		if service == "" {
			service = name
		}
		return r.Keychain(service, "")

	case SchemePass:
		if ref.Arg == "" {
			return Result{Outcome: Failed, Source: name, Detail: `"pass:" needs an entry name (pass:path/to/entry)`}
		}
		return r.run(SchemePass, ref.Arg, "pass", "show", ref.Arg)

	case SchemeCmd:
		if ref.Arg == "" {
			return Result{Outcome: Failed, Source: name, Detail: `"cmd:" needs a command`}
		}
		return r.run(SchemeCmd, ref.Arg, "sh", "-c", ref.Arg)
	}
	return Result{Outcome: Failed, Source: name, Detail: "unknown scheme " + ref.Scheme}
}

// Keychain reads one generic-password entry. account may be empty to match on
// the service alone. Read-only: proveo never calls add-generic-password.
func (r *Resolver) Keychain(service, account string) Result {
	if r.goos() != "darwin" {
		return Result{Outcome: Unsupported, Source: service,
			Detail: "the macOS Keychain exists on darwin only — use cmd: or pass: here"}
	}
	args := []string{"find-generic-password", "-w", "-s", service}
	if account != "" {
		args = append(args, "-a", account)
	}
	return r.run(SchemeKeychain, service, "/usr/bin/security", args...)
}

func (r *Resolver) run(scheme, source, name string, args ...string) Result {
	if r.Announce != nil {
		r.once.Do(func() { r.Announce(scheme) })
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout())
	defer cancel()

	runner := r.Exec
	if runner == nil {
		runner = execCommand
	}
	stdout, stderr, err := runner(ctx, name, args...)
	if ctx.Err() != nil {
		return Result{Outcome: TimedOut, Source: source,
			Detail: "no answer within " + r.timeout().String()}
	}
	if err != nil {
		return Result{Outcome: classify(string(stderr)), Source: source, Detail: firstLine(string(stderr))}
	}
	// Trailing newline only: a secret's interior is never ours to trim.
	if v := strings.TrimRight(string(stdout), "\r\n"); v != "" {
		return Result{Value: v, Outcome: OK, Source: source}
	}
	return Result{Outcome: NotFound, Source: source, Detail: "the entry exists and is empty"}
}

// classify maps a resolver's own stderr onto the taxonomy. Unrecognised text
// falls through to Failed, which quotes rather than labels.
func classify(stderr string) Outcome {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "could not be found"), strings.Contains(s, "itemnotfound"):
		return NotFound
	case strings.Contains(s, "interaction is not allowed"), strings.Contains(s, "interactionnotallowed"):
		return NoGUI
	case strings.Contains(s, "user canceled"), strings.Contains(s, "user cancelled"),
		strings.Contains(s, "usercanceled"), strings.Contains(s, "authorization was denied"):
		return Denied
	}
	return Failed
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func execCommand(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	cmd.Stdin = nil // never the operator's terminal
	err := cmd.Run()
	return []byte(out.String()), []byte(errb.String()), err
}

// Advice is the one sentence to print for a non-OK outcome.
func Advice(name string, res Result) string {
	switch res.Outcome {
	case OK:
		return ""
	case NotFound:
		return name + ": no entry named " + res.Source + " in the store — continuing with the credential you had"
	case Denied:
		return name + ": you denied access to " + res.Source + " — continuing with the credential you had"
	case NoGUI:
		return name + ": this session cannot ask for " + res.Source +
			" (ssh/CI has no way to show the prompt) — export the value instead"
	case TimedOut:
		return name + ": " + res.Source + " did not answer (" + res.Detail +
			") — continuing with the credential you had"
	case Unsupported:
		return name + ": " + res.Detail
	}
	detail := res.Detail
	if detail == "" {
		detail = "no output"
	}
	return name + ": could not read " + res.Source + " — " + detail
}
