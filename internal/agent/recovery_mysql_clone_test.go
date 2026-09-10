package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestRecoveryMySQLActualGuardedClone(t *testing.T) {
	f := newRecoveryMySQLFixture(t)
	f.query(0, "CREATE USER 'recovery_business'@'%' IDENTIFIED BY '"+f.password+"'; GRANT SELECT,INSERT ON recovery_fixture.* TO 'recovery_business'@'%'")
	for _, i := range []int{1, 2} {
		f.waitQuery(i, "SELECT COUNT(*) FROM mysql.user WHERE User='recovery_business'", "1", 20*time.Second)
	}
	for i := range f.policies {
		f.fence(i, true)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for i, p := range f.policies {
		if _, err := f.roles[i].RecoveryQuiesce(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	task := model.NewResourceID()
	if err := f.roles[1].RecoveryReplicaPrepare(ctx, f.policies[1], task); err != nil {
		t.Fatal(err)
	}
	_, err := f.command("SELECT 1;\n", "exec", "--interactive", f.containers[1], "mysql", "--defaults-file=/run/cg-fixture/client.cnf", "--protocol=tcp", "--host=127.0.0.1", "--user=recovery_business", "--batch")
	if err == nil || !strings.Contains(err.Error(), "3032") {
		t.Fatalf("offline guard did not reject the business account: %v", err)
	}
	for _, i := range []int{0, 1} {
		if err := f.roles[i].RecoveryReplicaPrepare(ctx, f.policies[i], task); err != nil {
			t.Fatal(err)
		}
		f.query(i, "SET GLOBAL super_read_only=OFF")
		if _, err := f.tryQuery(i, "SET SESSION sql_log_bin=0; INSTALL PLUGIN clone SONAME 'mysql_clone.so'"); err != nil {
			t.Fatalf("install clone behind offline-mode guard on member %d: %v", i, err)
		}
		f.fence(i, true)
	}
	if err := f.roles[0].RecoveryReplicaRelease(ctx, f.policies[0], task); err != nil {
		t.Fatal(err)
	}
	uuid := f.query(1, "SELECT @@server_uuid")
	f.query(1, "SET GLOBAL clone_valid_donor_list='mysql1:3306'")
	// Clone's internal writes run only behind the independently tested
	// offline-mode guard. The donor stays durably super-read-only.
	f.query(1, "SET GLOBAL super_read_only=OFF")
	if err := f.roles[1].RecoveryReplicaVerify(ctx, f.policies[1], task); err != nil {
		t.Fatal(err)
	}
	_, cloneErr := f.tryQuery(1, "CLONE INSTANCE FROM 'root'@'mysql1':3306 IDENTIFIED BY '"+f.password+"' REQUIRE SSL")
	if cloneErr != nil {
		t.Logf("clone command ended before restart verification: %v", cloneErr)
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		if state := f.mustDocker("inspect", "--format", "{{.State.Running}}", f.containers[1]); state != "true" {
			f.mustDocker("start", f.containers[1])
		}
		if out, err := f.tryQuery(1, "SELECT STATE FROM performance_schema.clone_status"); err == nil && out == "Completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("clone was not verified complete: %v", cloneErr)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if got := f.query(1, "SELECT @@server_uuid"); got != uuid {
		t.Fatal("physical reconstruction changed recipient native UUID")
	}
	if got := f.query(1, "SELECT @@global.read_only,@@global.super_read_only"); got != "1\t1" {
		t.Fatalf("clone restarted without a read-only fence: %s", got)
	}
	if err := f.roles[1].RecoveryReplicaVerify(ctx, f.policies[1], task); err != nil {
		t.Fatal(err)
	}
	f.query(1, "CHANGE REPLICATION SOURCE TO SOURCE_HOST='mysql1',SOURCE_USER='root',SOURCE_PASSWORD='"+f.password+"',SOURCE_AUTO_POSITION=1,GET_SOURCE_PUBLIC_KEY=1; START REPLICA")
	f.waitQuery(1, "SELECT SERVICE_STATE FROM performance_schema.replication_connection_status", "ON", 20*time.Second)
	if f.query(1, "SELECT @@global.gtid_executed") != f.query(0, "SELECT @@global.gtid_executed") {
		t.Fatal("cloned replica GTID does not match the authoritative donor")
	}
	if err := f.roles[1].RecoveryReplicaRelease(ctx, f.policies[1], task); err != nil {
		t.Fatal(err)
	}
	if f.query(1, "SELECT COUNT(*) FROM recovery_fixture.transactions") != "1" {
		t.Fatal("clone lost business rows")
	}
	t.Log("physical clone completed with business access blocked, recipient UUID preserved, restart fenced, and GTID replication verified")
}
