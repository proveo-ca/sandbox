// SPEC: _spec/internal/sbx/virtiofs-cwd-invalidation.puml
package sbx

import (
	"errors"
	"testing"
)

func TestRunningNamesFiltersToProveosOwnRunningSandboxes(t *testing.T) {
	origList, origLook := sh.SandboxList, lookPath
	t.Cleanup(func() { sh.SandboxList, lookPath = origList, origLook })
	lookPath = func(string) (string, error) { return "/usr/local/bin/sbx", nil }

	sh.SandboxList = func() ([]byte, error) {
		return []byte("NAME                      AGENT          STATUS\n" +
			"proveo-1787956302-22788   claude         running\n" +
			"proveo-1787956302-22789   opencode       stopped\n" +
			"my-own-sandbox            shell          running\n"), nil
	}
	names, ok := RunningNames()
	if !ok {
		t.Fatal("a readable listing must report ok")
	}
	if len(names) != 1 || names[0] != "proveo-1787956302-22788" {
		t.Errorf("RunningNames() = %v, want only the running proveo sandbox — a stopped one "+
			"holds nothing open, and an operator's own sandbox is not proveo's to reason about", names)
	}
}

// Unreadable while sbx IS installed is undecidable, not quiet: the caller uses
// this to gate a destructive prune.
func TestRunningNamesReportsAnUnreadableListing(t *testing.T) {
	origList, origLook := sh.SandboxList, lookPath
	t.Cleanup(func() { sh.SandboxList, lookPath = origList, origLook })
	lookPath = func(string) (string, error) { return "/usr/local/bin/sbx", nil }
	sh.SandboxList = func() ([]byte, error) { return nil, errors.New("daemon down") }

	if _, ok := RunningNames(); ok {
		t.Error("an unreadable listing must NOT report ok — the prune would read it as 'nothing running'")
	}
}

// No sbx on the host is a fact, not an unknown: a docker-only operator must
// still be able to reclaim their toolchains.
func TestRunningNamesWithoutTheCLI(t *testing.T) {
	origLook := lookPath
	t.Cleanup(func() { lookPath = origLook })
	lookPath = func(string) (string, error) { return "", errors.New("not found") }

	names, ok := RunningNames()
	if !ok || len(names) != 0 {
		t.Errorf("RunningNames() = %v, %v; want no sandboxes and a decided answer", names, ok)
	}
}
