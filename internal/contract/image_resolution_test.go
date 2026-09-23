// SPEC: _spec/_devops/image-lineage-and-publish.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/maintain"
)

func TestImageSuitesResolveRatherThanPinThePublishTag(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "internal", "imagetest", "*_test.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no image suites found: %v", err)
	}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, ":"+maintain.PublishTag+`"`) || strings.Contains(line, "imagetest.Resolve(") {
				continue
			}
			t.Errorf("%s:%d names the publish tag without resolving a newer local build:\n\t%s\n"+
				"wrap it in imagetest.Resolve, which decides by recency the way maintain.ResolveImage does",
				rel, i+1, strings.TrimSpace(line))
		}
	}
}

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
					"imagetest.Resolve (Go) or maintain.ResolveImage, which decide by "+
					"recency", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
