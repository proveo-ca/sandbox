// SPEC: _spec/cmd/proveo-entrypoint/prep-process-boundary.puml, _spec/internal/sbx/host-android-adb.puml, _spec/cmd/proveo-entrypoint/prep-sequence.puml, _spec/_paradigms/harness-paradigms.puml
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/proveo-ca/proveo/internal/adbmirror"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/verify"
)

const usage = "usage: proveo-entrypoint prep | proveo-entrypoint verify [dir] | proveo-entrypoint adb-mirror"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	if os.Args[1] == "verify" {
		root := ""
		if len(os.Args) > 2 {
			root = os.Args[2]
		}
		lines := verify.FormatLines(verify.Detect(root, exec.LookPath))
		if lines != "" {
			fmt.Println(lines)
		}
		return
	}

	if os.Args[1] == "adb-mirror" {
		host, port := os.Getenv("ADB_HOST"), os.Getenv("ADB_PORT")
		if host == "" || port == "" {
			fmt.Fprintln(os.Stderr, "proveo-entrypoint adb-mirror: ADB_HOST and ADB_PORT must be set")
			os.Exit(2)
		}
		logf := func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
		adbmirror.Run(context.Background(), net.JoinHostPort(host, port), 150*time.Millisecond, logf)
		return
	}

	if os.Args[1] != "prep" || len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	entrypoint.EnsureRuntimeUser()
	reportGitContext()
}

func reportGitContext() {
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	dir, _ := os.Getwd()
	if err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Run(); err == nil {
		top, _ := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
		fmt.Printf("✅ Git repository at %s", string(top))
		if origin, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output(); err == nil && len(origin) > 0 {
			fmt.Printf("✅ Remote origin: %s", origin)
		} else {
			fmt.Println("🔎 Not tracking a remote repo")
		}
	} else {
		fmt.Printf("🔎 Not a git repository: %s\n", dir)
	}
	name, _ := exec.Command("git", "-C", dir, "config", "--get", "user.name").Output()
	email, _ := exec.Command("git", "-C", dir, "config", "--get", "user.email").Output()
	ns, es := trimNL(string(name)), trimNL(string(email))
	if ns != "unset" || es != "unset" {
		fmt.Printf("✅ Git identity: %s <%s>\n", ns, es)
	} else {
		fmt.Println("🔎 No git identity (provide GIT_AUTHOR_NAME/GIT_AUTHOR_EMAIL)")
	}
	if _, err := exec.LookPath("gh"); err == nil {
		if err := exec.Command("timeout", "5s", "gh", "auth", "status").Run(); err == nil {
			fmt.Println("✅ gh session authenticated")
		} else {
			fmt.Println("🔎 gh session not authenticated (set GH_TOKEN or GITHUB_TOKEN)")
		}
	}
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	if s == "" {
		return "unset"
	}
	return s
}
