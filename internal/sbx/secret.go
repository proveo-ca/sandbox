package sbx

func SecretSetArgs(name string) []string {
	return []string{"secret", "set", "--force", name}
}

// secretSet is overridable in tests.
func SecretSet(name, value string) error {
	return sh.SecretSet(name, value)
}

// KitSchemaVersion is the kit-spec version this package writes.
//
// v2 is the current one, and the version is not cosmetic — it selects the SHAPE
// of two blocks. Under v1 the allowlist is "network.allowedDomains" (which v0.39
// still accepts, with a deprecation warning) and "sandbox.entrypoint" is a
// mapping; under v2 the allowlist moves to "permissions.network.allow" and the
// entrypoint becomes a plain list. Mixing them fails validation rather than
// degrading, so the version and the field names travel together.
