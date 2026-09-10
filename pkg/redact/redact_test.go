package redact

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnosticJSONPreservesProtocolAndRemovesSecrets(t *testing.T) {
	input := []byte(`{"revision":18446744073709551615,"observation_token":"public-cas","must_change_password":false,"rows":[{"primary_conninfo":"host=db password=leaked","replication_password":"leaked","passfile":"/leaked","control_token":"leaked","message":"password='leaked with spaces'"}]}`)
	output, err := JSON(input)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output, []byte("leaked")) || !bytes.Contains(output, []byte("18446744073709551615")) {
		t.Fatalf("unsafe or imprecise: %s", output)
	}
	var result struct {
		Revision    uint64
		Observation string `json:"observation_token"`
		MustChange  bool   `json:"must_change_password"`
	}
	if err = json.Unmarshal(output, &result); err != nil || result.Revision != ^uint64(0) || result.Observation != "public-cas" || result.MustChange {
		t.Fatalf("protocol changed: %#v %v", result, err)
	}
	for _, bad := range []string{`{`, `{} {}`, `{} trailing`} {
		if _, err = JSON([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestStructuredAndNumericSecretsAreNotExposed(t *testing.T) {
	output, err := JSON([]byte(`{"password":123456,"token":["secret-value"],"replication_secret":{"value":"nested-value"},"must_change_password":false}`))
	if err != nil || bytes.Contains(output, []byte("123456")) || bytes.Contains(output, []byte("secret-value")) || bytes.Contains(output, []byte("nested-value")) || !bytes.Contains(output, []byte(`"must_change_password":false`)) {
		t.Fatalf("unsafe diagnostic structure: %s %v", output, err)
	}
}

func TestSecretForms(t *testing.T) {
	for _, input := range []string{
		`password=leaked`, `PASSWORD='leaked with spaces'`, `password="leaked with spaces"`,
		`{"replication_password":"leaked"}`, `{"token": "leaked"}`, `CG_CONTROL_TOKEN=leaked`,
		`Authorization: Bearer leaked`, `mysql -pleaked -e SELECT`,
		`postgresql://repl:leaked@host/db`, `passfile=/leaked/path`,
		`ALTER SYSTEM SET primary_conninfo = 'host=x password=''leaked nested'' application_name=x'`,
		`primary_conninfo="host=x password=leaked"`,
	} {
		t.Run(input, func(t *testing.T) {
			if got := Text(input); strings.Contains(got, "leaked") || !strings.Contains(got, Replacement) {
				t.Fatalf("secret survived: %s", got)
			}
		})
	}
}

func TestRedactBeforeTruncating(t *testing.T) {
	input := "password='" + strings.Repeat("sensitive ", 100) + "'"
	if got := Bounded(input, 30); strings.Contains(got, "sensitive") {
		t.Fatal(got)
	}
	if got := Text("could not connect to 192.0.2.1:5432"); got != "could not connect to 192.0.2.1:5432" {
		t.Fatal(got)
	}
	if got := Text("opaque a%26b and a&b", "a&b"); strings.Contains(got, "a%26b") || strings.Contains(got, "a&b") {
		t.Fatal(got)
	}
}
