package postgresql_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const postgresImage = "postgres:16.4"

func TestEntrypointPreservesDynamicPostgreSQLRole(t *testing.T) {
	if os.Getenv("CG_DOCKER_INTEGRATION_TESTS") != "1" {
		t.Skip("set CG_DOCKER_INTEGRATION_TESTS=1 to run Docker entrypoint integration tests")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is unavailable")
	}
	if err := exec.Command("docker", "image", "inspect", postgresImage).Run(); err != nil {
		t.Skip(postgresImage + " is unavailable")
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test source path")
	}
	entrypoint := filepath.Join(filepath.Dir(currentFile), "clusterguard-postgres-entrypoint.sh")
	if override := os.Getenv("CG_PG_ENTRYPOINT_TEST_SCRIPT"); override != "" {
		if !filepath.IsAbs(override) {
			t.Fatal("entrypoint test script must use an absolute path")
		}
		entrypoint = override
	}

	t.Run("existing standby keeps runtime upstream", func(t *testing.T) {
		output := runEntrypointFixture(t, entrypoint, strings.Join([]string{
			"primary_conninfo = 'host=192.0.2.30 port=55432 user=cg_replication'",
			"clusterguard.primary_node_id = '33333333-3333-4333-8333-333333333333'",
			"clusterguard.node_id = 'old'",
			"clusterguard.hostname = 'old'",
		}, "\n")+"\n")
		for _, expected := range []string{
			"clusterguard.node_id = '11111111-1111-4111-8111-111111111111'",
			"clusterguard.primary_node_id = '33333333-3333-4333-8333-333333333333'",
			"clusterguard.hostname = 'orch-pg03'",
		} {
			if !strings.Contains(output, expected) {
				t.Fatalf("entrypoint output is missing %q:\n%s", expected, output)
			}
		}
		if strings.Contains(output, "clusterguard.primary_node_id = '22222222-2222-4222-8222-222222222222'") {
			t.Fatalf("static stack upstream replaced runtime upstream:\n%s", output)
		}
	})

	t.Run("promoted primary remains without upstream", func(t *testing.T) {
		output := runEntrypointFixture(t, entrypoint, strings.Join([]string{
			"clusterguard.node_id = 'old'",
			"clusterguard.hostname = 'old'",
		}, "\n")+"\n")
		if strings.Contains(output, "clusterguard.primary_node_id") {
			t.Fatalf("restart restored a static upstream on the promoted primary:\n%s", output)
		}
	})
}

func runEntrypointFixture(t *testing.T, entrypoint, autoConfiguration string) string {
	t.Helper()
	script, err := os.ReadFile(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	entrypoint = filepath.Join(t.TempDir(), "entrypoint.sh")
	if err := os.WriteFile(entrypoint, script, 0o700); err != nil {
		t.Fatal(err)
	}
	dataDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDirectory, "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDirectory, "postgresql.auto.conf"), []byte(autoConfiguration), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "replication-password")
	if err := os.WriteFile(secret, []byte("integration-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"run", "--rm", "--pull=never", "--network=none", "--memory=128m", "--cpus=0.5",
		"--label", "clusterguard.test=entrypoint",
		// Relabel only disposable fixture copies, never repository or live files.
		"--volume", dataDirectory + ":/var/lib/postgresql/data:Z",
		"--volume", entrypoint + ":/usr/local/bin/clusterguard-postgres-entrypoint.sh:ro,Z",
		"--volume", secret + ":/run/secrets/postgres_replication_password:ro,Z",
		"--entrypoint", "/usr/local/bin/clusterguard-postgres-entrypoint.sh",
		"-e", "PGDATA=/var/lib/postgresql/data",
		"-e", "CG_PG_SLOT=03",
		"-e", "CG_PG_SOURCE_HOST=192.0.2.10",
		"-e", "CG_PG_SOURCE_PORT=55432",
		"-e", "CG_PG_REPLICATION_USER=cg_replication",
		"-e", "CG_PG_APPLICATION_NAME=11111111-1111-4111-8111-111111111111",
		"-e", "CG_PG_PRIMARY_SLOT=cg_slot_03",
		"-e", "CG_PG_NODE_ID=11111111-1111-4111-8111-111111111111",
		"-e", "CG_PG_PRIMARY_NODE_ID=22222222-2222-4222-8222-222222222222",
		"-e", "CG_PG_HOSTNAME=orch-pg03",
		postgresImage, "sh", "-c", `cat "$PGDATA/postgresql.auto.conf"`,
	}
	output, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("run PostgreSQL entrypoint fixture: %v\n%s", err, output)
	}
	return string(output)
}
