// SPEC: _spec/_plans/init-credential-provisioning.puml, _spec/cmd/proveo/init-credential-wireframe.puml
package credentials

import (
	"strings"

	"github.com/proveo-ca/proveo/internal/provider"
)

// StoreKind is which of the two things an entry holds.
type StoreKind int

const (
	StoreAPIKey StoreKind = iota
	StoreSubscription
	StoreUninjectable
)

// StoreName is the name sbx authenticates by for one credential variable.
// SPEC: _spec/_plans/host-held-provider-keys.puml
func StoreName(envVar, def string) (name string, kind StoreKind) {
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
		return subscriptionName(service, def), StoreSubscription
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

func subscriptionName(service, def string) string {
	def = strings.TrimSpace(def)
	if def != "" && def != service {
		return def
	}
	if def != "" {
		return def + "-sub"
	}
	return service + "-sub"
}
