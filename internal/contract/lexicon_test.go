// SPEC: _spec/_conventions/spec-conventions.puml
package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// lexiconEnforced are the files held to the banned phrase; a file joins once it is clean.
var lexiconEnforced = []string{
	"_spec/_plans/session-continuity.puml",
	"_spec/internal/sbx/state-sync.puml",
}

// bannedPhrase matches the banned phrase in any case, across a comment-line break too.
var bannedPhrase = regexp.MustCompile(`(?i)\bat[\s']+all\b`)

func TestEnforcedFilesAvoidTheBannedPhrase(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range lexiconEnforced {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, at := range bannedPhrase.FindAllIndex(b, -1) {
			t.Errorf("%s:%d uses a banned phrase; drop it or name a concrete quantifier",
				rel, strings.Count(string(b[:at[0]]), "\n")+1)
		}
	}
}
