// SPEC: _spec/internal/credentials/secret-references.puml, _spec/internal/sbx/credential-resolution.puml (R11)
package credentials

import (
	"strings"
)

const (
	SchemeKeychain = "keychain"
	SchemeCmd      = "cmd"
	SchemeEnv      = "env"
	SchemePass     = "pass"
)

// SecretRef is a parsed reference.
type SecretRef struct {
	Scheme string
	Arg    string
}

func ParseSecretRef(raw string) (SecretRef, bool) {
	scheme, arg, found := strings.Cut(strings.TrimSpace(raw), ":")
	if !found {
		return SecretRef{}, false
	}
	switch scheme {
	case SchemeKeychain, SchemeCmd, SchemeEnv, SchemePass:
		return SecretRef{Scheme: scheme, Arg: strings.TrimSpace(arg)}, true
	}
	return SecretRef{}, false
}

// IsSecretRef reports whether raw names where a secret lives rather than holding it.
func IsSecretRef(raw string) bool {
	_, ok := ParseSecretRef(raw)
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
	ref, ok := ParseSecretRef(raw)
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
