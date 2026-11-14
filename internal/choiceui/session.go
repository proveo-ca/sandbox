// SPEC: _spec/internal/choiceui/session.puml
package choiceui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/proveo-ca/proveo/internal/ui"
)

// ErrTerminalHeld is what a second program on the terminal gets while a Session holds it.
var ErrTerminalHeld = errors.New("choiceui: a session holds the terminal")

// ErrSessionEnded is what a call gets once its Session's Program has stopped.
var ErrSessionEnded = errors.New("choiceui: the session ended")

// terminal records whether a Session holds the operator's terminal.
var terminal struct {
	mu   sync.Mutex
	held bool
}

// claimTerminal marks the terminal held, or refuses while a Session holds it.
func claimTerminal() error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.held {
		return ErrTerminalHeld
	}
	terminal.held = true
	return nil
}

// freeTerminal ends a claim.
func freeTerminal() {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.held = false
}

// terminalHeld reports whether a Session holds the terminal.
func terminalHeld() bool {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.held
}

// runningShowsClock is how long a running row waits before its outcome carries the time it took.
const runningShowsClock = 2 * time.Second

// Session holds the terminal for a walk of forms: one Program on one alternate screen, from
// OpenSession to Close. A form Ask was given belongs to the Session until the next Ask.
type Session struct {
	prog    *tea.Program
	ctx     context.Context
	cancel  context.CancelFunc
	ended   chan struct{} // closed once the Program has returned
	err     error         // what the Program returned; read after ended closes
	release func() []byte
	done    func()
	once    sync.Once
}

// OpenSession takes the operator's terminal for a walk: it holds ui.Default and starts the
// Program. It refuses with ErrTerminalHeld while another Session is open.
func OpenSession() (*Session, error) {
	return startSession(func() (io.Reader, io.Writer, func(), error) {
		in, out, done, err := ttyFiles()
		return in, out, done, err
	})
}

// startSession is OpenSession on the terminal open gives; opts reach the Program after the session's own.
func startSession(open func() (io.Reader, io.Writer, func(), error), opts ...tea.ProgramOption) (*Session, error) {
	if err := claimTerminal(); err != nil {
		return nil, err
	}
	in, out, done, err := open()
	if err != nil {
		freeTerminal()
		return nil, fmt.Errorf("session: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{ctx: ctx, cancel: cancel, ended: make(chan struct{}), done: done}
	model := newSessionModel(out, cancel)
	base := []tea.ProgramOption{tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()}
	s.prog = tea.NewProgram(model, append(base, opts...)...)
	s.release = ui.Hold()
	go func() {
		_, err := s.prog.Run()
		s.err = err
		close(s.ended)
	}()
	return s, nil
}

// sessionMsg is a message the Session's driver sends its Program.
type sessionMsg interface{ fromDriver() }

type (
	askMsg struct {
		f     *Form
		reply chan<- Outcome
	}
	planMsg   struct{ labels []string }
	statusMsg struct {
		label, outcome string
		mark           Mark
	}
	handMsg struct {
		command string
		parts   []string
		run     func() error
		outcome func(error) (Mark, string)
		sigs    <-chan os.Signal
		reply   chan<- error
	}
	dialogMsg struct {
		d     dialog
		reply chan<- int
	}
	peekMsg   struct{ reply chan<- string }
	resumeMsg struct{ f *Form }
	editMsg   struct{ edit func(*Form) }
)

func (askMsg) fromDriver()    {}
func (planMsg) fromDriver()   {}
func (statusMsg) fromDriver() {}
func (handMsg) fromDriver()   {}
func (dialogMsg) fromDriver() {}
func (peekMsg) fromDriver()   {}
func (resumeMsg) fromDriver() {}
func (editMsg) fromDriver()   {}

// send gives the Program m, and reports false once the Program has stopped.
func (s *Session) send(m sessionMsg) bool {
	select {
	case <-s.ended:
		return false
	default:
	}
	s.prog.Send(m)
	return true
}

// endErr is a call's error once the Program has stopped.
func (s *Session) endErr() error {
	if s.err != nil {
		return fmt.Errorf("%w: %w", ErrSessionEnded, s.err)
	}
	return ErrSessionEnded
}

// await is the reply on c, or the Session's end.
func await[T any](s *Session, c <-chan T) (T, error) {
	select {
	case v := <-c:
		return v, nil
	case <-s.ended:
		var zero T
		return zero, s.endErr()
	}
}

// Ask puts f up at rest and returns how the operator left it. Accepted leaves f up, running:
// until the next Ask only ctrl+c is taken, and it cancels Context.
func (s *Session) Ask(f *Form) (Outcome, error) {
	reply := make(chan Outcome, 1)
	if !s.send(askMsg{f: f, reply: reply}) {
		return Cancelled, s.endErr()
	}
	return await(s, reply)
}

// Plan names the rows the run Enter started will run, in order: they turn "·", every other
// answer row turns "–" with its answer, and the key line counts "running i of m".
func (s *Session) Plan(labels []string) { s.send(planMsg{labels: slices.Clone(labels)}) }

// Status puts mark and outcome on the row labelled label: the running row, else the first that
// waits in the plan, else the first. MarkRunning makes it the running row: the key line names it,
// and after two seconds its outcome carries the time it has taken. A planned label no row carries
// still moves the key line on.
func (s *Session) Status(label string, m Mark, outcome string) {
	s.send(statusMsg{label: label, mark: m, outcome: outcome})
}

// Resume puts f back up as Accepted left it, running: a page a sub-page stood over returns to its run.
func (s *Session) Resume(f *Form) { s.send(resumeMsg{f: f}) }

// Edit runs edit on the form up now, between two draws: the driver replaces rows a step redrew.
func (s *Session) Edit(edit func(*Form)) { s.send(editMsg{edit: edit}) }

// Hand gives the terminal to run: the card leaves the alternate screen, a blank row and the frame
// line "── <command> ── <parts> ──" print, run runs, then a blank row and the running row's outcome
// print and the card comes back with that outcome on the row. It returns run's error as run gave it;
// a SIGINT from the call until the card is back cancels Context.
func (s *Session) Hand(command string, parts []string, run func() error, outcome func(error) (Mark, string)) error {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)
	reply := make(chan error, 1)
	if !s.send(handMsg{command: command, parts: slices.Clone(parts), run: run, outcome: outcome, sigs: sigs, reply: reply}) {
		return s.endErr()
	}
	ran, err := await(s, reply)
	if err != nil {
		return err
	}
	return ran
}

// Dialog is a question a Session puts over its dimmed card.
type Dialog struct{ d dialog }

// DrawsGlyphsDialog is AskDrawsGlyphs' question for a Session: answer 1 is "Two icons".
func DrawsGlyphsDialog() Dialog { return Dialog{drawsGlyphsDialog()} }

// Dialog puts d over the card and returns the button the operator pressed. Ctrl+C presses d's
// ctrl+c button and cancels Context.
func (s *Session) Dialog(d Dialog) (int, error) {
	reply := make(chan int, 1)
	if !s.send(dialogMsg{d: d.d, reply: reply}) {
		return d.d.safe, s.endErr()
	}
	return await(s, reply)
}

// Context is cancelled by ctrl+c before the first page or once a page is accepted, by a Ctrl+C
// during a hand-off, and by Close.
func (s *Session) Context() context.Context { return s.ctx }

// peek is the screen as drawn now, as plain text.
func (s *Session) peek() (string, error) {
	reply := make(chan string, 1)
	if !s.send(peekMsg{reply: reply}) {
		return "", s.endErr()
	}
	return await(s, reply)
}

// Close ends the Program, which leaves the alternate screen; it then gives ui.Default back,
// prints what the hold kept, and frees the terminal. It returns the Program's error.
func (s *Session) Close() error {
	s.once.Do(func() {
		s.prog.Quit()
		<-s.ended
		s.cancel()
		ui.Replay(s.release())
		s.done()
		freeTerminal()
	})
	return s.err
}

// runningRow is the row a run is on: its label, its outcome as given, and when it started.
type runningRow struct {
	label, outcome string
	since          time.Time
}

// sessionModel is the one model a Session's Program runs: the page asked now, its run, a dialog
// over it, and the hand-off that lends the terminal out.
type sessionModel struct {
	form    formModel // the page; form.f is nil before the first Ask
	w, h    int
	tick    int
	asked   chan<- Outcome // the driver waiting on the page; nil once it answered
	ask     *dialog
	answer  chan<- int
	running runningRow
	stop    context.CancelFunc
	stopped bool      // ctrl+c or a SIGINT stopped the walk; a plan after it starts stopping
	out     io.Writer // the terminal a hand-off prints its frame and outcome on
	exec    func(tea.ExecCommand, tea.ExecCallback) tea.Cmd
	now     func() time.Time
}

func newSessionModel(out io.Writer, stop context.CancelFunc) sessionModel {
	return sessionModel{out: out, stop: stop, exec: tea.Exec, now: time.Now, tick: 1}
}

func (m sessionModel) Init() tea.Cmd { return animTick() }

func (m sessionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.ask != nil {
		switch msg.(type) {
		case tea.KeyPressMsg, dialogTimeout:
			next, _ := m.ask.Update(msg)
			d, _ := next.(dialog)
			if d.answer < 0 {
				m.ask = &d
				return m, nil
			}
			m.answer <- d.answer
			m.ask, m.answer = nil, nil
			if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+c" {
				m.stopRun()
			}
			return m, nil
		}
	}
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = v.Width, v.Height
		next, _ := m.form.Update(msg)
		m.form, _ = next.(formModel)
		if m.ask != nil {
			d := *m.ask
			d.w, d.h = v.Width, v.Height
			m.ask = &d
		}
	case formTick:
		m.tick++
		m.form.tick = m.tick
		m.clock()
		return m, animTick()
	case tea.KeyPressMsg:
		if m.asked == nil {
			if v.String() == "ctrl+c" && (m.form.f == nil || m.form.out == Accepted) {
				m.stopRun()
			}
			return m, nil
		}
		next, _ := m.form.Update(msg)
		m.form, _ = next.(formModel)
		if m.form.ended {
			m.asked <- m.form.out
			m.asked, m.form.ended = nil, false
		}
	case askMsg:
		m.form = formModel{f: v.f, cursor: v.f.startCursor(), tick: m.tick, w: m.w, h: m.h}
		m.asked, m.running = v.reply, runningRow{}
	case resumeMsg:
		m.form = formModel{f: v.f, cursor: -1, tick: m.tick, w: m.w, h: m.h, out: Accepted}
		m.asked = nil
		if p := v.f.Progress; p != nil && m.stopped {
			p.Stopping = true
		}
	case editMsg:
		if m.form.f != nil {
			v.edit(m.form.f)
		}
	case planMsg:
		m.plan(v.labels)
	case statusMsg:
		m.status(v.label, v.mark, v.outcome)
	case handMsg:
		return m, m.hand(v)
	case handDone:
		v.reply <- m.handed(v)
	case dialogMsg:
		d := v.d
		d.w, d.h, d.sel, d.answer, d.stops = m.w, m.h, d.safe, -1, true
		if m.form.f != nil {
			d.glyphs = m.form.f.Glyphs
		}
		m.ask, m.answer = &d, v.reply
		return m, d.Init()
	case peekMsg:
		v.reply <- sgrRun.ReplaceAllString(m.View().Content, "")
	}
	return m, nil
}

func (m sessionModel) View() tea.View {
	var v tea.View
	switch f := m.form.f; {
	case f == nil && m.ask != nil:
		v = m.ask.View()
	case f == nil:
		v = tea.NewView("")
	default:
		s := newScreen(m.w, m.h)
		f.draw(s, m.form.cursor, m.tick)
		switch {
		case m.form.quit.up() && !m.form.quit.overlay(s, f.at):
			v = m.form.quit.view()
		case m.ask != nil && !overlayDialog(s, f.at, *m.ask):
			v = m.ask.View()
		default:
			v = tea.NewView(s.view())
		}
	}
	v.AltScreen = true
	return v
}

// stopRun cancels the walk's context; on a running page the key line says it stops after its row.
func (m *sessionModel) stopRun() {
	m.stop()
	m.stopped = true
	if f := m.form.f; f != nil && f.Progress != nil {
		f.Progress.Stopping = true
	}
}

// plan starts the page's run: the planned rows wait as "·", every other answer row reads "–" and
// its answer, a row an answer opened reads its answer with no mark, and the key line counts the
// plan. labels name rows in row order, so a label two rows share stands for the next of them.
func (m *sessionModel) plan(labels []string) {
	f := m.form.f
	if f == nil {
		return
	}
	f.Progress = &Progress{Plan: labels, Stopping: m.stopped}
	next := 0
	for i := range f.Rows {
		r := &f.Rows[i]
		if r.Kind != RowAnswer || (len(r.Options) == 0 && !r.Field) {
			continue
		}
		r.Kind, r.Mark, r.Outcome = RowStatus, MarkLater, r.answerText()
		if r.When != nil {
			r.Mark = MarkNone
		}
		if at := slices.Index(labels[next:], r.Label); at >= 0 {
			r.Mark, next = MarkAhead, next+at+1
		}
	}
}

// answerText is what an answer row reads once it stops taking answers: its answer as its cell
// names it, and nothing for a field, whose value may be a secret.
func (r *Row) answerText() string {
	if r.Field || r.Selected < 0 || r.Selected >= len(r.Options) {
		return ""
	}
	return r.cellName(r.Options[r.Selected])
}

// status puts mark and outcome on the row labelled label; MarkRunning makes it the running row. A
// label in the plan no row carries moves the key line on alone.
func (m *sessionModel) status(label string, mark Mark, outcome string) {
	f := m.form.f
	if f == nil {
		return
	}
	if !f.setStatus(label, mark, outcome) {
		if p := f.Progress; p != nil && mark == MarkRunning {
			if i := slices.Index(p.Plan, label); i >= 0 {
				p.At = i
			}
		}
		return
	}
	switch {
	case mark == MarkRunning:
		m.running = runningRow{label: label, outcome: outcome, since: m.now()}
		if p := f.Progress; p != nil {
			if i := slices.Index(p.Plan, label); i >= 0 {
				p.At = i
			}
		}
	case m.running.label == label:
		m.running = runningRow{}
	}
}

// setStatus turns the row labelled label into a status row with mark and outcome: the running row
// of that label, else the first that waits in the plan, else the first; it reports false when no
// row carries label.
func (f *Form) setStatus(label string, mark Mark, outcome string) bool {
	at := -1
	for _, want := range []Mark{MarkRunning, MarkAhead, -1} {
		at = slices.IndexFunc(f.Rows, func(r Row) bool {
			return r.Label == label && (want < 0 || r.Kind == RowStatus && r.Mark == want)
		})
		if at >= 0 {
			break
		}
	}
	if at < 0 {
		return false
	}
	r := &f.Rows[at]
	if r.Kind == RowAnswer {
		r.Kind = RowStatus
	}
	r.Mark, r.Outcome = mark, outcome
	return true
}

// clock adds the time the running row has taken to its outcome, from two seconds on.
func (m *sessionModel) clock() {
	run, f := m.running, m.form.f
	if run.label == "" || f == nil {
		return
	}
	took := m.now().Sub(run.since)
	if took < runningShowsClock {
		return
	}
	for i := range f.Rows {
		if r := &f.Rows[i]; r.Label == run.label && r.Kind == RowStatus && r.Mark == MarkRunning {
			r.Outcome = run.outcome + " · " + took.Round(time.Second).String()
			return
		}
	}
}

// handDone is a hand-off back from Bubble Tea: what run did, and err when the terminal would not
// go or come back.
type handDone struct {
	x     *handExec
	err   error
	reply chan<- error
}

// hand lends the terminal to v.run through Bubble Tea's Exec, under the running row's label.
func (m *sessionModel) hand(v handMsg) tea.Cmd {
	label := ""
	if f := m.form.f; f != nil && f.Progress != nil {
		label = f.Progress.label()
	}
	tier := ui.GlyphTierFrom(os.Getenv)
	if f := m.form.f; f != nil {
		tier = f.Glyphs
	}
	x := &handExec{out: m.out, w: m.w, tier: tier, plain: !ui.Fancy(), form: m.form.f, command: v.command, parts: v.parts,
		label: label, run: v.run, outcome: v.outcome, sigs: v.sigs}
	return m.exec(x, func(err error) tea.Msg { return handDone{x: x, err: err, reply: v.reply} })
}

// handed puts the hand-off's outcome on its row and is what Hand returns: run's error, joined with
// the terminal's when it would not come back.
func (m *sessionModel) handed(d handDone) error {
	x := d.x
	if !x.ran {
		return fmt.Errorf("choiceui: lend the terminal: %w", d.err)
	}
	if x.label != "" {
		m.status(x.label, x.mark, x.text)
	}
	if len(x.sigs) > 0 {
		m.stopRun()
	}
	if d.err != nil {
		return errors.Join(x.err, fmt.Errorf("choiceui: take the terminal back: %w", d.err))
	}
	return x.err
}

// handExec is one hand-off as Bubble Tea's Exec runs it, between releasing the terminal and
// taking it back. The Set methods do nothing: run brings its own streams.
type handExec struct {
	out     io.Writer
	w       int
	tier    GlyphTier
	plain   bool  // no colour: ui.Default draws plain
	form    *Form // the card whose running row takes the outcome
	command string
	parts   []string
	label   string
	run     func() error
	outcome func(error) (Mark, string)
	sigs    <-chan os.Signal // the SIGINTs Hand catches
	ran     bool
	err     error
	mark    Mark
	text    string
}

func (x *handExec) SetStdin(io.Reader)  {}
func (x *handExec) SetStdout(io.Writer) {}
func (x *handExec) SetStderr(io.Writer) {}

// Run prints the frame and runs run, then prints a blank row and the outcome row and puts the
// outcome on the card's row before the card comes back.
func (x *handExec) Run() error {
	p := ui.NewTerminal(x.out, x.w)
	p.Tier, p.Plain = x.tier, x.plain
	p.Frame(x.command, x.parts...)
	x.err = x.run()
	x.mark, x.text = MarkDone, ""
	switch {
	case x.outcome != nil:
		x.mark, x.text = x.outcome(x.err)
	case x.err != nil:
		x.mark, x.text = MarkFailed, x.err.Error()
	}
	if x.form != nil && x.label != "" {
		x.form.setStatus(x.label, x.mark, x.text)
	}
	p.Blank()
	_, _ = io.WriteString(x.out, handRow(x.tier, x.plain, x.mark, x.label, x.text)+"\n")
	x.ran = true
	return nil
}

// markColors are the stream's inks for each mark.
var markColors = map[Mark]int{
	MarkDone:    ui.ColorSuccess,
	MarkNeeds:   ui.ColorWarn,
	MarkFailed:  ui.ColorFail,
	MarkLater:   ui.ColorMuted,
	MarkAhead:   ui.ColorMuted,
	MarkRunning: ui.ColorBrand,
}

// handRow is a step's outcome row on the primary screen: the mark at column 2, the label at
// column 4 and the outcome at column 18; plain draws no colour.
func handRow(t GlyphTier, plain bool, mark Mark, label, outcome string) string {
	glyph := mark.Glyph(t, 0)
	if c, ok := markColors[mark]; ok && !plain && !plainGlyphs(t) {
		glyph = ui.ANSI(c) + glyph + ui.ANSIReset
	}
	row := strings.Repeat(" ", markCol) + glyph + strings.Repeat(" ", labelCol-markCol-1) + label
	if outcome == "" {
		return row
	}
	gap := max(2, outcomeCol-labelCol-textWidth(label))
	return row + strings.Repeat(" ", gap) + outcome
}
