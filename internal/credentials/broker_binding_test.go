// SPEC: _spec/internal/credentials/credential-decisions.puml
package credentials

import (
	"slices"
	"strings"
	"testing"

	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/provider"
)

// The posture of run proveo-1788665157-82784, exactly as it was logged:
//
//	auth var   subscription
//	brokered   anthropic,cursor,openai,xai,google,opencode
//
// The answer withheld those keys from the agent's environment and the broker
// injected every one of them on-route anyway, because suppression stopped at
// the container. Six providers on the wire for a run that named one side.
func TestTheAnswerReachesTheBrokerNotJustTheEnvironment(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name: "opencode", Subscription: true,
		Env: []manifest.EnvVar{{Name: "OPENCODE_API_KEY", Secret: true}},
	}
	lookup := lookupOf(map[string]string{
		"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk", "CURSOR_API_KEY": "cur",
		"OPENAI_API_KEY": "oa", "XAI_API_KEY": "xa", "GEMINI_API_KEY": "gm",
	})
	detected := FilterProviders(provider.Detect(lookup), man.Capabilities)

	withheld := WithheldProviders(man, "opencode", AuthSubscription, "", lookup, detected)
	for _, want := range []string{"anthropic", "openai", "xai", "google"} {
		if !slices.Contains(withheld, want) {
			t.Errorf("%s survives an answer of %q: %v", want, AuthSubscription, withheld)
		}
	}
	if slices.Contains(withheld, "opencode") {
		t.Error("withheld the very credential the operator chose")
	}

	usable := UsableProviders(man, detected, lookup)
	brokered := BrokerProviders(false, man, Without(usable, withheld), lookup, true)
	if !slices.Equal(brokered, []string{"opencode"}) {
		t.Errorf("brokered = %v, want only the chosen side", brokered)
	}
}

// The mirror: choosing your own keys must keep them ALL on the wire, and drop
// only the harness's own credential.
func TestUsageCreditsKeepsEveryProviderKeyBrokered(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name: "opencode", Subscription: true,
		Env: []manifest.EnvVar{{Name: "OPENCODE_API_KEY", Secret: true}},
	}
	lookup := lookupOf(map[string]string{
		"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk", "OPENAI_API_KEY": "oa",
	})
	detected := FilterProviders(provider.Detect(lookup), man.Capabilities)
	withheld := WithheldProviders(man, "opencode", AuthUsage, "", lookup, detected)
	if !slices.Equal(withheld, []string{"opencode"}) {
		t.Errorf("withheld = %v, want only the harness's own credential", withheld)
	}
	usable := UsableProviders(man, detected, lookup)
	brokered := BrokerProviders(false, man, Without(usable, withheld), lookup, true)
	for _, want := range []string{"anthropic", "openai"} {
		if !slices.Contains(brokered, want) {
			t.Errorf("%s was dropped on the side that chose it: %v", want, brokered)
		}
	}
}

// No answer withholds nothing. A run that never reached the prompt must behave
// exactly as it did before this existed.
func TestNoAnswerWithholdsNothing(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{Name: "opencode", Env: []manifest.EnvVar{{Name: "OPENCODE_API_KEY", Secret: true}}}
	lookup := lookupOf(map[string]string{"OPENCODE_API_KEY": "zen", "ANTHROPIC_API_KEY": "sk"})
	detected := FilterProviders(provider.Detect(lookup), man.Capabilities)
	if got := WithheldProviders(man, "opencode", "", "", lookup, detected); len(got) != 0 {
		t.Errorf("withheld %v with no answer given", got)
	}
}

// A provider keeps its route while ANY credential it could use survives.
func TestAProviderWithASurvivingCredentialIsNotWithheld(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name: "claudecode", Subscription: true,
		Env:          []manifest.EnvVar{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"anthropic"}},
	}
	lookup := lookupOf(map[string]string{"ANTHROPIC_API_KEY": "sk", "CLAUDE_CODE_OAUTH_TOKEN": "oauth"})
	detected := FilterProviders(provider.Detect(lookup), man.Capabilities)
	// Choosing the plan drops the API key, but anthropic is still reachable
	// through the token — the ROUTE must stay.
	if got := WithheldProviders(man, "claudecode", AuthSubscription, "", lookup, detected); len(got) != 0 {
		t.Errorf("withheld %v, but anthropic still has a usable credential", got)
	}
}

// cursor is vendor-pinned, so it has no far side and nothing to withhold.
func TestVendorPinnedRunWithholdsNothing(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name: "cursor", Subscription: true, Provider: "cursor",
		Env:          []manifest.EnvVar{{Name: "CURSOR_API_KEY", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"cursor"}},
	}
	lookup := lookupOf(map[string]string{"CURSOR_API_KEY": "cur", "ANTHROPIC_API_KEY": "sk"})
	detected := FilterProviders(provider.Detect(lookup), man.Capabilities)
	if got := WithheldProviders(man, "cursor", AuthSubscription, "", lookup, detected); len(got) != 0 {
		t.Errorf("withheld %v on a harness with only one side", got)
	}
}

// One credential that buys BOTH a plan and metered usage backs both options.
// Filing OPENCODE_API_KEY under "subscription" alone told the operator the key
// settles the bill; it does not — the model id does.
func TestAGatewayCredentialBacksBothSides(t *testing.T) {
	t.Parallel()
	man := manifest.Manifest{
		Name: "opencode", Subscription: true,
		Env: []manifest.EnvVar{{Name: "OPENCODE_API_KEY", Secret: true}},
	}
	lookup := lookupOf(map[string]string{"OPENCODE_API_KEY": "zen"})
	b := AuthBacking(man, lookup, "opencode", "", "")
	for _, side := range []string{AuthUsage, AuthSubscription} {
		if !strings.Contains(b[side], "OPENCODE_API_KEY") {
			t.Errorf("%q backing = %q, want the gateway key on both sides", side, b[side])
		}
	}
	if got := AvailableAuthVars(man, lookup); !slices.Contains(got, AuthUsage) ||
		!slices.Contains(got, AuthSubscription) {
		t.Errorf("available = %v, want both sides offered on one gateway key", got)
	}
	// And the row has to admit what it cannot decide.
	if c := BillingCaveat(man, lookup); !strings.Contains(c, "opencode-go/") ||
		!strings.Contains(c, "opencode/") {
		t.Errorf("opencode caveat = %q, want both prefixes named", c)
	}
	cursor := manifest.Manifest{
		Name: "cursor", Subscription: true, Provider: "cursor",
		Env:          []manifest.EnvVar{{Name: "CURSOR_API_KEY", Secret: true}},
		Capabilities: manifest.Capabilities{Providers: []string{"cursor"}},
	}
	c := BillingCaveat(cursor, lookupOf(map[string]string{"CURSOR_API_KEY": "cur"}))
	if !strings.Contains(c, "overage") {
		t.Errorf("cursor caveat = %q, want the plan-then-overage split named", c)
	}
}
