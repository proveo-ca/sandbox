//go:build windows

package ptyproxy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/gdamore/tcell/v2"
)

// ErrNotRunning is returned when an overlay is requested before Run.
var ErrNotRunning = errors.New("ptyproxy: no child running")

// Proxy is a Windows stub: review-gate PTY overlays need Unix signals and a
// real PTY, which this GOOS does not provide.
type Proxy struct {
	In  *os.File
	Out *os.File

	Tap           func(b []byte, forwarded bool)
	OutTap        func(b []byte)
	DisableFilter bool
	DropReports   bool
}

func New(in, out *os.File) *Proxy { return &Proxy{In: in, Out: out} }

func Usable(in, out *os.File) bool { return false }

// Run reports that PTY proxying is unavailable on this GOOS.
func (p *Proxy) Run(cmd *exec.Cmd) error {
	return fmt.Errorf("ptyproxy: not supported on windows")
}

// Restore is a no-op on Windows.
func (p *Proxy) Restore() {}

// Overlay returns ErrNotRunning — there is never a live PTY child on Windows.
func (p *Proxy) Overlay(draw func(in io.Reader, out io.Writer) error) error {
	return ErrNotRunning
}

// OverlayScreen is unavailable on Windows (no PTY / terminfo overlay path).
func (p *Proxy) OverlayScreen(in io.Reader) (tcell.Screen, error) {
	return nil, fmt.Errorf("ptyproxy: OverlayScreen not supported on windows")
}
