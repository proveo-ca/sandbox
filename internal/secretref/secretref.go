// SPEC: _spec/internal/secretref/secret-references.puml, _spec/internal/sbx/credential-resolution.puml (R11)
package secretref

import (
	"strings"
)

const (
	SchemeKeychain = "keychain"
	SchemeCmd      = "cmd"
	SchemeEnv      = "env"
	SchemePass     = "pass"
)

// Ref is a parsed reference.
type Ref struct {
	Scheme string
	Arg    string
}

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

// IsReference reports whether raw names where a secret lives rather than holding it.
func IsReference(raw string) bool {
	_, ok := Parse(raw)
	return ok || isSbxRef(raw)
}

// isSbxRef is a source sbx resolves itself: a 1Password item or an AWS Secrets Manager ARN.
func isSbxRef(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, "op://") || strings.HasPrefix(raw, "arn:aws:secretsmanager:")
}

// SbxSource is the `sbx secret set` source flags for raw, held for variable name; stdin carries the value
// when raw is a plain value or an env: reference, and is "" when sbx resolves the source itself.
func SbxSource(name, raw string, getenv func(string) string) (flags []string, stdin string) {
	raw = strings.TrimSpace(raw)
	if isSbxRef(raw) {
		return []string{"--ref", raw}, ""
	}
	ref, ok := Parse(raw)
	if !ok {
		return nil, raw
	}
	switch ref.Scheme {
	case SchemeEnv:
		return nil, strings.TrimSpace(getenv(ref.Arg))
	case SchemeKeychain:
		service := ref.Arg
		if service == "" {
			service = name
		}
		return []string{"--command", "/usr/bin/security find-generic-password -w -s " + shellQuote(service)}, ""
	case SchemePass:
		return []string{"--command", "pass show " + shellQuote(ref.Arg) + " | head -n 1"}, ""
	}
	return []string{"--command", ref.Arg}, ""
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
