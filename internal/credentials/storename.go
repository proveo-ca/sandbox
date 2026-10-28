// SPEC: _spec/_plans/init-credential-provisioning.puml, _spec/cmd/proveo/init-credential-wireframe.puml, _spec/internal/sbx/credential-resolution.puml
package credentials

import (
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/provider"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// StoreKind is which of the two things an entry holds.
type StoreKind int

const (
	StoreAPIKey StoreKind = iota
	StoreSubscription
	StoreUninjectable
)

// StoreName is the service sbx authenticates by for one credential variable; a plan token's kind is StoreSubscription, which no store entry carries.
// SPEC: _spec/internal/sbx/credential-resolution.puml (R2, R4)
func StoreName(envVar string) (service string, kind StoreKind) {
	envVar = strings.TrimSpace(envVar)
	if envVar == "" {
		return "", StoreUninjectable
	}
	service, injectable := providerOfVar(envVar)
	switch {
	case service == "":
		return "", StoreUninjectable
	case !injectable:
		return service, StoreUninjectable
	case isPlanToken(envVar):
		return service, StoreSubscription
	}
	return service, StoreAPIKey
}

// providerOfVar resolves an env var to its registry provider.
func providerOfVar(envVar string) (service string, injectable bool) {
	for _, name := range provider.Names() {
		e, ok := provider.Lookup(name)
		if !ok {
			continue
		}
		for _, v := range e.Detect {
			if !strings.EqualFold(v, envVar) {
				continue
			}
			return name, len(e.Hosts) > 0 && len(e.Auth) > 0
		}
	}
	return "", false
}

func isPlanToken(envVar string) bool { return !strings.HasSuffix(strings.ToUpper(envVar), "_API_KEY") }

// StaleStoreEntries returns the `sbx secret rm` argv for each entry sbx never reads: a service named
// for an env var or a def, and a custom secret on a registry provider's variable.
// SPEC: _spec/internal/sbx/credential-resolution.puml (P0 M2, R2)
func StaleStoreEntries(services []string, custom []sbx.CustomSecret, defs []string) [][]string {
	var out [][]string
	for _, name := range services {
		if _, isProvider := provider.Lookup(name); isProvider {
			continue
		}
		svc, _ := providerOfVar(name)
		if svc != "" || slices.Contains(defs, name) || strings.HasSuffix(name, "-sub") {
			out = append(out, sbx.RemoveSecretArgs(name))
		}
	}
	for _, c := range custom {
		if svc, injectable := providerOfVar(c.Env); svc != "" && injectable {
			out = append(out, sbx.RemoveCustomArgs(c.Placeholder))
		}
	}
	return out
}
