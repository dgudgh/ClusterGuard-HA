package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/disaster"
	"clusterguard.io/ha/pkg/model"
	"clusterguard.io/ha/pkg/redact"
)

type recoveryMySQLFixture struct {
	t          *testing.T
	docker     string
	network    string
	password   string
	containers []string
	policies   []ClusterPolicy
	roles      []*DockerMySQLRoleController
}

// This fixture only creates fresh, unexposed containers and anonymous data
// volumes. It never takes an existing service, data directory or credential.
func newRecoveryMySQLFixture(t *testing.T) *recoveryMySQLFixture {
	t.Helper()
	image := os.Getenv("CG_MYSQL_RECOVERY_TEST_IMAGE")
	if image == "" {
		t.Skip("CG_MYSQL_RECOVERY_TEST_IMAGE required for isolated real MySQL recovery tests")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	f := &recoveryMySQLFixture{t: t, docker: docker, password: hex.EncodeToString(secret)}
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	f.network = "cg-recovery-test-" + string(model.NewResourceID())
	t.Cleanup(func() {
		for _, container := range f.containers {
			if _, err := f.command("", "rm", "--force", "--volumes", container); err != nil {
				t.Errorf("remove isolated recovery fixture: %v", err)
			}
		}
		if _, err := f.command("", "network", "rm", f.network); err != nil {
			t.Errorf("remove isolated recovery network: %v", err)
		}
	})
	// Pin a locally available image ID and disallow implicit image downloads.
	imageID := f.mustDocker("image", "inspect", "--format", "{{.Id}}", image)
	uid, err := strconv.Atoi(f.mustDocker("run", "--rm", "--pull=never", "--network=none", "--entrypoint", "id", imageID, "-u", "mysql"))
	if err != nil {
		t.Fatal("fixture image lacks a numeric MySQL user")
	}
	f.mustDocker("network", "create", "--internal", "--label", "clusterguard.test=recovery", f.network)
	clusterID := model.NewResourceID()
	for index := 0; index < 3; index++ {
		instanceID := model.NewResourceID()
		name := fmt.Sprintf("%s-%d", f.network, index+1)
		directory := filepath.Join(root, fmt.Sprint(index))
		config := filepath.Join(directory, "config")
		credentials := filepath.Join(directory, "credentials")
		for _, path := range []string{config, credentials} {
			if err := os.MkdirAll(path, 0755); err != nil {
				t.Fatal(err)
			}
		}
		// Random fixture-only secrets go through a private mount, not argv.
		for name, data := range map[string]string{
			"root-password": f.password,
			"client.cnf":    "[client]\nuser=root\npassword=" + f.password + "\n",
		} {
			if err := os.WriteFile(filepath.Join(credentials, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chown(filepath.Join(credentials, "root-password"), uid, -1); err != nil {
			t.Fatal(err)
		}
		policy := ClusterPolicy{
			ClusterID: clusterID, InstanceID: instanceID, Engine: model.EngineMySQL,
			RuntimeKind: model.RuntimeDocker, DockerSwarmService: name,
			DockerMySQLBinary: "mysql", MySQLPort: 3306,
			MySQLDefaultsFile: "/run/cg-fixture/client.cnf", DockerFenceFile: filepath.Join(config, "fence.cnf"),
		}
		if err := writeDockerRestartFence(policy.DockerFenceFile, false); err != nil {
			t.Fatal(err)
		}
		container := f.mustDocker("run", "--detach", "--pull=never", "--name", name,
			"--network", f.network, "--network-alias", fmt.Sprintf("mysql%d", index+1),
			"--memory", "768m", "--cpus", "1", "--label", "clusterguard.test=recovery",
			"--label", "com.docker.swarm.service.name="+name,
			"--label", "clusterguard.cluster_id="+string(clusterID),
			"--label", "clusterguard.instance_id="+string(instanceID),
			"--volume", config+":/etc/mysql/conf.d:ro,Z",
			"--volume", credentials+":/run/cg-fixture:ro,Z",
			"--env", "MYSQL_ROOT_PASSWORD_FILE=/run/cg-fixture/root-password", "--env", "MYSQL_ROOT_HOST=%",
			imageID, "--server-id="+fmt.Sprint(index+1), "--gtid-mode=ON", "--enforce-gtid-consistency=ON",
			"--log-bin=binlog", "--log-replica-updates=ON", "--skip-replica-start",
			"--innodb-buffer-pool-size=64M", "--innodb-redo-log-capacity=32M", "--max-connections=30")
		f.containers = append(f.containers, container)
		f.policies = append(f.policies, policy)
		f.roles = append(f.roles, NewDockerMySQLRoleController(OSCommandRunner{}, docker, filepath.Join(directory, "state")))
		f.waitQuery(index, "SELECT 1", "1", 90*time.Second)
		version := f.query(index, "SELECT @@version")
		if !strings.HasPrefix(version, "8.0.") {
			t.Fatalf("fixture currently qualifies MySQL 8.0, got %s", version)
		}
		// The image bootstrap generates unrelated local account GTIDs. Clear
		// those only in these new, empty fixture volumes before making replicas.
		f.query(index, "RESET MASTER")
	}
	f.query(0, "CREATE DATABASE recovery_fixture; CREATE TABLE recovery_fixture.transactions(id INT PRIMARY KEY, branch VARCHAR(32)); INSERT INTO recovery_fixture.transactions VALUES(1, 'common')")
	for _, index := range []int{1, 2} {
		f.query(index, "CHANGE REPLICATION SOURCE TO SOURCE_HOST='mysql1', SOURCE_USER='root', SOURCE_PASSWORD='"+f.password+"', SOURCE_AUTO_POSITION=1, GET_SOURCE_PUBLIC_KEY=1; START REPLICA")
		f.waitQuery(index, "SELECT COUNT(*) FROM recovery_fixture.transactions", "1", 30*time.Second)
	}
	t.Log("three isolated MySQL 8.0 members have actual GTID replication")
	return f
}

func (f *recoveryMySQLFixture) command(input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.docker, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("isolated MySQL fixture command: %w: %s", err, redact.Bounded(strings.ReplaceAll(string(out), f.password, "[REDACTED]"), 512))
	}
	return strings.TrimSpace(string(out)), nil
}

func (f *recoveryMySQLFixture) mustDocker(args ...string) string {
	f.t.Helper()
	out, err := f.command("", args...)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *recoveryMySQLFixture) tryQuery(index int, sql string) (string, error) {
	return f.command(sql+";\n", "exec", "--interactive", f.containers[index], "mysql", "--defaults-file=/run/cg-fixture/client.cnf", "--protocol=tcp", "--host=127.0.0.1", "--batch", "--raw", "--skip-column-names")
}

func (f *recoveryMySQLFixture) query(index int, sql string) string {
	f.t.Helper()
	out, err := f.tryQuery(index, sql)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *recoveryMySQLFixture) waitQuery(index int, sql, expected string, limit time.Duration) {
	f.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if out, err := f.tryQuery(index, sql); err == nil && out == expected {
			return
		}
		if running := f.mustDocker("inspect", "--format", "{{.State.Running}}", f.containers[index]); running != "true" {
			f.t.Fatalf("isolated MySQL member %d stopped unexpectedly", index+1)
		}
		time.Sleep(200 * time.Millisecond)
	}
	f.t.Fatalf("isolated MySQL member %d did not reach expected state", index+1)
}

func (f *recoveryMySQLFixture) fence(index int, readOnly bool) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := f.roles[index].PersistReadOnly(ctx, f.policies[index], readOnly); err != nil {
		f.t.Fatal(err)
	}
}

func (f *recoveryMySQLFixture) inspectAll() ([]model.DatabaseInstance, []model.RecoveryEvidence) {
	f.t.Helper()
	var members []model.DatabaseInstance
	var evidence []model.RecoveryEvidence
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for index, policy := range f.policies {
		e, err := f.roles[index].RecoveryInspect(ctx, policy)
		if err != nil {
			f.t.Fatal(err)
		}
		members = append(members, model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: policy.InstanceID}, ClusterID: policy.ClusterID, Engine: model.EngineMySQL, EngineIdentity: map[string]string{"server_uuid": e.NativeID}})
		evidence = append(evidence, e)
	}
	return members, evidence
}

func TestRecoveryMySQLActualThreeNodeRelayDrainAndSelection(t *testing.T) {
	f := newRecoveryMySQLFixture(t)
	f.query(1, "STOP REPLICA SQL_THREAD")
	f.query(0, "INSERT INTO recovery_fixture.transactions VALUES(2, 'received-not-applied')")
	executed := f.query(0, "SELECT @@global.gtid_executed")
	f.waitQuery(1, "SELECT GTID_SUBSET('"+executed+"', RECEIVED_TRANSACTION_SET) FROM performance_schema.replication_connection_status", "1", 20*time.Second)
	if rows := f.query(1, "SELECT COUNT(*) FROM recovery_fixture.transactions"); rows != "1" {
		t.Fatal("fixture must have an unapplied received transaction")
	}
	for i := range f.policies {
		f.fence(i, true)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	for index, policy := range f.policies {
		if _, err := f.roles[index].RecoveryQuiesce(ctx, policy); err != nil {
			t.Fatalf("quiesce actual MySQL member %d: %v", index+1, err)
		}
		if f.query(index, "SELECT COUNT(*) FROM recovery_fixture.transactions") != "2" {
			t.Fatal("recovery lost a received transaction")
		}
		if _, err := f.tryQuery(index, "INSERT INTO recovery_fixture.transactions VALUES(3, 'must-be-fenced')"); err == nil {
			t.Fatal("durable recovery fence allowed a business write")
		}
	}
	members, evidence := f.inspectAll()
	if _, err := disaster.Select(members, evidence, nil); err != nil {
		t.Fatal(err)
	}
	t.Log("actual relay transaction drained; all members are write-fenced with identical recoverable GTIDs")

	t.Run("persisted_writable_override_blocks_evidence", func(t *testing.T) {
		copy := *f
		copy.t = t
		f := &copy
		f.query(1, "SET PERSIST_ONLY super_read_only=OFF; SET PERSIST_ONLY read_only=OFF")
		defer f.fence(1, true)
		if f.query(1, "SELECT @@read_only, @@super_read_only") != "1\t1" {
			t.Fatal("fixture must remain read-only at runtime")
		}
		if _, err := f.roles[1].RecoveryInspect(context.Background(), f.policies[1]); err == nil || !strings.Contains(err.Error(), "persisted writable overrides") {
			t.Fatalf("unsafe restart settings must block recovery evidence: %v", err)
		}
	})

	t.Run("restart_keeps_write_fence", func(t *testing.T) {
		copy := *f
		copy.t = t
		f := &copy
		f.mustDocker("restart", "--time", "20", f.containers[1])
		f.waitQuery(1, "SELECT @@read_only, @@super_read_only", "1\t1", 40*time.Second)
		if _, err := f.roles[1].RecoveryInspect(context.Background(), f.policies[1]); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("independent_commits_block_selection", func(t *testing.T) {
		copy := *f
		copy.t = t
		f := &copy
		for _, index := range []int{1, 2} {
			f.fence(index, false)
			f.query(index, fmt.Sprintf("INSERT INTO recovery_fixture.transactions VALUES(%d, 'independent')", 10+index))
			f.fence(index, true)
		}
		members, evidence := f.inspectAll()
		if _, err := disaster.Select(members, evidence, nil); err == nil {
			t.Fatal("independently committed MySQL branches must block automatic selection")
		}
	})
	t.Run("prepared_xa_blocks_evidence", func(t *testing.T) {
		copy := *f
		copy.t = t
		f := &copy
		f.fence(0, false)
		f.query(0, "XA START 'cg_recovery_fixture'; INSERT INTO recovery_fixture.transactions VALUES(20, 'prepared'); XA END 'cg_recovery_fixture'; XA PREPARE 'cg_recovery_fixture'")
		f.fence(0, true)
		if _, err := f.roles[0].RecoveryInspect(context.Background(), f.policies[0]); err == nil || !strings.Contains(err.Error(), "prepared XA") {
			t.Fatalf("prepared XA must block recovery evidence: %v", err)
		}
	})
}
