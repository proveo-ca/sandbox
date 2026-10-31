// SPEC: _spec/internal/sbx/host-inference.puml (BEST HOST MODEL)
package run

import (
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
)

func stubFitting(t *testing.T, fit ...string) *[]string {
	t.Helper()
	var asked []string
	orig := fittingHostModels
	t.Cleanup(func() { fittingHostModels = orig })
	fittingHostModels = func(c []string, _ func(string) string) []string { asked = c; return fit }
	return &asked
}

func TestAutoLocalModelPicksTheBestFittingListedModel(t *testing.T) {
	asked := stubFitting(t, localModels[0].tag(), localModels[1].tag())
	rs := &Spec{Man: manifest.Manifest{Name: "hermes"}}
	if got := autoLocalModel(rs, &Params{Target: "hermes"}, func(string) string { return "" }); got != localModels[0].tag() {
		t.Errorf("autoLocalModel(no key, no model) = %q, want %q", got, localModels[0].tag())
	}
	if len(*asked) != len(localModels) {
		t.Errorf("candidates = %v, want every listed model's host tag", *asked)
	}
}

func TestAutoLocalModelLeavesAnExplicitChoiceAlone(t *testing.T) {
	stubFitting(t, localModels[0].tag())
	none := func(string) string { return "" }
	for _, c := range []struct {
		name   string
		rs     *Spec
		p      *Params
		getenv func(string) string
	}{
		{"local model given", &Spec{Man: manifest.Manifest{Name: "hermes"}}, &Params{Target: "hermes", LocalModel: "x:1"}, none},
		{"provider key", func() *Spec {
			s := &Spec{Man: manifest.Manifest{Name: "hermes"}}
			s.Creds.Detected = []string{"openrouter"}
			return s
		}(), &Params{Target: "hermes"}, none},
		{"subscription", &Spec{Man: manifest.Manifest{Name: "claudecode", Subscription: true}}, &Params{Target: "claudecode"}, none},
		{"vendor-pinned", &Spec{Man: manifest.Manifest{Name: "cursor"}}, &Params{Target: "cursor"}, none},
		{"print only", &Spec{Man: manifest.Manifest{Name: "hermes"}}, &Params{Target: "hermes", PrintOnly: true}, none},
		{"opted out", &Spec{Man: manifest.Manifest{Name: "hermes"}}, &Params{Target: "hermes"},
			func(k string) string { return map[string]string{"PROVEO_LOCAL_MODEL_AUTO": "0"}[k] }},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := autoLocalModel(c.rs, c.p, c.getenv); got != "" {
				t.Errorf("autoLocalModel(%s) = %q, want none", c.name, got)
			}
		})
	}
}

func TestAutoLocalModelNeedsAFit(t *testing.T) {
	stubFitting(t)
	if got := autoLocalModel(&Spec{Man: manifest.Manifest{Name: "hermes"}}, &Params{Target: "hermes"}, func(string) string { return "" }); got != "" {
		t.Errorf("autoLocalModel with nothing fitting = %q, want none", got)
	}
}
