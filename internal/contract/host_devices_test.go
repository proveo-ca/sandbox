// SPEC: _spec/internal/sbx/host-android-adb.puml
package contract_test

import (
	"path/filepath"
	"testing"

	"github.com/proveo-ca/proveo/internal/hostadb"
	"github.com/proveo-ca/proveo/internal/manifest"
)

// A harness may declare android only when hostadb.Launches gives it a per-launch
// way to take the mobile server; every sbx harness has a row, with a reason when not.
func TestAndroidIsDeclaredOnlyWhereTheServerCanLaunch(t *testing.T) {
	ms, err := manifest.Load(filepath.Join(repoRoot(t), "defs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Capabilities.HasHostDevice(manifest.HostDeviceAndroid) && !hostadb.Supports(m.Name) {
			t.Errorf("%s declares hostDevices: android, but hostadb.Launches has no working row (%q)",
				m.Name, hostadb.Launches[m.Name].Why)
		}
		if m.IsSbx() {
			if _, ok := hostadb.Launches[m.Name]; !ok {
				t.Errorf("sbx harness %s has no hostadb.Launches row — add its mechanism or the reason it has none", m.Name)
			}
		}
	}
}
