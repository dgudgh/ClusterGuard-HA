package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func TestDisasterRebuildRejectsLostAuthorityGuardAndNativeIdentity(t *testing.T) {
	for _, reason := range []string{"authority", "guard", "source-identity", "recipient-identity"} {
		t.Run(reason, func(t *testing.T) {
			r := newRecoveryBinlogRunner()
			r.oldPrevious = r.source
			primary, target := recoveryBinlogMembers()
			if reason == "source-identity" {
				primary.EngineIdentity = model.EngineIdentity{"server_uuid": recoveryRecipientUUID}
			}
			if reason == "recipient-identity" {
				target.EngineIdentity = model.EngineIdentity{"server_uuid": recoverySourceUUID}
			}
			err := (DisasterExecutor{Runner: r}).Rebuild(context.Background(), target, primary, adapter.OperationCredentials{
				Administrative: adapter.Credentials{Username: "admin", Password: "secret"}, Replication: adapter.Credentials{Username: "repl", Password: "secret"}},
				func() error {
					if reason == "authority" {
						return errors.New("authority lost")
					}
					return nil
				},
				func() error {
					if reason == "guard" {
						return errors.New("guard absent")
					}
					return nil
				}, func() error { return nil })
			if err == nil || r.cloneCount != 0 || !r.recipientFenced {
				t.Fatalf("lost %s admitted clone or removed fence: %v", reason, err)
			}
		})
	}
}

type reusedBinlogRunner struct {
	*recoveryBinlogRunner
	version string
}

type lateRevocationRunner struct {
	*recoveryBinlogRunner
	revoked bool
}

func (r *lateRevocationRunner) Exec(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, sql string) error {
	err := r.recoveryBinlogRunner.Exec(ctx, endpoint, credentials, sql)
	if sql == "SET GLOBAL super_read_only=OFF" {
		r.revoked = true
	}
	return err
}

func TestDisasterCloneRevalidatesAfterTemporaryFenceChange(t *testing.T) {
	r := &lateRevocationRunner{recoveryBinlogRunner: newRecoveryBinlogRunner()}
	r.oldPrevious = r.source
	primary, target := recoveryBinlogMembers()
	err := (DisasterExecutor{Runner: r}).Rebuild(context.Background(), target, primary, adapter.OperationCredentials{
		Administrative: adapter.Credentials{Username: "admin", Password: "secret"}, Replication: adapter.Credentials{Username: "repl", Password: "secret"}},
		func() error {
			if r.revoked {
				return errors.New("authority revoked after temporary fence change")
			}
			return nil
		},
		func() error { return nil }, func() error { return nil })
	if err == nil || r.cloneCount != 0 || !r.recipientFenced {
		t.Fatalf("late revocation admitted clone or lost the fence: %v", err)
	}
}

func (r reusedBinlogRunner) Query(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, sql string) ([]Row, error) {
	rows, err := r.recoveryBinlogRunner.Query(ctx, endpoint, credentials, sql)
	if err == nil && len(rows) == 1 {
		if sql == identityQuery {
			rows[0]["version"] = r.version
		}
		if (sql == "SHOW MASTER STATUS" || sql == "SHOW BINARY LOG STATUS") && r.flushCount == 1 {
			rows[0]["File"] = "binlog.000001"
		}
	}
	return rows, err
}

func TestDisasterReusedFilenameAnd84StatusRemainQualified(t *testing.T) {
	for _, version := range []string{"8.0.44", "8.4.10"} {
		t.Run(version, func(t *testing.T) {
			r := reusedBinlogRunner{recoveryBinlogRunner: newRecoveryBinlogRunner(), version: version}
			primary, _ := recoveryBinlogMembers()
			if err := (DisasterExecutor{Runner: r}).StartPrimary(context.Background(), primary, adapter.Credentials{}, r.source, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if r.flushCount != 2 {
				t.Fatal("reused filename did not require a second qualified boundary")
			}
			query := "SHOW MASTER STATUS"
			if version == "8.4.10" {
				query = "SHOW BINARY LOG STATUS"
			}
			if !strings.Contains(strings.Join(r.queries, "\n"), query) {
				t.Fatal("qualified status dialect was not used")
			}
		})
	}
}

type replicationNativeErrorRunner struct{ calls int }

func (r *replicationNativeErrorRunner) Query(ctx context.Context, _ adapter.Endpoint, _ adapter.Credentials, _ string) ([]Row, error) {
	r.calls++
	if r.calls > 1 {
		<-ctx.Done()
		return nil, &QueryError{Err: ctx.Err()}
	}
	return []Row{{"Source_UUID": recoverySourceUUID, "Replica_IO_Running": "No", "Replica_SQL_Running": "Yes", "Seconds_Behind_Source": "NULL", "Last_IO_Error": "fatal source error 1236; SOURCE_PASSWORD='private-fixture-value'", "Last_SQL_Error": ""}}, nil
}

func TestReplicationWaitRetainsSanitizedNativeError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	runner := &replicationNativeErrorRunner{}
	_, err := waitForFollowerReplicationHealthy(ctx, runner, adapter.Endpoint{}, adapter.Credentials{}, recoverySourceUUID, 0)
	if err == nil || !strings.Contains(err.Error(), "1236") || !strings.Contains(err.Error(), "[REDACTED]") || strings.Contains(err.Error(), "private-fixture-value") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("native error or cancellation lost, or credential leaked: %v", err)
	}
}
