//go:build windows

package ptyproxy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	tea "charm.land/bubbletea/v2"
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

// OverlayProgram returns ErrNotRunning — there is never a live PTY child on Windows.
func (p *Proxy) OverlayProgram(io.Reader, tea.Model) (tea.Model, error) {
	return nil, ErrNotRunning
}

// DiscardInput is a no-op on Windows.
func DiscardInput(*os.File) {}

// HandleCtrlC is a no-op on Windows, which has no PTY overlay.
func (p *Proxy) HandleCtrlC(func(io.Reader, io.Writer) CtrlCAction, func()) {}
