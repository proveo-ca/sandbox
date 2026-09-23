//go:build image

// SPEC: _spec/tests/testing-strategy.puml, _spec/_plans/host-shell-to-go.puml
package imagetest_test

import (
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/imagetest"
)

func TestImageMitmproxy(t *testing.T) {
	image := imagetest.Resolve("PROVEO_MITMPROXY_IMAGE", "proveo/mitmproxy:latest")
	s := imagetest.New(t, image)

	s.Check("mitmdump runs, ndjson_dump addon and entrypoint are baked", func(t *testing.T) {
		r := imagetest.Docker(imagetest.DefaultTimeout, nil,
			"run", "--rm", "--entrypoint", "/bin/bash", image, "-lc", `
  set -e
  mitmdump --version >/dev/null
  test -f /addons/ndjson_dump.py
  test -f /entrypoint.sh
`)
		if !r.OK() {
			t.Fatalf("%v\n  output: %s", r.Err, strings.TrimSpace(r.Out))
		}
	})
}
