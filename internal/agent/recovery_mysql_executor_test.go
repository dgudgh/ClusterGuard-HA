package agent

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"clusterguard.io/ha/pkg/redact"
)

type recoveryMySQLExecutorRunner struct {
	f     *recoveryMySQLFixture
	names map[string]string
}

func (r recoveryMySQLExecutorRunner) run(ctx context.Context, container, sql string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.f.docker, "exec", "--interactive", container, "mysql", "--defaults-file=/run/cg-fixture/client.cnf", "--batch")
	cmd.Stdin = strings.NewReader(sql + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("fixture SQL failed: %s", redact.Bounded(string(out), 512, r.f.password))
	}
	return string(out), nil
}

func (r recoveryMySQLExecutorRunner) Query(ctx context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, sql string) ([]mysql.Row, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	container, ok := r.names[endpoint.IPAddress]
	if !ok {
		return nil, fmt.Errorf("unregistered fixture endpoint")
	}
	out, err := r.run(ctx, container, sql)
	if err != nil {
		return nil, err
	}
	reader := csv.NewReader(strings.NewReader(out))
	reader.Comma = '\t'
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []mysql.Row
	for {
		fields, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(fields) != len(header) {
			return nil, fmt.Errorf("unexpected fixture columns")
		}
		row := mysql.Row{}
		for i, k := range header {
			row[k] = strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t").Replace(fields[i])
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func (r recoveryMySQLExecutorRunner) Exec(ctx context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, sql string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	container, ok := r.names[endpoint.IPAddress]
	if !ok {
		return fmt.Errorf("unregistered fixture endpoint")
	}
	_, err := r.run(ctx, container, sql)
	return err
}

func TestRecoveryMySQLActualExecutorRebuild(t *testing.T) {
	f := newRecoveryMySQLFixture(t)
	// Only this disposable recipient misses the new commit. Purging the
	// donor's old binlog then makes a physical clone necessary, not optional.
	f.query(1, "STOP REPLICA IO_THREAD")
	f.query(0, "INSERT INTO recovery_fixture.transactions VALUES(2,'clone-required')")
	f.query(0, "FLUSH BINARY LOGS; PURGE BINARY LOGS BEFORE DATE_ADD(NOW(), INTERVAL 1 SECOND)")
	for i := range f.policies {
		f.fence(i, true)
	}
	runner := recoveryMySQLExecutorRunner{f: f, names: map[string]string{}}
	members := make([]model.DatabaseInstance, 3)
	for i, p := range f.policies {
		ip := f.mustDocker("inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", f.containers[i])
		runner.names[ip] = f.containers[i]
		members[i] = model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: p.InstanceID}, ClusterID: p.ClusterID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": f.query(i, "SELECT @@server_uuid")}, IPAddress: ip, Port: 3306}
	}
	executor := mysql.DisasterExecutor{Runner: runner}
	credentials := adapter.OperationCredentials{Administrative: adapter.Credentials{Username: "root", Password: f.password}, Replication: adapter.Credentials{Username: "root", Password: f.password}}
	id := model.NewResourceID()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	authorize := func() error { return ctx.Err() }
	for i, p := range f.policies {
		if err := f.roles[i].RecoveryReplicaPrepare(ctx, p, id); err != nil {
			t.Fatal(err)
		}
		guard := func() error { return f.roles[i].RecoveryReplicaVerify(ctx, p, id) }
		if err := executor.Prepare(ctx, members[i], credentials.Administrative, authorize, guard); err != nil {
			t.Fatal(err)
		}
		if err := f.roles[i].RecoveryReplicaRelease(ctx, p, id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.roles[i].RecoveryQuiesce(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	frozenGTID := f.query(0, "SELECT @@global.gtid_executed")
	if err := executor.StartPrimary(ctx, members[0], credentials.Administrative, frozenGTID, authorize); err != nil {
		t.Fatal(err)
	}
	p := f.policies[1]
	if err := f.roles[1].RecoveryReplicaPrepare(ctx, p, id); err != nil {
		t.Fatal(err)
	}
	guard := func() error { return f.roles[1].RecoveryReplicaVerify(ctx, p, id) }
	restart := func() error {
		deadline := time.Now().Add(20 * time.Second)
		for {
			if f.mustDocker("inspect", "--format", "{{.State.Running}}", f.containers[1]) != "true" {
				f.mustDocker("start", f.containers[1])
			}
			if _, err := f.tryQuery(1, "SELECT 1"); err == nil {
				return f.roles[1].PersistReadOnly(ctx, p, true)
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("fixture restart did not become reachable")
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	if err := executor.Rebuild(ctx, members[1], members[0], credentials, authorize, guard, restart); err != nil {
		t.Fatal(err)
	}
	if got := f.query(1, "SELECT STATE FROM performance_schema.clone_status"); got != "Completed" {
		t.Fatalf("physical clone was not exercised: %s", got)
	}
	if got := f.query(1, "SELECT COUNT(*) FROM recovery_fixture.transactions"); got != "2" {
		t.Fatal("rebuild lost latest business commit")
	}
	if _, err := executor.VerifyGuardedPrimary(ctx, members[0], credentials.Administrative, frozenGTID); err != nil {
		t.Fatal(err)
	}
	if err := f.roles[1].RecoveryReplicaRelease(ctx, p, id); err != nil {
		t.Fatal(err)
	}
	t.Log("actual recovery executor performed required physical clone, verified GTID and recipient identity, and preserved both business commits")
}
