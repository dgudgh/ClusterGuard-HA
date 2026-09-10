package redact

import (
	"strings"
	"testing"
)

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
