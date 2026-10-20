// Package proveo embeds the harness manifests into the Go binaries so the CLI
// (cmd/proveo) is self-contained and works without the defs/ tree on disk.
package proveo

import "embed"

// Manifests holds every defs/<name>/harness.manifest (the harness registry).
//
//go:embed defs/*/harness.manifest
var Manifests embed.FS
