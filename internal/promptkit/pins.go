// SPEC: _spec/internal/promptkit/prompt-kit.puml
// Package promptkit installs what the shell-prompt segment needs: starship, a
// Nerd Font when the host has none, the shell's init line, and one marked block
// in starship's config.
package promptkit

import "fmt"

// StarshipVersion is the starship release proveo installs.
const StarshipVersion = "1.27.0"

// pinned is one release asset and the sha256 its publisher lists for it.
type pinned struct{ Asset, SHA256 string }

// starshipAssets maps GOOS/GOARCH to the release archive for it.
var starshipAssets = map[string]pinned{
	"darwin/arm64":  {"starship-aarch64-apple-darwin.tar.gz", "5362da8cb2928f98f3ce0ffda697cf0f8de70081db000dae316d6d3a404f6cf0"},
	"darwin/amd64":  {"starship-x86_64-apple-darwin.tar.gz", "344e4d38787e0aa86dd522ae5462e6fb6da1227691f81a79ffed480a4563ccb3"},
	"linux/arm64":   {"starship-aarch64-unknown-linux-musl.tar.gz", "341f5cbf24699fa0b27ec88965ad25f862ea09e15d35a543fd6ffdff1d2aa00d"},
	"linux/amd64":   {"starship-x86_64-unknown-linux-musl.tar.gz", "10c8eb4cb3b761d9bf59ea828444c2dab36c900b7d69a69e08814b286969df89"},
	"windows/arm64": {"starship-aarch64-pc-windows-msvc.zip", "1822b6cf1af28071b056976d3f445359d61df324918ffe76c6d0f851eef9366f"},
	"windows/amd64": {"starship-x86_64-pc-windows-msvc.zip", "7764f861805376af2812dd78c84fadc16b714cf3f346baed9b597bfda8484403"},
}

// StarshipAsset is the pinned archive for a host; ok is false when starship publishes none.
func StarshipAsset(goos, goarch string) (asset, sha256, url string, ok bool) {
	p, ok := starshipAssets[goos+"/"+goarch]
	if !ok {
		return "", "", "", false
	}
	return p.Asset, p.SHA256, fmt.Sprintf("https://github.com/starship/starship/releases/download/v%s/%s", StarshipVersion, p.Asset), true
}

// The Nerd Font proveo installs when the host has none.
const (
	FontRelease = "v3.5.1"
	FontAsset   = "FiraCode.zip"
	FontSHA256  = "239395baf60c89b2eaf4862b6b09db0ef95605cd3e8eef51c00345822a81a665"
	FontFamily  = "FiraCode Nerd Font"
)

// FontURL is the pinned font archive.
func FontURL() string {
	return "https://github.com/ryanoasis/nerd-fonts/releases/download/" + FontRelease + "/" + FontAsset
}
