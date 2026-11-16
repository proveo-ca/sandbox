// SPEC: _spec/internal/sbx/host-android-adb.puml
package contract_test

import (
	"path/filepath"
	"testing"

	"github.com/proveo-ca/proveo/internal/hostdev"
	"github.com/proveo-ca/proveo/internal/manifest"
)

// A harness may declare android only when hostdev.Launches gives it a per-launch
// way to take the mobile server; every sbx harness has a row, with a reason when not.
func TestAndroidIsDeclaredOnlyWhereTheServerCanLaunch(t *testing.T) {
	ms, err := manifest.Load(filepath.Join(repoRoot(t), "defs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Capabilities.HasHostDevice(manifest.HostDeviceAndroid) && !hostdev.Supports(m.Name) {
			t.Errorf("%s declares hostDevices: android, but hostdev.Launches has no working row (%q)",
				m.Name, hostdev.Launches[m.Name].Why)
		}
		if _, ok := hostdev.Launches[m.Name]; !ok {
			t.Errorf("sbx harness %s has no hostdev.Launches row — add its mechanism or the reason it has none", m.Name)
		}
	}
}
