// SPEC: _spec/internal/maintain/build-schedule.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var fetchRun = regexp.MustCompile(`\b(apt-get\s+update|npm\s+install|pip3?\s+install|go\s+mod\s+download|go\s+install|go\s+build)\b`)

var cacheID = regexp.MustCompile(`\bid=([A-Za-z0-9_.-]+)`)

func dockerfileRuns(df string) []string {
	var runs []string
	var cur strings.Builder
	in := false
	for _, line := range strings.Split(df, "\n") {
		trim := strings.TrimSpace(line)
		if !in {
			if strings.HasPrefix(trim, "#") || trim == "" {
				continue
			}
			if !strings.HasPrefix(trim, "RUN ") && trim != "RUN" {
				continue
			}
			in = true
			cur.Reset()
			cur.WriteString(trim)
			if strings.HasSuffix(trim, `\`) {
				continue
			}
			runs = append(runs, cur.String())
			in = false
			continue
		}
		cur.WriteByte(' ')
		cur.WriteString(strings.TrimSuffix(trim, `\`))
		if strings.HasSuffix(trim, `\`) {
			continue
		}
		runs = append(runs, cur.String())
		in = false
	}
	return runs
}

func TestFetchRunsMountABuildKitCache(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	ids := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, "defs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "Dockerfile" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		body := readRepoFile(t, rel)
		for _, run := range dockerfileRuns(body) {
			if !fetchRun.MatchString(run) {
				continue
			}
			if !strings.Contains(run, "--mount=type=cache") {
				t.Errorf("%s fetch RUN lacks --mount=type=cache:\n%s", rel, run)
			}
			if strings.Contains(run, "go install") && strings.Contains(run, "@latest") {
				t.Errorf("%s go install is @latest:\n%s", rel, run)
			}
		}
		instr := instructionsOnly(body)
		if strings.Contains(instr, "npm cache clean") {
			t.Errorf("%s runs npm cache clean, which empties the npm mount", rel)
		}
		if strings.Contains(instr, "/var/lib/apt/lists") {
			t.Errorf("%s deletes /var/lib/apt/lists, which empties the apt mount", rel)
		}
		for _, m := range cacheID.FindAllStringSubmatch(body, -1) {
			id := m[1]
			if prev, ok := ids[id]; ok && prev != rel {
				t.Errorf("cache id %s is shared by %s and %s", id, prev, rel)
			}
			ids[id] = rel
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatal("no cache mount ids under defs/")
	}
}

func TestLSPBuilderGoInstallsArePinned(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		rel   string
		wants []string
	}{
		{"defs/cursor/Dockerfile", []string{
			"ARG MCP_LANGUAGE_SERVER_VERSION=v0.1.1",
			"ARG GOPLS_VERSION=v0.23.0",
			`mcp-language-server@${MCP_LANGUAGE_SERVER_VERSION}`,
			`gopls@${GOPLS_VERSION}`,
		}},
		{"defs/codex/Dockerfile", []string{
			"ARG MCP_LANGUAGE_SERVER_VERSION=v0.1.1",
			`mcp-language-server@${MCP_LANGUAGE_SERVER_VERSION}`,
		}},
		{"defs/base/Dockerfile", []string{
			"ARG PLANTUML_LSP_VERSION=v0.5.3",
			`plantuml-lsp@${PLANTUML_LSP_VERSION}`,
		}},
	} {
		df := instructionsOnly(readRepoFile(t, tc.rel))
		for _, want := range tc.wants {
			if !strings.Contains(df, want) {
				t.Errorf("%s lacks %q", tc.rel, want)
			}
		}
		if strings.Contains(df, "@latest") {
			t.Errorf("%s still contains @latest", tc.rel)
		}
	}
}
