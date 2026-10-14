package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const recoverySourceUUID = "913514cc-c60b-11f1-b9e3-000c29938c48"
const recoveryRecipientUUID = "3e289a54-c60c-11f1-84d2-000c29bb1d81"

type recoveryBinlogRunner struct {
	source, recipient, purged string
	logError                  error
	cloneFailure              bool
	cloneCount, flushCount    int
	recipientFenced           bool
	changeHistoryOnFlush      bool
	reusedFirstRotation       bool
	oldPrevious               string
	queries, statements       []string
}

func newRecoveryBinlogRunner() *recoveryBinlogRunner {
	return &recoveryBinlogRunner{source: recoverySourceUUID + ":1-17", recipient: recoverySourceUUID + ":1-3", recipientFenced: true}
}

func (r *recoveryBinlogRunner) Query(ctx context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, sql string) ([]Row, error) {
	if err := ctx.Err(); err != nil {
		return nil, &QueryError{Err: err}
	}
	r.queries = append(r.queries, sql)
	switch {
	case sql == identityQuery:
		uuid, history, fence := recoverySourceUUID, r.source, "1"
		if endpoint.Port == 3307 {
			uuid, history, fence = recoveryRecipientUUID, r.recipient, boolString(r.recipientFenced)
		}
		return []Row{{"server_uuid": uuid, "hostname": "mysql", "port": fmt.Sprint(endpoint.Port), "server_id": "1", "version": "8.0.44", "read_only": "1", "super_read_only": fence, "gtid_mode": "ON", "gtid_executed": history, "gtid_purged": r.purged, "log_bin": "1"}}, nil
	case sql == "SHOW BINARY LOGS":
		return []Row{{"Log_name": "binlog.000001", "File_size": "500"}, {"Log_name": "binlog.000002", "File_size": "200"}}, nil
	case sql == "SHOW MASTER STATUS" || sql == "SHOW BINARY LOG STATUS":
		file := "binlog.000001"
		if r.flushCount > 0 && !(r.reusedFirstRotation && r.flushCount == 1) {
			file = "binlog.000002"
		}
		return []Row{{"File": file, "Position": "200", "Executed_Gtid_Set": r.source}}, nil
	case strings.HasPrefix(sql, "SHOW BINLOG EVENTS IN 'binlog.000001'"):
		if r.logError != nil {
			return nil, r.logError
		}
		return []Row{{"Log_name": "binlog.000001", "Event_type": "Format_desc", "Pos": "4"}, {"Log_name": "binlog.000001", "Event_type": "Previous_gtids", "Info": r.oldPrevious}}, nil
	case strings.HasPrefix(sql, "SHOW BINLOG EVENTS IN 'binlog.000002'"):
		return []Row{{"Log_name": "binlog.000002", "Event_type": "Format_desc", "Pos": "4"}, {"Log_name": "binlog.000002", "Event_type": "Previous_gtids", "Info": r.source}}, nil
	case strings.Contains(sql, "INFORMATION_SCHEMA.PLUGINS"):
		return []Row{{"state": "ACTIVE"}}, nil
	case sql == "SELECT ID, BEGIN_TIME, STATE FROM performance_schema.clone_status":
		return nil, nil
	case sql == "SELECT ID, BEGIN_TIME, STATE, ERROR_NO FROM performance_schema.clone_status":
		if r.cloneFailure {
			return []Row{{"ID": "1", "BEGIN_TIME": "now", "STATE": "Failed", "ERROR_NO": "3862"}}, nil
		}
		return []Row{{"ID": "1", "BEGIN_TIME": "now", "STATE": "Completed", "ERROR_NO": "0"}}, nil
	case sql == replicaStatusQuery || sql == slaveStatusQuery:
		if endpoint.Port == 3306 {
			return nil, nil
		}
		return []Row{{"Source_UUID": recoverySourceUUID, "Replica_IO_Running": "Yes", "Replica_SQL_Running": "Yes", "Seconds_Behind_Source": "0"}}, nil
	case strings.HasPrefix(sql, "SELECT WAIT_FOR_EXECUTED_GTID_SET("):
		return []Row{{"caught_up": "0"}}, nil
	}
	return nil, fmt.Errorf("unexpected recovery query: %s", sql)
}

func (r *recoveryBinlogRunner) Exec(_ context.Context, _ adapter.Endpoint, _ adapter.Credentials, sql string) error {
	r.statements = append(r.statements, sql)
	switch {
	case sql == "FLUSH BINARY LOGS":
		r.flushCount++
		if r.changeHistoryOnFlush {
			r.source = recoverySourceUUID + ":1-18"
		}
	case sql == "SET GLOBAL super_read_only=OFF":
		r.recipientFenced = false
	case strings.Contains(sql, "SET PERSIST super_read_only=ON"):
		r.recipientFenced = true
	case strings.HasPrefix(sql, "CLONE INSTANCE FROM"):
		r.cloneCount++
		if !r.cloneFailure {
			r.recipient = r.source
		}
	case sql == "START REPLICA":
		r.recipient = r.source
	}
	return nil
}

func recoveryBinlogMembers() (model.DatabaseInstance, model.DatabaseInstance) {
	return model.DatabaseInstance{IPAddress: "127.0.0.1", Port: 3306, EngineIdentity: model.EngineIdentity{"server_uuid": recoverySourceUUID}},
		model.DatabaseInstance{IPAddress: "127.0.0.1", Port: 3307, EngineIdentity: model.EngineIdentity{"server_uuid": recoveryRecipientUUID}}
}

func TestDisasterRebuildUsesActualBinlogAvailability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*recoveryBinlogRunner)
		clones    int
		success   bool
	}{
		{"missing-path-with-unchanged-purged", func(r *recoveryBinlogRunner) {
			r.logError = &QueryError{Code: 29, Output: "File 'binlog.000001' not found (OS errno 2 - No such file)"}
		}, 1, true},
		{"recreated-log-header-is-not-old-history", func(r *recoveryBinlogRunner) { r.oldPrevious = r.source }, 1, true},
		{"readable-history-retains-incremental", func(*recoveryBinlogRunner) {}, 0, true},
		{"purged-missing-history-clones", func(r *recoveryBinlogRunner) { r.purged = recoverySourceUUID + ":1-8" }, 1, true},
		{"equal-history-needs-no-old-log", func(r *recoveryBinlogRunner) { r.recipient = r.source; r.logError = &QueryError{Code: 29} }, 0, true},
		{"permission-error-does-not-clone", func(r *recoveryBinlogRunner) { r.logError = &QueryError{Code: 1227} }, 0, false},
		{"query-error-does-not-clone", func(r *recoveryBinlogRunner) { r.logError = &QueryError{Code: 2003} }, 0, false},
		{"unqualified-file-error-does-not-clone", func(r *recoveryBinlogRunner) {
			r.logError = &QueryError{Code: 29, Output: "OS errno 13 - Permission denied"}
		}, 0, false},
		{"errant-history-never-clones", func(r *recoveryBinlogRunner) { r.recipient += "," + recoveryRecipientUUID + ":1" }, 0, false},
		{"clone-failure-retains-write-fence", func(r *recoveryBinlogRunner) { r.oldPrevious = r.source; r.cloneFailure = true }, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRecoveryBinlogRunner()
			tc.configure(r)
			primary, target := recoveryBinlogMembers()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := (DisasterExecutor{Runner: r}).Rebuild(ctx, target, primary, adapter.OperationCredentials{
				Administrative: adapter.Credentials{Username: "admin", Password: "secret"},
				Replication:    adapter.Credentials{Username: "repl", Password: "secret"}},
				func() error { return ctx.Err() }, func() error { return nil }, func() error { return nil })
			if (err == nil) != tc.success {
				t.Fatalf("rebuild result: %v", err)
			}
			if r.cloneCount != tc.clones {
				t.Fatalf("clones=%d, expected %d", r.cloneCount, tc.clones)
			}
			if !r.recipientFenced {
				t.Fatal("recipient lost its read-only fence")
			}
		})
	}
}

func TestDisasterFreshBinlogPreservesFrozenHistoryAndAuthority(t *testing.T) {
	for _, tc := range []struct {
		name             string
		revoked, changed bool
		wantFlush        int
		success          bool
	}{
		{"new-readable-boundary", false, false, 1, true},
		{"revoked-before-rotation", true, false, 0, false},
		{"changed-frozen-history", false, true, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRecoveryBinlogRunner()
			r.changeHistoryOnFlush = tc.changed
			primary, _ := recoveryBinlogMembers()
			err := (DisasterExecutor{Runner: r}).StartPrimary(context.Background(), primary, adapter.Credentials{}, r.source, func() error {
				if tc.revoked {
					return errors.New("authority revoked")
				}
				return nil
			})
			if (err == nil) != tc.success || r.flushCount != tc.wantFlush {
				t.Fatalf("result=%v flushes=%d", err, r.flushCount)
			}
		})
	}
}
