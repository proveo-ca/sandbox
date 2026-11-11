module github.com/proveo-ca/proveo

go 1.26.7

// The project's Go pin. GOTOOLCHAIN=auto (the default) makes ANY go >= 1.21
// re-exec exactly this toolchain, so a contributor on g, mise, brew or CI all
// compile with the same one — no `compile: version ... does not match` skew.
// Bump here; `g install 1.26.10` just makes it the local default too.
toolchain go1.26.10

require (
	github.com/creack/pty v1.1.24
	github.com/gdamore/tcell/v2 v2.6.0
	github.com/google/go-cmp v0.7.0
	github.com/google/martian/v3 v3.3.3
	github.com/ktr0731/go-fuzzyfinder v0.9.0
	github.com/mattn/go-runewidth v0.0.30
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.9
	golang.org/x/sys v0.47.0
	golang.org/x/term v0.44.0
	golang.org/x/text v0.24.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	charm.land/bubbles/v2 v2.2.1 // indirect
	charm.land/bubbletea/v2 v2.1.1 // indirect
	charm.land/lipgloss/v2 v2.1.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20261008173134-6b8d4baf91b4 // indirect
	github.com/charmbracelet/x/ansi v0.11.9 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/ktr0731/go-ansisgr v0.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/nsf/termbox-go v1.1.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v1.0.0 // indirect
	golang.org/x/net v0.6.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
)
