// SPEC: _spec/internal/sbx/credential-resolution.puml (R4, R8)
package credentials

import "github.com/proveo-ca/proveo/internal/manifest"

// NeedsSandboxLogin reports a subscription harness on sbx with no credential anywhere: no file login, nothing stored for its services, no provider key.
func NeedsSandboxLogin(man manifest.Manifest, sbxBackend, fileLogin bool, stored []string, lookup func(string) string) bool {
	if !sbxBackend || !man.Subscription || fileLogin {
		return false
	}
	if len(StoreHolds(man, stored)) > 0 {
		return false
	}
	return len(ProviderKeyVars(man, lookup)) == 0
}
