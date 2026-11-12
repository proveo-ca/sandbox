// SPEC: _spec/internal/ui/step-lines.puml
package ui

import (
	"cmp"
	"fmt"
	"strings"
	"time"
)

// StepClock is how often a running step's live line redraws its elapsed time.
var StepClock = time.Second

// Running shows a step while it runs and returns the func that ends it. A terminal
// gets one live line, redrawn with frame(elapsed); any other output gets plain, once, unless it is "".
func Running(frame func(ran time.Duration) string, plain string) (done func()) {
	if !Fancy() {
		if plain != "" {
			Asyncf("%s", plain)
		}
		return func() {}
	}
	began := time.Now()
	Live(RoleAsync, "%s", frame(0))
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(StepClock)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				Live(RoleAsync, "%s", frame(time.Since(began)))
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-stopped
		EndLive()
	}
}

// SuspendLive takes the live line off the terminal until the returned func is
// called: a prompt, or another program, is about to draw there.
func SuspendLive() (resume func()) { return Default.SuspendLive() }

// SuspendLive is the package function for one printer.
func (p *Printer) SuspendLive() (resume func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eraseLive()
	p.suspended++
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.suspended--; p.suspended == 0 {
			p.drawLive()
		}
	}
}

// Steps is a phase's plan, shown one step at a time: "step 2/5 · name · 12s".
// A nil plan runs each step and shows nothing.
type Steps struct {
	of, n int
}

// NewSteps is a plan of `of` steps.
func NewSteps(of int) *Steps { return &Steps{of: of} }

// label is the next step's place and name.
func (s *Steps) label(name string) string {
	s.n++
	return fmt.Sprintf("step %d/%d · %s", s.n, max(s.of, s.n), name)
}

// Do runs fn as the next step behind a live line with a clock.
func (s *Steps) Do(name string, fn func() error) error { return s.do(name, "", fn) }

// DoAs runs fn as Do does; output with no live line gets line, with no place in it.
func (s *Steps) DoAs(name, line string, fn func() error) error { return s.do(name, line, fn) }

func (s *Steps) do(name, line string, fn func() error) error {
	if s == nil {
		return fn()
	}
	label := s.label(name)
	Default.heading()
	logPlace(label, line)
	Logf("step: %s started", name)
	done := Running(func(ran time.Duration) string {
		return label + " · " + ran.Round(time.Second).String()
	}, cmp.Or(line, label))
	return logged(name, done, fn)
}

// Quiet runs fn as the next step behind a live line with a clock; output with no live line
// gets nothing.
func (s *Steps) Quiet(name string, fn func() error) error {
	if s == nil {
		return fn()
	}
	label := s.label(name)
	Default.heading()
	Logf("%s", label)
	Logf("step: %s started", name)
	done := Running(func(ran time.Duration) string {
		return label + " · " + ran.Round(time.Second).String()
	}, "")
	return logged(name, done, fn)
}

// Silent counts fn as the next step and logs its place; it prints nothing.
func (s *Steps) Silent(name string, fn func() error) error {
	if s == nil {
		return fn()
	}
	Logf("%s", s.label(name))
	Logf("step: %s started", name)
	return logged(name, func() {}, fn)
}

// Hand runs fn as the next step under a start line alone: fn gives the terminal
// to a prompt or to another program, which a live line would draw over.
func (s *Steps) Hand(name string, fn func() error) error { return s.hand(name, "", fn) }

// HandAs runs fn as Hand does, under the start line `line`, with no place in it.
func (s *Steps) HandAs(name, line string, fn func() error) error { return s.hand(name, line, fn) }

func (s *Steps) hand(name, line string, fn func() error) error {
	if s == nil {
		return fn()
	}
	label := s.label(name)
	Asyncf("%s", cmp.Or(line, label))
	logPlace(label, line)
	Logf("step: %s started", name)
	return logged(name, func() {}, fn)
}

// Start prints the next step's start line; the caller runs the step and records it.
func (s *Steps) Start(name string) {
	if s != nil {
		Asyncf("%s", s.label(name))
	}
}

// StartAs counts the next step and prints line, with no place in it; the caller runs the step and records it.
func (s *Steps) StartAs(name, line string) {
	if s != nil {
		label := s.label(name)
		Asyncf("%s", line)
		logPlace(label, line)
	}
}

// logPlace writes a step's place to the run log when its screen line carries none.
func logPlace(label, line string) {
	if line != "" {
		Logf("%s", label)
	}
}

// logged runs fn, ends the step's line, and writes the step's duration and outcome to the log.
func logged(name string, done func(), fn func() error) error {
	began := time.Now()
	err := fn()
	done()
	outcome := "ok"
	if err != nil {
		outcome = "failed: " + strings.SplitN(err.Error(), "\n", 2)[0]
	}
	Logf("step: %s %s %s", name, time.Since(began).Round(time.Millisecond), outcome)
	return err
}
