// SPEC: _spec/internal/choiceui/wireframe.puml, _spec/cmd/proveo/github-access.puml
package run

import (
	"fmt"
	"slices"
	"strings"

	"github.com/proveo-ca/proveo/internal/choiceui"
	"github.com/proveo-ca/proveo/internal/credentials"
	"github.com/proveo-ca/proveo/internal/manifest"
	"github.com/proveo-ca/proveo/internal/sbx"
)

type credentialInventory struct{ APIKeys, OAuth, SSH []string }

func inventoryOf(man manifest.Manifest, lookup func(string) string, store map[string]sbx.StoreEntry, ssh []string) credentialInventory {
	api, oauth := map[string]bool{}, map[string]bool{}
	for service, entry := range store {
		if entry.APIKey {
			api[service] = true
		}
		if entry.OAuth {
			oauth[service] = true
		}
	}
	for _, name := range append(credentials.ProviderKeyVars(man, lookup), credentials.SubscriptionVars(man, lookup)...) {
		service, _ := credentials.StoreName(name)
		if service == "" {
			service = name
		}
		if strings.Contains(name, "OAUTH") {
			oauth[service] = true
		} else {
			api[service] = true
		}
	}
	keys := func(set map[string]bool) []string {
		out := make([]string, 0, len(set))
		for name := range set {
			out = append(out, name)
		}
		slices.Sort(out)
		return out
	}
	return credentialInventory{APIKeys: keys(api), OAuth: keys(oauth), SSH: slices.Clone(ssh)}
}

func credentialsRow(items credentialInventory) choiceui.Row {
	r := choiceui.Row{Label: "credentials", Multi: true, Locked: true,
		Options: []string{"API keys", "OAuth", "SSH"}, Cols: []int{0, 1, 2},
		Help: map[string]string{}, Lists: map[int]*choiceui.More{},
		Reason: "host credential inventory; configure with proveo init; the selected model and kit bindings determine use",
	}
	for i, list := range [][]string{items.APIKeys, items.OAuth, items.SSH} {
		r.On = append(r.On, len(list) > 0)
		r.Off = append(r.Off, true)
		r.Help[r.Options[i]] = fmt.Sprintf("%d configured item(s); read-only", len(list))
		if len(list) == 0 {
			continue
		}
		on := make([]bool, len(list))
		for j, name := range list {
			on[j] = true
			r.Help[name] = name + " — configured on the host; read-only"
		}
		r.Lists[i] = &choiceui.More{Cell: i, Options: slices.Clone(list), On: on, ReadOnly: true}
	}
	return r
}
