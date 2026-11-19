// SPEC: _spec/_plans/isolated-browser-profiles.puml
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/proveo-ca/proveo/internal/guestbrowser"
)

func runBrowser(args []string) error {
	if len(args) == 1 {
		switch args[0] {
		case "check":
			return guestbrowser.CheckExecutable(guestbrowser.Executable())
		case "clear-fresh":
			l, err := net.Listen("tcp4", guestbrowser.AgentAddress)
			if err != nil {
				return fmt.Errorf("browser launcher still owns fresh data: %w", err)
			}
			defer func() { _ = l.Close() }()
			return guestbrowser.ClearFresh(guestbrowser.ProfileRoot)
		}
	}
	if len(args) != 1 || (args[0] != "fresh" && args[0] != "named") {
		return fmt.Errorf("expected browser fresh or browser named")
	}
	input := bufio.NewReader(io.LimitReader(os.Stdin, 128))
	token, err := input.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read viewport token: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, input)
		cancel()
	}()
	agent, err := net.Listen("tcp4", guestbrowser.AgentAddress)
	if err != nil {
		return err
	}
	defer func() { _ = agent.Close() }()
	viewport, err := net.Listen("tcp4", ":"+strconv.Itoa(guestbrowser.ViewportPort))
	if err != nil {
		return err
	}
	return guestbrowser.Serve(ctx, agent, viewport, guestbrowser.Options{
		Executable: guestbrowser.Executable(), Root: guestbrowser.ProfileRoot,
		Persistent: args[0] == "named", Token: strings.TrimSpace(token),
	})
}
