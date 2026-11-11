// SPEC: _spec/internal/ptyproxy/pty-ownership.puml, _spec/internal/choiceui/tui-stack.puml
//go:build !windows

package ptyproxy

import (
	"context"
	"io"
	"os"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// overlayOut is the suspended terminal as a Program's output: a bare LF leaves as CR LF.
type overlayOut struct{ f *os.File }

func (o overlayOut) Read(p []byte) (int, error) { return o.f.Read(p) }
func (o overlayOut) Close() error               { return nil }
func (o overlayOut) Fd() uintptr                { return o.f.Fd() }

func (o overlayOut) Write(p []byte) (int, error) {
	if _, err := o.f.Write(crlf(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// crlf puts a CR before each LF that has none.
func crlf(p []byte) []byte {
	out := make([]byte, 0, len(p)+16)
	var prev byte
	for _, b := range p {
		if b == '\n' && prev != '\r' {
			out = append(out, '\r')
		}
		out = append(out, b)
		prev = b
	}
	return out
}

// overlayModel puts every frame of inner on the alternate screen and leaves bracketed paste alone.
type overlayModel struct{ inner tea.Model }

func (m overlayModel) Init() tea.Cmd { return m.inner.Init() }

func (m overlayModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	inner, cmd := m.inner.Update(msg)
	return overlayModel{inner}, cmd
}

func (m overlayModel) View() tea.View {
	v := m.inner.View()
	v.AltScreen = true
	v.DisableBracketedPasteMode = true
	return v
}

// OverlayProgram runs model as a Bubble Tea Program on the suspended terminal and
// returns the model as it ended. in is the reader Overlay handed to draw.
func (p *Proxy) OverlayProgram(in io.Reader, model tea.Model) (tea.Model, error) {
	return overlayProgram(in, overlayOut{p.Out}, model)
}

// overlayProgram is OverlayProgram over any output; opts reach the Program after proveo's own.
func overlayProgram(in io.Reader, out io.Writer, model tea.Model, opts ...tea.ProgramOption) (tea.Model, error) {
	var quitting atomic.Bool
	halt := func() {
		quitting.Store(true)
		if h, ok := in.(interface{ halt() }); ok {
			h.halt()
		}
	}
	base := []tea.ProgramOption{
		tea.WithInput(nil),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(tea.QuitMsg); ok {
				halt()
			}
			return msg
		}),
	}
	prog := tea.NewProgram(overlayModel{model}, append(base, opts...)...)

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan uv.Event, 16)
	streamed := make(chan struct{})
	go func() {
		_ = uv.NewTerminalReader(in, os.Getenv("TERM")).StreamEvents(ctx, events)
		close(events)
		if !quitting.Load() {
			prog.Kill()
		}
	}()
	go func() {
		defer close(streamed)
		for ev := range events {
			switch e := ev.(type) {
			case uv.KeyPressEvent:
				prog.Send(tea.KeyPressMsg(e))
			case uv.PasteEvent:
				prog.Send(tea.PasteMsg(e))
			}
		}
	}()

	final, err := prog.Run()
	halt()
	cancel()
	<-streamed
	if m, ok := final.(overlayModel); ok {
		final = m.inner
	}
	return final, err
}
