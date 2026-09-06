// SPEC: _spec/packages/lib/seed-and-launch.puml, _spec/internal/sbx/sbx-kit-contract.puml
package contract_test

import (
	"regexp"
	"strings"
	"testing"

	proveo "github.com/proveo-ca/proveo"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
)

// seedImageSources maps a manifest's image target to the Dockerfile that builds
// it, and the base that Dockerfile starts from. (image_size_test.go owns a
// same-shaped list for a different question; kept separate so neither test's
// coverage silently changes when the other's does.)
var seedImageSources = map[string]struct{ file, base string }{
	"cecli":               {"defs/cecli/Dockerfile", "proveo/base:latest"},
	"opencode":            {"defs/opencode/Dockerfile", "proveo/base-node-lsp:latest"},
	"cursor":              {"defs/cursor/Dockerfile", ""},
	"claudecode":          {"defs/claudecode/mcp/Dockerfile", "proveo/base-node-lsp:latest"},
	"claudecode-solidity": {"defs/claudecode/solidity/Dockerfile", "proveo/claudecode:latest"},

	// `--browser` rebuilds the SAME Dockerfile against base-node-browser
	// (Playwright + Chromium), so a COPY in the harness file covers both
	// variants — and the variant is what a browser run actually launches. The
	// crashed session ran proveo/opencode-browser:local.
	"claudecode-browser": {"defs/claudecode/mcp/Dockerfile", "proveo/base-node-browser:latest"},
	"opencode-browser":   {"defs/opencode/Dockerfile", "proveo/base-node-browser:latest"},
	"cursor-browser":     {"defs/cursor/Dockerfile", ""},
}

// baseDockerfiles resolves a proveo base image tag back to the file that builds
// it, so the seed can be inherited rather than restated.
var baseDockerfiles = map[string]string{
	"proveo/base:latest":              "defs/base/Dockerfile",
	"proveo/base-node:latest":         "defs/base-node/Dockerfile",
	"proveo/base-node-lsp:latest":     "defs/base-node-lsp/Dockerfile",
	"proveo/base-node-browser:latest": "defs/base-node-browser/Dockerfile",
	"proveo/claudecode:latest":        "defs/claudecode/mcp/Dockerfile",
}

// The Kit names ONE startup command for every sbx target, and an image that
// runs on sbx without shipping it fails startup with exit 127 — seeding
// nothing, on every run, silently enough that it took a crashed session and
// /var/log/sbx-kit-startup.log to surface:
//
//	> /etc/durable-startup.d/002-startup-opencode-posture/000-cmd.sh
//	sh: 2: exec: /usr/local/bin/proveo-seed: not found
//	fail … exit=127
//
// opencode and cecli both shipped without it. The Kit registers the command
// unconditionally, so nothing in Go could notice, and the e2e assertion checks
// that the KIT has the step — never that the IMAGE has the binary. Those are
// the two halves of one contract and this is the seam between them.
func TestEverySbxImageShipsTheKitsStartupCommand(t *testing.T) {
	t.Parallel()
	binary := sbx.SeedCommand("opencode").Command[0] // /usr/local/bin/proveo-seed

	ms, err := manifest.LoadFS(proveo.Manifests)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, m := range ms {
		if !m.IsSbx() {
			continue
		}
		for target := range m.Images { // target name -> image ref
			df, ok := seedImageSources[target]
			if !ok {
				t.Errorf("%s builds image target %q but no Dockerfile is mapped here — "+
					"add it, or this contract silently stops covering it", m.Name, target)
				continue
			}
			checked++
			if !installsSeed(t, df.file, df.base, binary) {
				t.Errorf("%s (%s) runs on sbx but never installs %s — its Kit startup "+
					"command exits 127 and the run seeds nothing", target, df.file, binary)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no sbx image was checked; the mapping above has gone stale")
	}
}

// installsSeed reports whether this Dockerfile, or any proveo base it builds
// on, copies the seed to the path the Kit names. Inheritance counts: solidity
// builds FROM the claudecode image and is right to not restate it.
func installsSeed(t *testing.T, file, base, binary string) bool {
	t.Helper()
	for depth := 0; file != "" && depth < 8; depth++ {
		body := instructionsOnly(readRepoFile(t, file))
		copyTo := regexp.MustCompile(`(?m)^COPY[^\n]*\s` + regexp.QuoteMeta(binary) + `\s*$`)
		if copyTo.MatchString(body) {
			return true
		}
		if base == "" {
			return false
		}
		next := baseDockerfiles[base]
		if next == "" {
			return false // an upstream image we do not build; it will not have ours
		}
		file, base = next, declaredBase(readRepoFile(t, next))
	}
	return false
}

// declaredBase reads `ARG BASE_IMAGE=<ref>`, which is how every proveo image
// names its parent.
func declaredBase(df string) string {
	m := regexp.MustCompile(`(?m)^ARG BASE_IMAGE=(\S+)\s*$`).FindStringSubmatch(df)
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}
