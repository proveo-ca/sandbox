// SPEC: _spec/internal/secretref/secret-references.puml
package runner

// Mount is a bind mount.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}
