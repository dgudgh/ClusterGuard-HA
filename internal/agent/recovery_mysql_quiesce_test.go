package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type recoveryIsolation struct{ safe bool }

type recoveryProtocolRoles struct {
	fakeRoleController
	quiesced  bool
	inspected bool
}

func (r *recoveryProtocolRoles) RecoveryQuiesce(context.Context, ClusterPolicy) (model.RecoveryEvidence, error) {
	r.quiesced = true
	return model.RecoveryEvidence{Complete: true, Fenced: true}, nil
}

func (r *recoveryProtocolRoles) RecoveryInspect(context.Context, ClusterPolicy) (model.RecoveryEvidence, error) {
	r.inspected = true
	if !r.quiesced {
		return model.RecoveryEvidence{}, errors.New("replication restarted")
	}
	return model.RecoveryEvidence{Complete: true, Fenced: true}, nil
}

func (r recoveryIsolation) IsolationStatus(context.Context, ClusterPolicy) (MySQLIsolationStatus, error) {
	return MySQLIsolationStatus{DatabaseReachable: true, ServiceRunning: true, ReadOnly: r.safe, SuperReadOnly: r.safe, RestartReadOnly: r.safe, PersistedReadOnly: r.safe}, nil
}

func TestRecoveryMySQLQuiescePreservesReceivedTransactions(t *testing.T) {
	for _, version := range []string{"8.0.21", "8.0.42", "8.4.6"} {
		t.Run(version, func(t *testing.T) {
			verb, err := recoveryMySQLReplicationVerb([]byte(version))
			if err != nil {
				t.Fatal(err)
			}
			const received = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:1-25"
			state := recoveryMySQLState{UUID: string(model.NewResourceID()), Executed: received, GTIDMode: "ON", Consistency: "ON", LogBin: 1, LogUpdates: 1, ReadOnly: 1, SuperReadOnly: 1}
			encoded, _ := json.Marshal(state)
			var commands []string
			query := func(_ context.Context, _ ClusterPolicy, sql string) ([]byte, error) {
				commands = append(commands, sql)
				switch sql {
				case "SELECT @@version":
					return []byte(version), nil
				case recoveryMySQLChannelsQuery:
					return []byte(`{"channels":1,"unsupported":0,"filters":0,"applier_errors":0}`), nil
				case recoveryMySQLReceivedQuery:
					return []byte(`{"rows":1,"running":0,"received":"` + received + `"}`), nil
				case "SELECT WAIT_FOR_EXECUTED_GTID_SET('" + received + "', 60)":
					return []byte("0"), nil
				case recoveryMySQLStateQuery:
					return encoded, nil
				case "STOP " + verb + " IO_THREAD", "START " + verb + " SQL_THREAD", "STOP " + verb + " SQL_THREAD", "XA RECOVER":
					return nil, nil
				default:
					t.Fatalf("unexpected SQL: %s", sql)
					return nil, nil
				}
			}
			p := ClusterPolicy{InstanceID: model.NewResourceID()}
			e, err := quiesceMySQLRecovery(context.Background(), p, recoveryIsolation{true}, query)
			if err != nil {
				t.Fatal(err)
			}
			if !e.Complete || !e.Fenced || e.GTIDExecuted != received || e.Fingerprint == "" {
				t.Fatalf("incomplete result: %+v", e)
			}
			want := []string{"SELECT @@version", recoveryMySQLChannelsQuery, "STOP " + verb + " IO_THREAD", recoveryMySQLReceivedQuery, "START " + verb + " SQL_THREAD", "SELECT WAIT_FOR_EXECUTED_GTID_SET('" + received + "', 60)", "STOP " + verb + " SQL_THREAD", recoveryMySQLStateQuery, "XA RECOVER", recoveryMySQLStateQuery}
			if !reflect.DeepEqual(commands, want) {
				t.Fatalf("unsafe order: %q", commands)
			}
		})
	}
}

func TestRecoveryMySQLQuiesceStopsApplierOnFailedDrain(t *testing.T) {
	for _, failure := range []string{"missing-status", "receiver-running", "bad-gtid", "start-failure", "timeout", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopped := false
			query := func(ctx context.Context, _ ClusterPolicy, sql string) ([]byte, error) {
				switch sql {
				case "SELECT @@version":
					return []byte("8.0.42"), nil
				case recoveryMySQLChannelsQuery:
					return []byte(`{"channels":1,"unsupported":0,"filters":0,"applier_errors":0}`), nil
				case "STOP REPLICA IO_THREAD":
					return nil, nil
				case recoveryMySQLReceivedQuery:
					if failure == "missing-status" {
						return []byte(`{"rows":0,"running":0,"received":null}`), nil
					}
					if failure == "receiver-running" {
						return []byte(`{"rows":1,"running":1,"received":""}`), nil
					}
					if failure == "bad-gtid" {
						return []byte(`{"rows":1,"running":0,"received":"'; SELECT 1;--"}`), nil
					}
					return []byte(`{"rows":1,"running":0,"received":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:1-25"}`), nil
				case "START REPLICA SQL_THREAD":
					if failure == "start-failure" {
						return nil, errors.New("applier unavailable")
					}
					return nil, nil
				case "STOP REPLICA SQL_THREAD":
					if ctx.Err() != nil {
						t.Fatal("cancelled parent prevented safety cleanup")
					}
					stopped = true
					return nil, nil
				}
				if strings.HasPrefix(sql, "SELECT WAIT_FOR_EXECUTED_GTID_SET") {
					if failure == "cancelled" {
						cancel()
						return nil, context.Canceled
					}
					return []byte("1"), nil
				}
				t.Fatalf("unexpected SQL: %s", sql)
				return nil, nil
			}
			e, err := quiesceMySQLRecovery(ctx, ClusterPolicy{}, recoveryIsolation{true}, query)
			if err == nil || e.Complete || !stopped {
				t.Fatalf("failed drain returned evidence or left applier: %+v %v stopped=%v", e, err, stopped)
			}
		})
	}
}

func TestRecoveryMySQLQuiesceRejectsUnsafePreconditionsWithoutSQLMutation(t *testing.T) {
	for _, channels := range []string{`{}`, `{"channels":1,"unsupported":0,"filters":null,"applier_errors":0}`, `{"channels":2,"unsupported":1,"filters":0,"applier_errors":0}`, `{"channels":1,"unsupported":0,"filters":1,"applier_errors":0}`, `{"channels":1,"unsupported":0,"filters":0,"applier_errors":1}`} {
		query := func(_ context.Context, _ ClusterPolicy, sql string) ([]byte, error) {
			switch sql {
			case "SELECT @@version":
				return []byte("8.0.42"), nil
			case recoveryMySQLChannelsQuery:
				return []byte(channels), nil
			}
			t.Fatalf("unsafe mutation: %s", sql)
			return nil, nil
		}
		if _, err := quiesceMySQLRecovery(context.Background(), ClusterPolicy{}, recoveryIsolation{true}, query); err == nil {
			t.Fatalf("accepted %s", channels)
		}
	}
	query := func(context.Context, ClusterPolicy, string) ([]byte, error) {
		t.Fatal("unfenced database was queried for relay drain")
		return nil, nil
	}
	if _, err := quiesceMySQLRecovery(context.Background(), ClusterPolicy{}, recoveryIsolation{}, query); err == nil {
		t.Fatal("accepted missing durable fence")
	}
}

func TestRecoveryQuiesceProtocolRequiresLeaseVIPFenceAndCurrentReplayEvidence(t *testing.T) {
	for _, condition := range []string{"missing-lease", "tampered-lease", "vip-owned", "success", "stale-replay"} {
		t.Run(condition, func(t *testing.T) {
			roles := &recoveryProtocolRoles{}
			vip := &fakeVIPController{owns: condition == "vip-owned"}
			service, policy, now := testAgentService(t, vip, roles)
			lease := model.NewResourceID()
			if condition == "missing-lease" {
				lease = ""
			}
			r := signedAgentRequest(t, Request{Command: CommandRecoveryQuiesce, ClusterID: policy.ClusterID, LeaseID: lease, ExpiresAt: now.Add(time.Minute)})
			if condition == "tampered-lease" {
				r.LeaseID = model.NewResourceID()
			}
			response := service.Handle(context.Background(), r)
			expected := condition == "success" || condition == "stale-replay"
			if (response.Status == StatusOK) != expected || roles.quiesced != expected {
				t.Fatalf("unsafe protocol response: %+v quiesced=%v", response, roles.quiesced)
			}
			if !expected {
				return
			}
			if condition == "stale-replay" {
				roles.quiesced = false
			}
			replayed := service.Handle(context.Background(), r)
			if !roles.inspected || (replayed.Status == StatusOK) != (condition == "success") {
				t.Fatalf("replayed stale quiescence: %+v", replayed)
			}
		})
	}
}
