// SPEC: _spec/_paradigms/runtime-user-boundary.puml
package contract_test

import (
	"strings"
	"testing"
)

// commentFree drops `#` lines so a Dockerfile may EXPLAIN the trap without
// tripping the guard that enforces it.
func commentFree(body string) string {
	var kept []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}

var aliasPaths = map[string]bool{
	"/home/${USER_NAME}": true,
	"/home/$USER_NAME":   true,
}

func chownTargets(body string) map[string][]string {
	out := map[string][]string{}
	joined := strings.ReplaceAll(body, "\\\n", " ")
	for _, line := range strings.Split(joined, "\n") {
		for _, seg := range strings.Split(line, "&&") {
			fields := strings.Fields(seg)
			// Skip the RUN/COPY keyword so `RUN chown …` is seen.
			if len(fields) > 0 && (fields[0] == "RUN" || fields[0] == "COPY") {
				fields = fields[1:]
			}
			if len(fields) == 0 || fields[0] != "chown" {
				continue
			}
			var args []string
			seenOwner := false
			for _, f := range fields[1:] {
				if strings.HasPrefix(f, "-") {
					continue // a flag
				}
				if !seenOwner {
					seenOwner = true // the owner:group spec
					continue
				}
				args = append(args, strings.Trim(f, `"'`))
			}
			if len(args) > 0 {
				out[strings.TrimSpace(seg)] = args
			}
		}
	}
	return out
}

func TestNoDockerfileChownsTheHomeSymlink(t *testing.T) {
	t.Parallel()

	for _, img := range sortedKeys(imageDockerfiles) {
		rel := imageDockerfiles[img]
		body := commentFree(dockerfileBody(t, rel))
		if !strings.Contains(body, "ln -s /home/agent") {
			continue // no alias, no trap
		}
		for stmt, targets := range chownTargets(body) {
			for _, target := range targets {
				if !aliasPaths[target] {
					continue
				}
				t.Errorf("%s (%s) chowns the /home/${USER_NAME} SYMLINK:\n\t%s\n"+
					"chown does not follow it, so everything mkdir created through the link "+
					"stays root-owned while the link itself reports the right owner. Target "+
					"/home/agent (as cursor does) or a subpath ending at a real directory "+
					"(as claudecode does).", img, rel, stmt)
			}
		}
	}
}
