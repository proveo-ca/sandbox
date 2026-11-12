// SPEC: _spec/internal/ptyproxy/pty-ownership.puml, _spec/internal/choiceui/tui-stack.puml
//go:build !windows

package ptyproxy

import (
	"io"
	"os"
	"time"

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

// fedKey is one key proveo decoded off the hand-off, on its way to the model.
type fedKey struct{ key tea.KeyPressMsg }

// overlayModel puts every frame of inner on the alternate screen, leaves bracketed
// paste alone, and reports after each fed key whether inner has its answer.
type overlayModel struct {
	inner tea.Model
	acked chan<- bool
}

func (m overlayModel) Init() tea.Cmd { return m.inner.Init() }

func (m overlayModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	fed, isKey := msg.(fedKey)
	if !isKey {
		inner, cmd := m.inner.Update(msg)
		return overlayModel{inner, m.acked}, cmd
	}
	inner, cmd := m.inner.Update(fed.key)
	done := false
	if d, ok := inner.(interface{ Done() bool }); ok {
		done = d.Done()
	}
	m.acked <- done
	return overlayModel{inner, m.acked}, cmd
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

// escWait is how long a lone ESC, or the start of a sequence, waits for its rest.
const escWait = 50 * time.Millisecond

// overlayProgram is OverlayProgram over any output; opts reach the Program after proveo's own.
func overlayProgram(in io.Reader, out io.Writer, model tea.Model, opts ...tea.ProgramOption) (tea.Model, error) {
	acked := make(chan bool, 1)
	ended := make(chan struct{})
	base := []tea.ProgramOption{tea.WithInput(nil), tea.WithOutput(out), tea.WithoutSignalHandler()}
	prog := tea.NewProgram(overlayModel{model, acked}, append(base, opts...)...)

	fed := make(chan struct{})
	go func() {
		defer close(fed)
		feedKeys(in, ended, func(key tea.KeyPressMsg) (done bool) {
			prog.Send(fedKey{key})
			select {
			case done = <-acked:
			case <-ended:
				done = true
			}
			return done
		}, prog.Kill)
	}()

	final, err := prog.Run()
	close(ended)
	if h, ok := in.(interface{ halt() }); ok {
		h.halt()
	}
	<-fed
	if m, ok := final.(overlayModel); ok {
		final = m.inner
	}
	return final, err
}

// feedKeys decodes in one key at a time and gives each to deliver. It stops when
// deliver reports the model has its answer, when ended closes, or when in ends; in
// that last case it calls inputEnded. Bytes it read and did not deliver go back to in.
func feedKeys(in io.Reader, ended <-chan struct{}, deliver func(tea.KeyPressMsg) (done bool), inputEnded func()) {
	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		for {
			b := make([]byte, 4096)
			n, err := in.Read(b)
			if n > 0 {
				select {
				case chunks <- b[:n]:
				case <-ended:
					giveBack(in, b[:n])
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var dec uv.EventDecoder
	var buf []byte
	pasting := false
	stop := func() {
		if h, ok := in.(interface{ halt() }); ok {
			h.halt()
		}
		for b := range chunks {
			buf = append(buf, b...)
		}
		giveBack(in, buf)
	}
	for {
		expired := false
		for len(buf) > 0 {
			n, ev := dec.Decode(buf)
			if n == 0 {
				break
			}
			_, unknown := ev.(uv.UnknownEvent)
			if !expired && (unknown || (buf[0] == 0x1b && n <= 2)) {
				select {
				case b, ok := <-chunks:
					if !ok {
						expired = true
						continue
					}
					buf = append(buf, b...)
				case <-time.After(escWait):
					expired = true
				case <-ended:
					stop()
					return
				}
				continue
			}
			buf, expired = buf[n:], false
			switch e := ev.(type) {
			case uv.PasteStartEvent:
				pasting = true
			case uv.PasteEndEvent:
				pasting = false
			case uv.KeyPressEvent:
				if !pasting && deliver(tea.KeyPressMsg(e)) {
					stop()
					return
				}
			}
		}
		select {
		case b, ok := <-chunks:
			if !ok {
				inputEnded()
				return
			}
			buf = append(buf, b...)
		case <-ended:
			stop()
			return
		}
	}
}

// giveBack returns bytes the overlay read and did not use to the hand-off, ahead of what it still holds.
func giveBack(in io.Reader, b []byte) {
	if g, ok := in.(interface{ giveBack([]byte) }); ok && len(b) > 0 {
		g.giveBack(b)
	}
}
