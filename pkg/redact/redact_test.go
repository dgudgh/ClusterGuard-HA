package redact

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestSQLPasswordsAndStoredKeyMaterialAreRedacted(t *testing.T) {
	for _, input := range []string{
		"ALTER ROLE replicator PASSWORD 'private-value';",
		"CREATE USER repl IDENTIFIED BY 'private-value';",
		"CREATE USER repl IDENTIFIED WITH caching_sha2_password BY 'private-value';",
		"ALTER USER repl IDENTIFIED WITH mysql_native_password AS 'private-value';",
		"ALTER ROLE repl PASSWORD 'private-''value';",
		`ALTER ROLE repl PASSWORD "private-value";`,
		"SCRAM-SHA-256$4096:c2FsdA==$c3RvcmVk:a2V5",
		"-----BEGIN PRIVATE KEY-----\nprivate-value\n-----END PRIVATE KEY-----",
	} {
		got := Text(input)
		if strings.Contains(got, "private-") || strings.Contains(got, "c3RvcmVk") || !strings.Contains(got, Replacement) {
			t.Fatalf("secret material survived redaction: %s", got)
		}
	}
}

func TestBoundedPreservesUTF8AndRedaction(t *testing.T) {
	for _, input := range []string{"abc", "\u4e3b\u5e93\u6821\u9a8c\u5931\u8d25", "A\u00e9\U0001f512\u4e2dZ", "password=private \u5931\u8d25", "bad\xff\u4e2d"} {
		for limit := 1; limit <= len(input)+1; limit++ {
			got := Bounded(input, limit)
			if len(got) > limit || !utf8.ValidString(got) || strings.Contains(got, "private") {
				t.Errorf("input=%q limit=%d output=%q", input, limit, got)
			}
		}
	}
}
