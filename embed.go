// Package proveo embeds the harness manifests and the Squid config into the Go
// binaries so the CLI (cmd/proveo) is self-contained and works without the defs/
// tree on disk. The files under defs/ remain the source of truth; these embeds
// are compiled from them at build time.
package proveo

import "embed"

// Manifests holds every defs/<name>/harness.manifest (the harness registry).
//
//go:embed defs/*/harness.manifest
var Manifests embed.FS

// ModelBridges holds every defs/bridges/<harness>.tsv: how the shared role vars
// become the env vars a harness actually reads. The shell applies the same tables
// at container start; embedding lets the host resolve slots for the prompt header
// before any container exists.
//
//go:embed defs/bridges/*.tsv
var ModelBridges embed.FS

// SquidConfig holds the enforcement-proxy config staged into each session.
//
//go:embed defs/sidecars/squid-proxy/squid.conf
//go:embed defs/sidecars/squid-proxy/firehol-blocked-nets.conf
//go:embed defs/sidecars/squid-proxy/firehol-ipset.conf
//go:embed defs/sidecars/squid-proxy/provider-allow.conf
var SquidConfig embed.FS
