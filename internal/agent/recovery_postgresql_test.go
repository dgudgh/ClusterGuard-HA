package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestRecoveryOfflineNativePostgreSQL(t *testing.T) {
	bin := os.Getenv("CG_PG16_BIN")
	if bin == "" {
		t.Skip("CG_PG16_BIN required for real PostgreSQL WAL verification")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	run := func(name string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(bin, name), args...)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
		return out
	}
	run("initdb", "-D", dir, "-U", "cg_fixture", "-A", "trust", "--locale=C")
	// Unix socket only: this fixture never listens on a production interface.
	socket, err := os.MkdirTemp("/tmp", "cg-recovery-pg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socket) })
	run("pg_ctl", "-D", dir, "-l", filepath.Join(root, "server.log"), "-o", "-c listen_addresses='' -c unix_socket_directories='"+socket+"'", "-w", "start")
	running := true
	t.Cleanup(func() {
		if running {
			exec.Command(filepath.Join(bin, "pg_ctl"), "-D", dir, "-m", "immediate", "stop").Run()
		}
	})
	run("psql", "-h", socket, "-U", "cg_fixture", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", "CREATE TABLE recovery_fixture (id integer); INSERT INTO recovery_fixture VALUES (1); CHECKPOINT;")
	run("pg_ctl", "-D", dir, "-m", "fast", "-w", "stop")
	running = false
	policy := ClusterPolicy{InstanceID: model.NewResourceID(), PostgreSQLNodeID: model.NewResourceID(), PostgreSQLDataDirectory: dir}
	identityFile, err := os.OpenFile(filepath.Join(dir, "postgresql.auto.conf"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identityFile.WriteString("\nclusterguard.node_id = '" + string(policy.PostgreSQLNodeID) + "'\n"); err != nil {
		t.Fatal(err)
	}
	if err := identityFile.Close(); err != nil {
		t.Fatal(err)
	}
	tool := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "postgres" {
			args = append([]string{"-D", dir}, args...)
		} else if name == "pg_controldata" {
			args = append([]string{dir}, args...)
		} else {
			args = append([]string{"--path", filepath.Join(dir, "pg_wal")}, args...)
		}
		return (OSCommandRunner{}).RunOrdered(ctx, filepath.Join(bin, name), args...)
	}
	e, err := inspectStoppedPostgreSQL(context.Background(), policy, tool)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Complete || !e.Fenced || e.SystemIdentifier == "" || e.Fingerprint == "" {
		t.Fatalf("incomplete evidence: %+v", e)
	}
	proof, err := verifyRecoveryWAL(context.Background(), policy, tool, model.RecoveryWALRequest{Fingerprint: e.Fingerprint, Timeline: e.Timeline, Start: e.Redo, End: e.Position})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(proof.Digest, "sha256:") {
		t.Fatalf("missing byte proof: %+v", proof)
	}
	_, err = verifyRecoveryWAL(context.Background(), policy, tool, model.RecoveryWALRequest{Fingerprint: "stale", Timeline: e.Timeline, Start: e.Redo, End: e.Position})
	if err == nil {
		t.Fatal("stale evidence accepted")
	}
}

func TestRecoveryControlRejectsCorruptEvidence(t *testing.T) {
	for _, value := range []string{"", "WARNING: possible byte ordering mismatch\nDatabase system identifier: 123", "CRC mismatch\nDatabase system identifier: 123"} {
		if _, err := parseRecoveryControl([]byte(value)); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
