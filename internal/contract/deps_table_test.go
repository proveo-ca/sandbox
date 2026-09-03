// SPEC: _spec/packages/lib/dependency-trees.puml, _spec/internal/workspace/mount-model.puml
package contract_test

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/proveo-ca/proveo/internal/workspace"
)

// The dependency-tree table lives twice — Go and shell — and these tests are
// the lockstep. SPEC: _spec/packages/lib/dependency-trees.puml

var (
	classArmRe = regexp.MustCompile(`(?m)^\s*([a-z|]+)\)\s+REPLY=([a-z]+)\s*;;`)
	depLangsRe = regexp.MustCompile(`_dep_langs\(\)\s*\{\s*echo\s+"([^"]+)"`)
)

func depClasses(t *testing.T, src string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range classArmRe.FindAllStringSubmatch(caseBody(t, src, "_dep_lang_class"), -1) {
		for _, lang := range strings.Split(m[1], "|") {
			out[lang] = m[2]
		}
	}
	return out
}

func echoArms(t *testing.T, src, fn string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, m := range quotedEchoRe.FindAllStringSubmatch(caseBody(t, src, fn), -1) {
		out[m[1]] = strings.Fields(m[2])
	}
	return out
}

func TestEverySupportedLanguageHasADependencyClass(t *testing.T) {
	t.Parallel()
	src := entrypointLib(t)
	classes := depClasses(t, src)

	if missing, extra := missingExtra(boolSet(classes), wantLanguages); len(missing) > 0 || len(extra) > 0 {
		t.Errorf("_dep_lang_class does not cover exactly the supported languages\n  missing: %v\n  extra:   %v\n"+
			"every language gets an explicit row — `none` for markup and config — because an absent "+
			"row is indistinguishable from an oversight", missing, extra)
	}

	m := depLangsRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("_dep_langs not found — the seed's walk order moved?")
	}
	walked := strings.Fields(m[1])
	var wantWalk []string
	for lang, class := range classes {
		if class != "none" {
			wantWalk = append(wantWalk, lang)
		}
	}
	sort.Strings(wantWalk)
	got := append([]string(nil), walked...)
	sort.Strings(got)
	if diff := cmp.Diff(wantWalk, got); diff != "" {
		t.Errorf("_dep_langs must walk every class except `none` (-want +got):\n%s", diff)
	}
}

func TestMountPlanAndSeedIsolateTheSameDependencyTrees(t *testing.T) {
	t.Parallel()
	src := entrypointLib(t)
	classes := depClasses(t, src)
	markers := echoArms(t, src, "_dep_lang_markers")
	dirs := echoArms(t, src, "_dep_lang_dirs")

	// Go → shell: every tree the plan hides, the seed knows about.
	goLangs := map[string]bool{}
	for _, row := range workspace.DepLangs {
		goLangs[row.Lang] = true
		if diff := cmp.Diff(row.Markers, markers[row.Lang]); diff != "" {
			t.Errorf("%s markers: internal/workspace.DepLangs vs _dep_lang_markers (-go +shell):\n%s", row.Lang, diff)
		}
		if diff := cmp.Diff(row.Dirs, dirs[row.Lang]); diff != "" {
			t.Errorf("%s dirs: internal/workspace.DepLangs vs _dep_lang_dirs (-go +shell):\n%s", row.Lang, diff)
		}
	}
	// shell → Go: every tree the seed would install into, the plan hides.
	for lang, d := range dirs {
		if len(d) > 0 && !goLangs[lang] {
			t.Errorf("_dep_lang_dirs names %v for %s but internal/workspace.DepLangs has no row — "+
				"the seed would install into a directory the plan bind-mounts from the host", d, lang)
		}
	}
	// A language that materialises a tree must say where it roots.
	for lang, class := range classes {
		switch class {
		case "addons", "artifacts", "provisioned":
			if len(markers[lang]) == 0 || len(dirs[lang]) == 0 {
				t.Errorf("%s is class %s but has markers=%v dirs=%v — a tree with no root or no name cannot be isolated", lang, class, markers[lang], dirs[lang])
			}
		case "none":
			if len(markers[lang]) > 0 || len(dirs[lang]) > 0 {
				t.Errorf("%s is class none yet has markers=%v dirs=%v", lang, markers[lang], dirs[lang])
			}
		}
	}
	// An install command without a marker never runs; a marker without a class is a typo.
	for lang := range markers {
		if _, ok := classes[lang]; !ok {
			t.Errorf("_dep_lang_markers has %q but _dep_lang_class does not", lang)
		}
	}
}
