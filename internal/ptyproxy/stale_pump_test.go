//go:build !windows

// SPEC: _spec/internal/ptyproxy/pty-ownership.puml
package ptyproxy

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestRunLeavesNoReaderOnTheTerminal(t *testing.T) {
	const rounds, keys = 20, 10
	for r := range rounds {
		opMaster, opSlave, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Dup(int(opSlave.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		in := os.NewFile(uintptr(fd), "/dev/stdin")
		p := New(in, opSlave)
		if err := p.Run(exec.Command("true")); err != nil {
			t.Fatal(err)
		}
		second, err := os.OpenFile(opSlave.Name(), os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := term.MakeRaw(int(second.Fd())); err != nil {
			t.Fatal(err)
		}
		got := make(chan byte, keys*2)
		go func() {
			buf := make([]byte, 16)
			for {
				n, err := second.Read(buf)
				for i := range n {
					got <- buf[i]
				}
				if err != nil {
					return
				}
			}
		}()
		time.Sleep(30 * time.Millisecond)
		want := ""
		for i := range keys {
			want += string(rune('a' + i))
			_, _ = opMaster.Write([]byte{byte('a' + i)})
			time.Sleep(10 * time.Millisecond)
		}
		have := ""
		timeout := time.After(time.Second)
	collect:
		for len(have) < keys {
			select {
			case b := <-got:
				have += string(rune(b))
			case <-timeout:
				break collect
			}
		}
		_ = opMaster.Close()
		_ = opSlave.Close()
		_ = in.Close()
		if have != want {
			t.Fatalf("round %d: a reader opened after Run got %q of %q; the proxy's stdin reader outlived Run and took the rest", r, have, want)
		}
	}
}
