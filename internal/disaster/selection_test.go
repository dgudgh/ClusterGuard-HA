package disaster

import (
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func selectionFixture(engine model.Engine) ([]model.DatabaseInstance, []model.RecoveryEvidence) {
	members := []model.DatabaseInstance{}
	evidence := []model.RecoveryEvidence{}
	for i := 0; i < 3; i++ {
		id, native := model.NewResourceID(), string(model.NewResourceID())
		identity := model.EngineIdentity{"resource_id": native, "server_uuid": native, "system_identifier": "7654321"}
		members = append(members, model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: id}, Engine: engine, EngineIdentity: identity})
		e := model.RecoveryEvidence{InstanceID: id, NativeID: native, Engine: engine, ObservedAt: time.Now(), Fenced: true, Complete: true, SystemIdentifier: "7654321", Timeline: 1, Position: "0/400", Checkpoint: "0/100"}
		e.Fingerprint = EvidenceFingerprint(e)
		evidence = append(evidence, e)
	}
	return members, evidence
}

func fingerprintAll(evidence []model.RecoveryEvidence) {
	for i := range evidence {
		evidence[i].Fingerprint = EvidenceFingerprint(evidence[i])
	}
}

func TestMySQLSelectionUsesSetInclusionNotCount(t *testing.T) {
	m, e := selectionFixture(model.EngineMySQL)
	u := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	v := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	e[0].GTIDExecuted = u + ":1-100"
	e[1].GTIDExecuted = u + ":1-101"
	e[2].GTIDExecuted = u + ":1-90"
	fingerprintAll(e)
	if id, err := Select(m, e, nil); err != nil || id != e[1].InstanceID {
		t.Fatalf("selection: %s %v", id, err)
	}
	e[2].GTIDExecuted = u + ":1-90," + v + ":1"
	fingerprintAll(e)
	if _, err := Select(m, e, nil); err == nil {
		t.Fatal("independent committed GTID was discarded")
	}
	e[1].GTIDExecuted += "," + v + ":1"
	e[1].GTIDPurged = u + ":1-70"
	fingerprintAll(e)
	if id, err := Select(m, e, nil); err != nil || id != e[1].InstanceID {
		t.Fatalf("superset with purged history must allow rebuild: %s %v", id, err)
	}
}

func TestRecoveryRejectsIncompleteAndDuplicateEvidence(t *testing.T) {
	for _, mutate := range []func([]model.RecoveryEvidence){
		func(e []model.RecoveryEvidence) { e[0].Complete = false },
		func(e []model.RecoveryEvidence) { e[0].Fenced = false },
		func(e []model.RecoveryEvidence) { e[0].PreparedTransactions = true },
		func(e []model.RecoveryEvidence) { e[0].InstanceID = e[1].InstanceID },
		func(e []model.RecoveryEvidence) { e[0].NativeID = e[1].NativeID },
	} {
		m, e := selectionFixture(model.EngineMySQL)
		mutate(e)
		fingerprintAll(e)
		if _, err := Select(m, e, nil); err == nil {
			t.Fatal("unsafe evidence accepted")
		}
	}
}

func TestPostgreSQLSelectionRequiresActualWALProof(t *testing.T) {
	m, e := selectionFixture(model.EnginePostgreSQL)
	e[1].Position = "0/500"
	fingerprintAll(e)
	if _, err := Select(m, e, nil); err == nil {
		t.Fatal("selected by maximum LSN without shared WAL comparison")
	}
	proofs := []model.RecoveryProof{}
	for _, other := range []int{0, 2} {
		proofs = append(proofs, model.RecoveryProof{CandidateID: e[1].InstanceID, OtherID: e[other].InstanceID, CandidateFingerprint: e[1].Fingerprint, OtherFingerprint: e[other].Fingerprint, CommonWALVerified: true})
	}
	if id, err := Select(m, e, proofs); err != nil || id != e[1].InstanceID {
		t.Fatalf("proven source: %s %v", id, err)
	}
	proofs[0].CandidateFingerprint = "stale"
	if _, err := Select(m, e, proofs); err == nil {
		t.Fatal("stale WAL proof accepted")
	}
}

func TestPostgreSQLBranchesRequireNoDiscardedCommit(t *testing.T) {
	_, e := selectionFixture(model.EnginePostgreSQL)
	e[0].Timeline = 2
	e[0].History = []model.RecoveryTimeline{{Timeline: 1, SwitchLSN: "0/200"}}
	e[1].Timeline = 3
	e[1].History = []model.RecoveryTimeline{{Timeline: 1, SwitchLSN: "0/200"}}
	fingerprintAll(e)
	p := model.RecoveryProof{CandidateID: e[1].InstanceID, OtherID: e[0].InstanceID, CandidateFingerprint: e[1].Fingerprint, OtherFingerprint: e[0].Fingerprint, CommonWALVerified: true}
	if ok, err := PostgreSQLContains(e[1], e[0], []model.RecoveryProof{p}); err != nil || ok {
		t.Fatalf("unscanned branch accepted: %v %v", ok, err)
	}
	p.DiscardedWALVerified = true
	p.DiscardedTransactions = true
	if ok, err := PostgreSQLContains(e[1], e[0], []model.RecoveryProof{p}); err != nil || ok {
		t.Fatalf("discarded committed branch: %v %v", ok, err)
	}
	p.DiscardedTransactions = false
	if ok, err := PostgreSQLContains(e[1], e[0], []model.RecoveryProof{p}); err != nil || !ok {
		t.Fatalf("empty branch blocked: %v %v", ok, err)
	}
}

func TestLSNAndHistoryRejectMalformedInput(t *testing.T) {
	for _, value := range []string{"", "-1/0", "0", "100000000/1", "0/100000000", "0/g"} {
		if _, err := LSN(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	_, e := selectionFixture(model.EnginePostgreSQL)
	e[0].Timeline = 3
	e[0].History = []model.RecoveryTimeline{{Timeline: 2, SwitchLSN: "0/100"}}
	if _, _, err := CommonTimeline(e[0], e[1]); err == nil {
		t.Fatal("missing ancestor accepted")
	}
}
