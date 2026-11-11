//go:build !windows

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package ptyproxy

import (
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestDiscardInputDropsWhatWasTypedBeforeIt(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close(); _ = slave.Close() })
	if _, err := term.MakeRaw(int(slave.Fd())); err != nil {
		t.Fatal(err)
	}
	if _, err := master.Write([]byte("y\r")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	DiscardInput(slave)
	if _, err := master.Write([]byte("z")); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := slave.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case s := <-got:
		if s != "z" {
			t.Errorf("read %q after DiscardInput, want only the key typed after it", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing arrived after DiscardInput")
	}
	DiscardInput(nil)
}
