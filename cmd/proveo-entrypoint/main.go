// SPEC: _spec/cmd/proveo-entrypoint/prep-process-boundary.puml, _spec/internal/sbx/host-android-adb.puml, _spec/cmd/proveo-entrypoint/prep-sequence.puml, _spec/_paradigms/harness-paradigms.puml
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/proveo-ca/proveo/internal/adbmirror"
	"github.com/proveo-ca/proveo/internal/entrypoint"
	"github.com/proveo-ca/proveo/internal/verify"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: proveo-entrypoint prep [target] | proveo-entrypoint verify [dir] | proveo-entrypoint adb-mirror | proveo-entrypoint <target> -- <command> [args...]")
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

	prepOnly := os.Args[1] == "prep"
	var smokeTarget string
	var args []string
	if prepOnly {
		smokeTarget = "harness"
		if len(os.Args) > 2 {
			smokeTarget = os.Args[2]
		}
	} else {
		smokeTarget = os.Args[1]
		args = os.Args[2:]
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
	}

	runPrep()

	if entrypoint.SmokeReady(smokeTarget) {
		for {
			time.Sleep(time.Hour)
		}
	}

	if prepOnly {
		return
	}
	if len(args) == 0 {
		return
	}
	bin, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "proveo-entrypoint: %v\n", err)
		os.Exit(127)
	}
	if err := syscall.Exec(bin, args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "proveo-entrypoint exec: %v\n", err)
		os.Exit(1)
	}
}

func runPrep() {
	entrypoint.EnsureRuntimeUser()

	if wd := os.Getenv("PROVEO_WORKDIR"); wd != "" {
		_ = os.Chdir(wd)
	} else if st, err := os.Stat("/app"); err == nil && st.IsDir() {
		_ = os.Chdir("/app")
	}

	mode := os.Getenv("PROVEO_EGRESS_MODE")
	if entrypoint.ShouldSkipEnvLoad(mode) {
		fmt.Printf("🔒 Skipping .env load (egress mode %s — secrets stay on host / broker)\n", mode)
	} else if path := entrypoint.FindEnvFile(""); path != "" {
		fmt.Printf("✅ Found .env at %s\n", path)
		if err := entrypoint.LoadEnvFile(path); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  load .env: %v\n", err)
		} else {
			fmt.Println("✅ Loaded environment variables from .env")
		}
	} else {
		fmt.Println("🔎 No .env found")
	}
	entrypoint.BridgeGoogleKeys()

	if keys := os.Getenv("PROVEO_CREDENTIAL_BROKER_KEYS"); keys != "" {
		if rewritten := entrypoint.ApplyBrokerSentinel(mode, keys, os.Getenv("PROVEO_BROKER_SENTINEL")); len(rewritten) > 0 {
			fmt.Printf("🔒 Broker sentinel applied to: %v\n", rewritten)
		}
	}

	entrypoint.BridgeGitIdentity("")
	reportGitContext("")
	entrypoint.ApplyEnvBridges()
}

func reportGitContext(dir string) {
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
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
