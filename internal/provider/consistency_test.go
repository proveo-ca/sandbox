package provider

import (
	"testing"
)

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
	exempt := map[string]bool{"bedrock": true, "vertex": true}
	for _, name := range Names() {
		if exempt[name] {
			continue
		}
		e, _ := Lookup(name)
		if len(e.Hosts) == 0 {
			t.Errorf("provider %q has no hosts: nothing can name where it is reached", name)
		}
		if len(e.Detect) == 0 {
			t.Errorf("provider %q declares no detect key, so it can never be selected", name)
		}
	}
}
