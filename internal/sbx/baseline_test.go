package sbx

import "testing"

// The shape sbx prints for `policy inspect local-policy`. Only allow-all was
// observable on the development host — changing the baseline needs
// `sbx policy reset`, which clears every policy including per-sandbox Kit ones —
// so the balanced and deny-all fixtures are built from that same table shape.
// If sbx changes the table, TestPolicyBaselineLive is what catches it.
const header = `Policy:      local-policy
Policy ID:   local-policy
Source:      local
Applies to:  all
Status:      active

Rules in this policy:
DECISION   RESOURCE   TYPE               RULE   STATUS
`

func TestPolicyBaselineClassifiesEachSbxBaseline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules string
		want  string
		known bool
	}{
		{
			// The live host: `allow **` on network, rule id default-allow-all.
			name: "allow-all",
			rules: `allow      **         network            -      active
allow      **         filesystem:read    -      active
allow      **         filesystem:write   -      active`,
			want: BaselineAllowAll, known: true,
		},
		{
			// Specific allows and no `**`: sbx's "typical development traffic".
			name: "balanced",
			rules: `allow      registry.npmjs.org   network            -      active
allow      api.anthropic.com    network            -      active
allow      **                   filesystem:read    -      active`,
			want: BaselineBalanced, known: true,
		},
		{
			// Network present in the table but nothing allowed.
			name: "deny-all",
			rules: `deny       **         network            -      active
allow      **         filesystem:read    -      active`,
			want: BaselineDenyAll, known: true,
		},
		{
			// A filesystem-only table says nothing about the network baseline, and
			// guessing either way would put a false posture in front of the operator.
			name:  "no network rules is unreadable",
			rules: `allow      **         filesystem:read    -      active`,
			want:  "", known: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := sh.InspectPolicy
			t.Cleanup(func() { sh.InspectPolicy = orig })
			sh.InspectPolicy = func() ([]byte, error) { return []byte(header + tc.rules), nil }

			got, known := PolicyBaseline()
			if got != tc.want || known != tc.known {
				t.Errorf("PolicyBaseline() = %q,%v; want %q,%v", got, known, tc.want, tc.known)
			}
		})
	}
}

func TestPolicyBaselineIsUnknownWhenSbxCannotBeRead(t *testing.T) {
	orig := sh.InspectPolicy
	t.Cleanup(func() { sh.InspectPolicy = orig })
	sh.InspectPolicy = func() ([]byte, error) { return nil, errFake }

	if got, known := PolicyBaseline(); known {
		// "unreadable" must never render as a posture: an operator told "deny-all"
		// on a host that is actually allow-all would trust a boundary that is not there.
		t.Errorf("PolicyBaseline() = %q,%v; want unknown when sbx errors", got, known)
	}
}

var errFake = errFakeType{}

type errFakeType struct{}

func (errFakeType) Error() string { return "sbx unavailable" }
