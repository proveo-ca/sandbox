// SPEC: _spec/internal/devports/dev-ports.puml
package devports

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestAndroidAppsFindsApplicationModulesOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files := map[string]string{
		"app/build.gradle.kts":        "plugins { id(\"com.android.application\") }\nandroid {\n namespace = \"ca.proveo.ns\"\n defaultConfig { applicationId = \"ca.proveo.hello\" }\n}\n",
		"wear/build.gradle":           "plugins { alias(libs.plugins.android.application) }\nandroid { namespace 'ca.proveo.wear' }\n",
		"lib/build.gradle.kts":        "plugins { id(\"com.android.library\") }\nandroid { namespace = \"ca.proveo.lib\" }\n",
		"build.gradle.kts":            "plugins { id(\"com.android.application\") apply false }\n",
		"node_modules/x/build.gradle": "plugins { id 'com.android.application' }\nandroid { namespace 'x.y' }\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := []AndroidApp{
		{Module: ":app", AppID: "ca.proveo.hello", Source: "app/build.gradle.kts"},
		{Module: ":wear", AppID: "ca.proveo.wear", Source: "wear/build.gradle"},
	}
	if diff := cmp.Diff(want, AndroidApps(root)); diff != "" {
		t.Errorf("AndroidApps(%s) mismatch (-want +got):\n%s", root, diff)
	}
}

func TestAndroidAppsReadsTheE2EFixture(t *testing.T) {
	t.Parallel()
	got := AndroidApps("../../e2e/testdata/android-hello")
	want := []AndroidApp{{Module: ":app", AppID: "ca.proveo.hello", Source: "app/build.gradle.kts"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("AndroidApps(android-hello) mismatch (-want +got):\n%s", diff)
	}
	if task := got[0].Task(); task != ":app:installDebug" {
		t.Errorf("Task() = %q, want :app:installDebug", task)
	}
	if task := (AndroidApp{AppID: "x"}).Task(); task != "installDebug" {
		t.Errorf("root Task() = %q, want installDebug", task)
	}
}
