// SPEC: _spec/_plans/isolated-browser-profiles.puml
package guestbrowser

import "os/exec"

func groupChromium(*exec.Cmd) {}

func killChromium(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}
