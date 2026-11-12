//go:build !windows

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package ptyproxy

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnOverlayOpenAtChildExitEndsWithTheChild(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	p := New(inR, outW)
	opened := make(chan struct{}, 1)
	var readErr atomic.Value
	var decided, removed atomic.Bool
	p.HandleCtrlC(func(in io.Reader, _ io.Writer) CtrlCAction {
		opened <- struct{}{}
		_, err := in.Read(make([]byte, 8))
		readErr.Store(err)
		decided.Store(true)
		return CtrlCStay
	}, func() { removed.Store(true) })

	cmd := exec.Command("sleep", "30")
	ran := make(chan struct{})
	go func() { defer close(ran); _ = p.Run(cmd) }()
	time.Sleep(300 * time.Millisecond)
	if _, err := inW.Write([]byte{0x03}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-opened:
	case <-time.After(3 * time.Second):
		t.Fatal("the overlay never opened")
	}
	_ = cmd.Process.Kill()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the child died under an open overlay")
	}
	if !decided.Load() {
		t.Fatal("Run returned while the overlay was still up")
	}
	if err, _ := readErr.Load().(error); !errors.Is(err, io.EOF) {
		t.Errorf("the overlay's reader ended with %v, want io.EOF", err)
	}
	if removed.Load() {
		t.Error("an overlay ended by the child's exit still ran the remove hook")
	}
	if err := p.Overlay(func(io.Reader, io.Writer) error { return nil }); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Overlay after the child exited = %v, want ErrNotRunning", err)
	}
}

func TestAShutHandOffReaderStaysShutAcrossResume(t *testing.T) {
	r := &chanReader{ch: make(chan []byte, 1)}
	r.shut()
	r.resume()
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("Read after shut and resume = %v, want io.EOF", err)
	}
}
