// SPEC: _spec/_devops/image-lineage-and-publish.puml
package contract_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/proveo-ca/proveo/internal/maintain"
)

// fakeDocker answers `docker image inspect <ref> --format {{.Created}}` from a
// table in $FAKE_IMAGES, and exits non-zero for anything absent — the same signal
// a host gives for an image it does not have.
const fakeDocker = `#!/usr/bin/env bash
[ "$1" = image ] && [ "$2" = inspect ] || exit 1
ref="$3"
IFS=';' read -ra rows <<< "${FAKE_IMAGES:-}"
for r in "${rows[@]}"; do
  [ "${r%%=*}" = "$ref" ] && { printf '%s\n' "${r#*=}"; exit 0; }
done
exit 1
`

// The shell resolver every def's test.sh goes through must agree with the one the
// runner uses. Two implementations of "prefer the newer build" that disagree is a
// suite testing a different image than the run it is supposed to vouch for.
func TestShellImageResolverMatchesResolveImage(t *testing.T) {
	t.Parallel()

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(repoRoot(t), "defs", "lib", "docker-build.sh")

	const (
		older = "2026-08-31T00:04:06.000000000Z"
		newer = "2026-09-03T12:00:00.000000000Z"
	)
	stamp := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return ts
	}

	cases := []struct {
		name  string
		ref   string
		table map[string]string
	}{
		{"local newer than published", "proveo/cecli:latest",
			map[string]string{"proveo/cecli:latest": older, "proveo/cecli:local": newer}},
		{"local older than published", "proveo/cecli:latest",
			map[string]string{"proveo/cecli:latest": newer, "proveo/cecli:local": older}},
		{"no local build", "proveo/cecli:latest",
			map[string]string{"proveo/cecli:latest": newer}},
		{"local only, nothing published locally", "proveo/cecli:latest",
			map[string]string{"proveo/cecli:local": older}},
		{"neither present", "proveo/cecli:latest", map[string]string{}},
		{"explicit tag is a decision", "proveo/cecli:v2",
			map[string]string{"proveo/cecli:local": newer}},
		{"an explicit :local stays", "proveo/cecli:local",
			map[string]string{"proveo/cecli:local": newer}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rows []string
			for ref, ts := range c.table {
				rows = append(rows, ref+"="+ts)
			}

			cmd := exec.Command("bash", "-c",
				`source "$1"; proveo_resolve_image "$2"`, "bash", lib, c.ref)
			cmd.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_IMAGES="+strings.Join(rows, ";"))
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("proveo_resolve_image %q: %v", c.ref, err)
			}
			gotShell := strings.TrimSpace(string(out))

			gotGo, _ := maintain.ResolveImage(c.ref, func(ref string) (time.Time, bool) {
				ts, ok := c.table[ref]
				if !ok {
					return time.Time{}, false
				}
				return stamp(ts), true
			})

			if gotShell != gotGo {
				t.Errorf("resolvers disagree on %q: shell=%q, maintain.ResolveImage=%q — "+
					"a def suite would test a different image than `proveo run` uses",
					c.ref, gotShell, gotGo)
			}
		})
	}
}

// Every def test script must go through the resolver rather than pinning the
// publish tag. proveo_docker_build REFUSES to --load :latest, so a suite that
// hard-defaults to it can only ever exercise what the registry last published —
// never the tree the author is standing in. build.sh is exempt: :latest is
// legitimately its publish target.
func TestDefTestScriptsResolveRatherThanPinThePublishTag(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	for _, sub := range []string{"defs", "e2e"} {
		err := filepath.Walk(filepath.Join(root, sub), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".sh") {
				return err
			}
			base := filepath.Base(path)
			// Only test scripts. A build script's default target IS the publish tag.
			if !strings.HasPrefix(base, "test") && !strings.Contains(base, "helpers") &&
				!strings.Contains(base, "smoke") {
				return nil
			}
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			rel, _ := filepath.Rel(root, path)
			for i, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				if !strings.Contains(line, ":-") || !strings.Contains(line, ":"+maintain.PublishTag) {
					continue
				}
				if strings.Contains(line, "proveo_test_image") || strings.Contains(line, "proveo_resolve_image") {
					continue
				}
				t.Errorf("%s:%d pins the publish tag without resolving a newer local build:\n\t%s\n"+
					"wrap it in proveo_test_image (or proveo_resolve_image) from defs/lib/docker-build.sh",
					rel, i+1, strings.TrimSpace(line))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// One rule, in one place. The smoke suite grew its own `for tag in local latest`
// that preferred :local by EXISTENCE, so a stale local build shadowed an image
// pulled a minute ago while the run read as coverage of the newer one — the exact
// failure maintain.ResolveImage's doc comment warns about. A second opinion about
// which image is current is worse than none.
func TestNoAdHocLocalOverLatestResolvers(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	adHoc := regexp.MustCompile(`for\s+\w+\s+in\s+` + maintain.LocalTag + `\s+` + maintain.PublishTag)

	for _, sub := range []string{"defs", "e2e", "scripts"} {
		err := filepath.Walk(filepath.Join(root, sub), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".sh") {
				return err
			}
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if adHoc.Match(b) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s picks between :local and :latest by existence; use "+
					"proveo_resolve_image from defs/lib/docker-build.sh, which decides by "+
					"recency the way maintain.ResolveImage does", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
