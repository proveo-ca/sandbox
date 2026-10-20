// SPEC: _spec/internal/sbx/kit-domain-form.puml
package sbx

import (
	"sort"
	"strings"
)

// DomainPatterns translates ONE stored provider host into the patterns sbx
// actually enforces.
func DomainPatterns(host string) []string {
	h := strings.TrimSpace(host)
	if h == "" {
		return nil
	}
	// Already sbx's own grammar (`*.foo`, or the allow-all `**`): nothing to
	// translate, and translating it again would corrupt it.
	if strings.HasPrefix(h, "*") {
		return []string{h}
	}
	if !strings.HasPrefix(h, ".") {
		return []string{h}
	}
	// A port suffix rides along on both halves for free — the dot is a prefix
	// and everything after the labels is carried, not parsed.
	bare := strings.TrimLeft(h, ".")
	if bare == "" {
		return nil
	}
	return []string{bare, "*." + bare}
}

// AllowPatterns is DomainPatterns over a list: deduplicated and sorted, which
// is the form `permissions.network.allow` is written in.
func AllowPatterns(hosts []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(hosts)*2)
	for _, h := range hosts {
		for _, p := range DomainPatterns(h) {
			if seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
