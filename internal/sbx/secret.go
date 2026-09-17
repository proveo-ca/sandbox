package sbx

import "strings"

func SecretSetArgs(name string) []string {
	return []string{"secret", "set", "--force", name}
}

func SecretSet(name, value string) error {
	return sh.SecretSet(name, value)
}

// BuiltinServices are the service ids `sbx secret set` has built-in knowledge
// for — it knows each one's hosts and auth header, so a value stored under one
// of these names is enough for sbx's proxy to inject it.
//
// Measured from `sbx secret set --help` on v0.42.1. A name OUTSIDE this list is
// still storable (the store already holds several), but sbx has nowhere to put
// the value: it knows neither the host to match nor the header to write. That
// is what `sbx secret set-custom` supplies.
var BuiltinServices = []string{
	"anthropic", "copilot", "cursor", "devin", "droid", "github", "google",
	"groq", "mistral", "nebius", "openai", "openrouter", "xai",
}

func IsBuiltinService(name string) bool {
	for _, s := range BuiltinServices {
		if s == name {
			return true
		}
	}
	return false
}

// SecretSetCustomArgs stores a credential for a service sbx does not know,
// naming the hosts to match and the variable the sandbox sees.
//
// The VALUE is never an argument. `--value` and `--token` both exist and both
// say "less secure: visible in shell history"; argv is also visible to anyone
// running ps, and the suite asserts no provider key appears there. The value
// goes on stdin, the way SecretSetArgs already passes one.
func SecretSetCustomArgs(hosts []string, envVar string) []string {
	args := []string{"secret", "set-custom"}
	for _, h := range hosts {
		args = append(args, "--host", h)
	}
	return append(args, "--env", envVar)
}

// CustomSecretPlaceholder finds the placeholder of the custom secret already
// bound to envVar, if there is one.
//
// `sbx secret set-custom` has no --force and REFUSES a variable it already
// holds ("custom secret env … already exists in scope"), so a second run of the
// same def died before its shell until the old entry was removed first. The
// placeholder is the only handle `sbx secret rm` takes for a custom secret.
func CustomSecretPlaceholder(envVar string) string {
	out, err := sh.SecretList()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		var hasVar bool
		var placeholder string
		for _, f := range fields {
			if f == envVar {
				hasVar = true
			}
			if strings.HasPrefix(f, customPlaceholderPrefix) {
				placeholder = f
			}
		}
		if hasVar && placeholder != "" {
			return placeholder
		}
	}
	return ""
}

const customPlaceholderPrefix = "sbx-cs-"

func SecretRemoveCustomArgs(placeholder string) []string {
	return []string{"secret", "rm", "--placeholder", placeholder, "-f"}
}

// SecretSetCustom replaces whatever custom secret holds envVar, so the value
// this run resolved is the one that answers — the same semantics `secret set
// --force` already gives a service secret.
func SecretSetCustom(hosts []string, envVar, value string) error {
	if p := CustomSecretPlaceholder(envVar); p != "" {
		if err := sh.SecretRemoveCustom(p); err != nil {
			return err
		}
	}
	return sh.SecretSetCustom(hosts, envVar, value)
}
