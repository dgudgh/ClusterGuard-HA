package postgresql_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This executes the real restart branch with only ownership/delegation stubbed;
// actual database startup remains covered by the Docker integration fixture.
func TestInitializedEntrypointIgnoresObsoleteBootstrapCoordinates(t *testing.T) {
	if _, err := os.Stat("/run/secrets/postgres_replication_password"); err == nil {
		t.Skip("test must not run against host deployment secrets")
	}
	script, err := filepath.Abs("clusterguard-postgres-entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{"01", "03"} {
		t.Run(slot, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "PG_VERSION"), []byte("16\n"), 0600); err != nil {
				t.Fatal(err)
			}
			configuration := "primary_conninfo = 'host=192.0.2.153 port=55432 user=replica'\nclusterguard.primary_node_id = '33333333-3333-4333-8333-333333333333'\n"
			if err := os.WriteFile(filepath.Join(dir, "postgresql.auto.conf"), []byte(configuration), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", `chown() { return 0; }
exec() { [[ "$1" == /usr/local/bin/docker-entrypoint.sh ]] || exit 99; exit 0; }
source "$1"`, "restart-fixture", script)
			cmd.Env = append(os.Environ(), "PGDATA="+dir, "CG_PG_SLOT="+slot, "CG_PG_NODE_ID=11111111-1111-4111-8111-111111111111", "CG_PG_HOSTNAME=pg-fixture", "CG_PG_SOURCE_HOST=", "CG_PG_SOURCE_PORT=invalid", "CG_PG_PRIMARY_NODE_ID=obsolete-bootstrap", "CG_PG_APPLICATION_NAME=", "CG_PG_PRIMARY_SLOT=")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("runtime restart read bootstrap coordinates: %v %s", err, output)
			}
			actual, err := os.ReadFile(filepath.Join(dir, "postgresql.auto.conf"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(actual), configuration) {
				t.Fatalf("runtime upstream was overwritten: %s", actual)
			}
		})
	}
}
