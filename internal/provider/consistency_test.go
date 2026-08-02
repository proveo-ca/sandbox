package provider

import (
	"strings"
	"testing"
)

// Every provider a bare model id resolves to must exist in the registry. A prefix
// pointing at an unregistered name resolves the broker to a provider that cannot
// be looked up, so the pin silently evaporates.
func TestBareIDPrefixesResolveToRegisteredProviders(t *testing.T) {
	t.Parallel()
	for _, e := range bareIDPrefixes {
		if _, ok := Lookup(e.provider); !ok {
			t.Errorf("bare id %q resolves to %q, which is not in the registry", e.prefix, e.provider)
		}
	}
}

// Every detect key must belong to a registry entry that can actually reach its
// provider: a key with no ACL is advertised support the egress layer cannot honour.
func TestEveryRegisteredProviderIsReachable(t *testing.T) {
	t.Parallel()
	// bedrock/vertex authenticate through cloud SDKs and deliberately carry no
	// blanket ACL (allowlisting all of .amazonaws.com would defeat the point).
	exempt := map[string]bool{"bedrock": true, "vertex": true}
	for _, name := range Names() {
		if exempt[name] {
			continue
		}
		e, _ := Lookup(name)
		if strings.TrimSpace(e.ACL) == "" {
			t.Errorf("provider %q has no ACL: it can be detected and pinned but never reached", name)
		}
		if len(e.Detect) == 0 {
			t.Errorf("provider %q declares no detect key, so it can never be selected", name)
		}
	}
}
