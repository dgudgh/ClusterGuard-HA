package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestMySQLRecoveryEvidenceRejectsUnsafeRuntime(t *testing.T) {
	valid := recoveryMySQLState{UUID: string(model.NewResourceID()), GTIDMode: "ON", Consistency: "ON", LogBin: 1, LogUpdates: 1, ReadOnly: 1, SuperReadOnly: 1, Executed: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:1-20"}
	data, _ := json.Marshal(valid)
	if _, err := parseRecoveryMySQLState(data); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*recoveryMySQLState){
		func(s *recoveryMySQLState) { s.UUID = "unknown" },
		func(s *recoveryMySQLState) { s.GTIDMode = "OFF" },
		func(s *recoveryMySQLState) { s.Consistency = "WARN" },
		func(s *recoveryMySQLState) { s.LogBin = 0 },
		func(s *recoveryMySQLState) { s.LogUpdates = 0 },
		func(s *recoveryMySQLState) { s.ReadOnly = 0 },
		func(s *recoveryMySQLState) { s.SuperReadOnly = 0 },
		func(s *recoveryMySQLState) { s.ActiveReceivers = 1 },
		func(s *recoveryMySQLState) { s.ActiveAppliers = 1 },
		func(s *recoveryMySQLState) { s.PendingRelay = 1 },
		func(s *recoveryMySQLState) { s.UnsafeRestartOverrides = 1 },
		func(s *recoveryMySQLState) { s.UnsafeRestartOverrides = -1 },
		func(s *recoveryMySQLState) { s.Purged = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:1-21" },
	} {
		s := valid
		mutate(&s)
		data, _ := json.Marshal(s)
		if _, err := parseRecoveryMySQLState(data); err == nil {
			t.Fatalf("unsafe evidence accepted: %+v", s)
		}
	}
}

func TestRecoveryWALScopeIsCryptographicallyBound(t *testing.T) {
	r := Request{Command: CommandRecoveryWAL, Engine: model.EnginePostgreSQL, ClusterID: model.NewResourceID(), OperationID: model.NewResourceID(), PlanDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute), RecoveryWAL: &model.RecoveryWALRequest{Fingerprint: "sha256:" + strings.Repeat("b", 64), Timeline: 1, Start: "0/100", End: "0/200"}}
	signature, err := SignRequest(r, "test-only-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*model.RecoveryWALRequest){func(w *model.RecoveryWALRequest) { w.Timeline = 2 }, func(w *model.RecoveryWALRequest) { w.Start = "0/101" }, func(w *model.RecoveryWALRequest) { w.End = "0/201" }, func(w *model.RecoveryWALRequest) { w.Fingerprint = "stale" }} {
		copy := *r.RecoveryWAL
		mutate(&copy)
		changed := r
		changed.RecoveryWAL = &copy
		actual, _ := SignRequest(changed, "test-only-secret")
		if actual == signature {
			t.Fatal("changed WAL scope retained signature")
		}
	}
}

func TestMySQLRecoveryEvidenceRequiresEveryField(t *testing.T) {
	valid := recoveryMySQLState{UUID: string(model.NewResourceID()), GTIDMode: "ON", Consistency: "ON", LogBin: 1, LogUpdates: 1, ReadOnly: 1, SuperReadOnly: 1}
	data, _ := json.Marshal(valid)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		for _, missing := range []bool{true, false} {
			if missing {
				delete(fields, key)
			} else {
				fields[key] = json.RawMessage("null")
			}
			data, _ := json.Marshal(fields)
			if _, err := parseRecoveryMySQLState(data); err == nil {
				t.Fatalf("accepted missing or null %s", key)
			}
			fields[key] = value
		}
	}
}
