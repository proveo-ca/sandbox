// SPEC: _spec/internal/choiceui/wireframe.puml
package credentials

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEnvFileNamesListsNamesAToZWithoutValues(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".env")
	body := strings.Join([]string{
		"# a comment=not-a-name",
		"zeta=value-zeta-8f3",
		"export ALPHA='value-alpha-8f3'",
		"",
		"=value-nameless-8f3",
		"Beta=\"value-beta-8f3\"",
		"no equals sign",
		"ALPHA=value-alpha-again-8f3",
		"  gamma = value-gamma-8f3  ",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := EnvFileNames(path)
	if want := []string{"ALPHA", "Beta", "gamma", "zeta"}; !slices.Equal(got, want) {
		t.Fatalf("names = %q, want %q", got, want)
	}
	if strings.Contains(strings.Join(got, " "), "value-") {
		t.Fatalf("a value reached the names: %q", got)
	}
	if got := EnvFileNames(filepath.Join(t.TempDir(), "missing")); len(got) != 0 {
		t.Fatalf("a missing file named %q", got)
	}
	if got := EnvFileNames(""); len(got) != 0 {
		t.Fatalf("no file named %q", got)
	}
}

// multiLineEnv is an env file whose quoted values span lines: a PEM in double quotes, a service
// account JSON in single quotes, both behind export, and a quote left open at the end of the file.
const multiLineEnv = `# keys
export TLS_KEY="-----BEGIN PRIVATE KEY-----
MIIEvQIBADANBgkqhkiG9w0BAQEFAASC==
PEMBODYLINE=two+three/four
-----END PRIVATE KEY-----"
PLAIN=after-pem
export SA_JSON='{
  "type": "service_account",
  "private_key": "-----BEGIN PRIVATE KEY-----\nJSONKEYBODY==\n-----END PRIVATE KEY-----\n",
  "client_email": "bot@proj.iam.gserviceaccount.com"
}'
ESCAPED="line one
says \"hi\" = yes
"
LAST=kept
OPEN="never closed
DANGLING_FRAGMENT=leaks-if-parsed
TAIL_BODY==
`

func writeEnv(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAQuotedValueRunsAcrossLinesToItsClosingQuote(t *testing.T) {
	t.Parallel()
	got := ParseEnvFile(writeEnv(t, multiLineEnv))
	want := map[string]string{
		"TLS_KEY": "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC==\nPEMBODYLINE=two+three/four\n-----END PRIVATE KEY-----",
		"PLAIN":   "after-pem",
		"SA_JSON": "{\n  \"type\": \"service_account\",\n" +
			"  \"private_key\": \"-----BEGIN PRIVATE KEY-----\\nJSONKEYBODY==\\n-----END PRIVATE KEY-----\\n\",\n" +
			"  \"client_email\": \"bot@proj.iam.gserviceaccount.com\"\n}",
		"ESCAPED": "line one\nsays \\\"hi\\\" = yes\n",
		"LAST":    "kept",
	}
	if len(got) != len(want) {
		t.Errorf("parsed %d entries, want %d: %q", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["OPEN"]; ok {
		t.Errorf("a quote left open at the end of the file kept its entry: %q", got["OPEN"])
	}
}

func TestEnvFileNamesNeverHoldAFragmentOfAValue(t *testing.T) {
	t.Parallel()
	got := EnvFileNames(writeEnv(t, "1BAD=x\nWITH SPACE=x\nok.dot=x\n"+multiLineEnv))
	if want := []string{"ESCAPED", "LAST", "PLAIN", "SA_JSON", "TLS_KEY"}; !slices.Equal(got, want) {
		t.Fatalf("names = %q, want %q", got, want)
	}
	for _, fragment := range []string{"MIIE", "PEMBODY", "BEGIN", "END", "JSONKEY", "type", "private_key", "client_email",
		"bot@", "says", "never closed", "DANGLING", "TAIL", "{", "}", "\"", "'"} {
		for _, name := range got {
			if strings.Contains(name, fragment) {
				t.Errorf("name %q holds %q, a fragment of a value", name, fragment)
			}
		}
	}
}

func TestOneLineValuesParseAsBefore(t *testing.T) {
	t.Parallel()
	got := ParseEnvFile(writeEnv(t, strings.Join([]string{
		`A="two words"`, `B='single'`, `C="trailing" # note`, `D="C:\dir\"`, `E='it's'`, `F=x"y`, `G=""`, "H=\"crlf\"\r", "I=bare",
	}, "\n")))
	want := map[string]string{"A": "two words", "B": "single", "C": `"trailing" # note`, "D": `C:\dir\`, "E": "it's",
		"F": `x"y`, "G": "", "H": "crlf", "I": "bare"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("parsed %q, want %q", got, want)
	}
}

func TestAByteOrderMarkHidesNoName(t *testing.T) {
	t.Parallel()
	if got := EnvFileNames(writeEnv(t, "\ufeffFIRST=1\nPAD==\nLAST=2\n")); !slices.Equal(got, []string{"FIRST", "LAST"}) {
		t.Errorf("names = %q, want FIRST and LAST", got)
	}
}
