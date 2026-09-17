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
//
// StoreUninjectable is not "sbx cannot hold it" — `sbx secret set-custom` takes
// any host and variable, and proveo already uses it for every provider sbx has
// no built-in service for. It means the proxy cannot ATTACH it: an AWS key is
// signed into each request and GOOGLE_APPLICATION_CREDENTIALS names a file the
// client reads, so a placeholder in the variable is useless to the agent.
// azure is in that set only because the registry gives it no Hosts or Auth; its
// key IS a static `api-key` header on known domains, so filling those in would
// move it. Injectability is answered per ENTRY, so bedrock's two variables share
// an answer: giving that entry hosts to rescue AWS_BEARER_TOKEN_BEDROCK would
// mark the SigV4 signing key beside it brokerable too, and no substitution
// produces a signature. SPEC: _spec/_plans/host-held-provider-keys.puml
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

// providerOfVar resolves through the registry rather than trimming _API_KEY off
// the variable: six ids are deliberate and do not follow the pattern.
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
