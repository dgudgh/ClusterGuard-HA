package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/disaster"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

func disasterFixture(t *testing.T, engine model.Engine) (*Repository, model.RecoveryTask) {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "recovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	c, err := r.UpsertCluster(model.DatabaseCluster{Engine: engine, DisplayName: "disaster-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		native := string(model.NewResourceID())
		identity := model.EngineIdentity{"server_uuid": native}
		if engine == model.EnginePostgreSQL {
			identity = model.EngineIdentity{"resource_id": native, "system_identifier": "7654321"}
		}
		_, err := r.ReconcileInstance(model.DatabaseInstance{ClusterID: c.ResourceID, Engine: engine, EngineIdentity: identity, Hostname: fmt.Sprintf("member-%d", i), IPAddress: fmt.Sprintf("192.0.2.%d", i+1), Port: 5432, Role: model.RoleUnknown, Health: model.Health{State: model.HealthUnhealthy}})
		if err != nil {
			t.Fatal(err)
		}
	}
	task, err := r.PlanRecovery(context.Background(), c.ResourceID, "operator")
	if err != nil {
		t.Fatal(err)
	}
	lease := model.NewResourceID()
	err = r.PutCoordinationOperationLock(coordination.OperationLockRecord{ResourceID: lease, ClusterID: c.ResourceID, OperationID: task.ResourceID, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	task, err = r.BeginRecovery(context.Background(), task.ResourceID, task.MetadataRevision, lease)
	if err != nil {
		t.Fatal(err)
	}
	return r, task
}

func advanceDisasterFixture(t *testing.T, r *Repository, task model.RecoveryTask) model.RecoveryTask {
	t.Helper()
	for _, stage := range []model.RecoveryStage{model.RecoveryInspecting, model.RecoverySelecting, model.RecoveryStarting, model.RecoveryRebuilding, model.RecoveryVerifying} {
		if stage == model.RecoveryStarting {
			for i, m := range task.Members {
				native := m.EngineIdentity["server_uuid"]
				if task.Engine == model.EnginePostgreSQL {
					native = m.EngineIdentity["resource_id"]
				}
				e := model.RecoveryEvidence{InstanceID: m.ResourceID, NativeID: native, Engine: task.Engine, ObservedAt: r.now(), Fenced: true, Complete: true, SystemIdentifier: "7654321", Timeline: 1, Position: "0/500", Checkpoint: "0/100"}
				if i == 0 {
					e.GTIDExecuted = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:1-20"
				} else {
					e.GTIDExecuted = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:1-10"
					e.Position = "0/400"
				}
				e.Fingerprint = disaster.EvidenceFingerprint(e)
				task.Evidence = append(task.Evidence, e)
			}
			task.PrimaryID = task.Members[0].ResourceID
			for i := 1; i < 3; i++ {
				task.Proofs = append(task.Proofs, model.RecoveryProof{CandidateID: task.PrimaryID, OtherID: task.Members[i].ResourceID, CandidateFingerprint: task.Evidence[0].Fingerprint, OtherFingerprint: task.Evidence[i].Fingerprint, CommonWALVerified: true})
			}
		}
		var err error
		task, err = r.AdvanceRecovery(context.Background(), task, model.RecoveryEvent{Stage: stage, Message: string(stage)})
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	return task
}

func disasterTopology(r *Repository, task model.RecoveryTask) model.TopologySnapshot {
	zero := int64(0)
	topology := model.TopologySnapshot{ClusterID: task.ClusterID, ObservedAt: r.now(), Health: model.Health{State: model.HealthHealthy, ObservedAt: r.now()}}
	for i, m := range task.Members {
		m.Health = model.Health{State: model.HealthHealthy, ObservedAt: r.now()}
		if i == 0 {
			m.Role = model.RolePrimary
		} else {
			m.Role = model.RoleReplica
			if task.Engine == model.EnginePostgreSQL {
				m.Role = model.RoleStandby
			}
			m.Replication = model.ReplicationStatus{SourceIdentity: task.Members[0].EngineIdentity.Clone(), IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &zero}
			topology.Links = append(topology.Links, model.ReplicationLink{ClusterID: task.ClusterID, SourceInstanceID: task.PrimaryID, TargetInstanceID: m.ResourceID, Healthy: true, LagSeconds: &zero})
		}
		topology.Instances = append(topology.Instances, m)
		topology.Probes = append(topology.Probes, model.ProbeStatus{InstanceID: m.ResourceID, Outcome: model.ProbeOutcomeReachable, DiscoveryObservedAt: r.now(), Health: m.Health})
	}
	return topology
}

func TestDisasterCommitBothEnginesPersistsAndKeepsFreeze(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		t.Run(string(engine), func(t *testing.T) {
			r, task := disasterFixture(t, engine)
			if err := r.SetRecoveryFreeze(context.Background(), task.ClusterID, false); err == nil {
				t.Fatal("ordinary unfreeze bypassed recovery commit")
			}
			if err := r.SetMaintenance(context.Background(), task.ClusterID, task.Members[0].ResourceID, false); err == nil {
				t.Fatal("ordinary maintenance clear bypassed recovery")
			}
			task = advanceDisasterFixture(t, r, task)
			topology := disasterTopology(r, task)
			bad := topology
			bad.Links = nil
			if _, err := r.CommitRecovery(context.Background(), task, bad); err == nil {
				t.Fatal("links=[] was accepted")
			}
			bad = topology
			bad.Probes = nil
			if _, err := r.CommitRecovery(context.Background(), task, bad); err == nil {
				t.Fatal("healthy labels without current probes were accepted")
			}
			committed, err := r.CommitRecovery(context.Background(), task, topology)
			if err != nil {
				t.Fatal(err)
			}
			cluster, _ := r.Cluster(task.ClusterID)
			if !cluster.RecoveryFreeze || cluster.Recovery.CurrentPrimaryID != task.PrimaryID || cluster.Recovery.IncidentActive || !cluster.Recovery.IncidentRecovered {
				t.Fatalf("commit state: %+v", cluster)
			}
			for _, m := range r.Instances(task.ClusterID) {
				if !m.Maintenance || m.DesiredRole != m.Role {
					t.Fatalf("member projection not committed: %+v", m)
				}
			}
			reopened, err := Open(r.path)
			if err != nil {
				t.Fatal(err)
			}
			stored, found := reopened.RecoveryTask(task.ResourceID)
			if !found || stored.Stage != model.RecoveryCommitted || len(stored.Evidence) != 3 {
				t.Fatalf("recovery task not durable: %+v", stored)
			}
			stored.Evidence[0].NativeID = "changed"
			original, _ := reopened.RecoveryTask(task.ResourceID)
			if original.Evidence[0].NativeID == "changed" {
				t.Fatal("mutable recovery state escaped")
			}
			finished, err := r.CompleteRecovery(context.Background(), committed)
			if err != nil {
				t.Fatal(err)
			}
			cluster, _ = r.Cluster(task.ClusterID)
			if finished.Stage != model.RecoverySucceeded || cluster.RecoveryFreeze || cluster.Recovery.RecoveredAt.IsZero() {
				t.Fatalf("completion state: %+v", cluster)
			}
		})
	}
}

func TestRecoveryFreezeRejectsStaleWriterGrant(t *testing.T) {
	r, task := disasterFixture(t, model.EngineMySQL)
	ep := model.NewResourceID()
	now := r.now()
	grant := coordination.LeaseRecord{Lease: endpoint.Lease{ResourceID: model.NewResourceID(), ClusterID: task.ClusterID, HAEndpointID: ep, OperationID: ep, OwnerID: task.Members[0].ResourceID, Active: true, ExpiresAt: now.Add(time.Minute)}, CreatedAt: now, UpdatedAt: now}
	if err := r.PutCoordinationLease(grant); err == nil {
		t.Fatal("writer grant bypassed freeze")
	}
	if err := r.ReplaceCoordinationLeases([]coordination.LeaseRecord{grant}); err == nil {
		t.Fatal("stale keeper batch restored a writer grant")
	}
	if len(r.CoordinationLeases()) != 0 {
		t.Fatal("rejected grant was persisted")
	}
}

func TestRecoveryCompletionRejectsNewFailureAfterCommit(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		t.Run(string(engine), func(t *testing.T) {
			r, task := disasterFixture(t, engine)
			task = advanceDisasterFixture(t, r, task)
			committed, err := r.CommitRecovery(context.Background(), task, disasterTopology(r, task))
			if err != nil {
				t.Fatal(err)
			}
			r.mu.Lock()
			topology := r.snapshot.TopologySnapshots[task.ClusterID]
			topology.Health.State = model.HealthUnhealthy
			r.snapshot.TopologySnapshots[task.ClusterID] = topology
			r.mu.Unlock()
			if _, err := r.CompleteRecovery(context.Background(), committed); err == nil || !strings.Contains(err.Error(), "state=unhealthy") {
				t.Fatalf("new topology failure was ignored or unexplained: %v", err)
			}
			cluster, _ := r.Cluster(task.ClusterID)
			if !cluster.RecoveryFreeze {
				t.Fatal("new failure released freeze")
			}
		})
	}
}

func TestRecoveryTopologyRejectionExplainsEvidenceGap(t *testing.T) {
	r, task := disasterFixture(t, model.EngineMySQL)
	task = advanceDisasterFixture(t, r, task)
	for _, test := range []struct {
		name   string
		change func(*model.TopologySnapshot)
		want   string
	}{
		{"cluster", func(s *model.TopologySnapshot) { s.ClusterID = model.NewResourceID() }, "cluster does not match"},
		{"links", func(s *model.TopologySnapshot) { s.Links = nil }, "members=3/3 links=0/2"},
		{"health", func(s *model.TopologySnapshot) { s.Health.State = model.HealthDegraded }, "state=degraded"},
		{"old", func(s *model.TopologySnapshot) { s.ObservedAt = r.now().Add(-time.Minute) }, "not fresh: observed="},
		{"future", func(s *model.TopologySnapshot) { s.ObservedAt = r.now().Add(time.Second) }, "not fresh: observed="},
	} {
		t.Run(test.name, func(t *testing.T) {
			topology := disasterTopology(r, task)
			test.change(&topology)
			if err := verifyRecoveryTopology(task, topology, r.now()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("evidence gap not explained: %v", err)
			}
		})
	}
}

func TestDisasterCommitFailureKeepsEveryProtection(t *testing.T) {
	r, task := disasterFixture(t, model.EnginePostgreSQL)
	task = advanceDisasterFixture(t, r, task)
	r.syncFile = func(*os.File) error { return errors.New("injected disk failure") }
	if _, err := r.CommitRecovery(context.Background(), task, disasterTopology(r, task)); err == nil {
		t.Fatal("commit unexpectedly succeeded")
	}
	cluster, _ := r.Cluster(task.ClusterID)
	stored, _ := r.RecoveryTask(task.ResourceID)
	if !cluster.RecoveryFreeze || cluster.Recovery.IncidentRecovered || stored.Stage != model.RecoveryVerifying {
		t.Fatalf("failed commit released state: %+v %+v", cluster, stored)
	}
	for _, m := range r.Instances(task.ClusterID) {
		if !m.Maintenance {
			t.Fatal("failed commit released a member")
		}
	}
}

func TestDisasterRejectsExpiredExecutorAndRedactsEvents(t *testing.T) {
	r, task := disasterFixture(t, model.EngineMySQL)
	updated, err := r.AdvanceRecovery(context.Background(), task, model.RecoveryEvent{Stage: model.RecoveryFencing, Message: "password=do-not-expose"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(updated.Message, "do-not-expose") {
		t.Fatal("secret stored in recovery event")
	}
	if _, err := r.AdvanceRecovery(context.Background(), task, model.RecoveryEvent{Stage: model.RecoveryInspecting}); err == nil {
		t.Fatal("stale executor revision accepted")
	}
	now := r.now().Add(2 * time.Hour)
	r.now = func() time.Time { return now }
	if _, err := r.AdvanceRecovery(context.Background(), updated, model.RecoveryEvent{Stage: model.RecoveryInspecting}); err == nil {
		t.Fatal("expired executor accepted")
	}
}

func TestRecoveryAbandonedExecutorRemainsFenced(t *testing.T) {
	r, task := disasterFixture(t, model.EnginePostgreSQL)
	now := r.now()
	if err := r.BlockAbandonedRecoveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, _ := r.RecoveryTask(task.ResourceID)
	if stored.Stage != model.RecoveryFencing {
		t.Fatal("live executor was blocked")
	}
	r.now = func() time.Time { return now.Add(time.Hour + 14*time.Second) }
	if err := r.BlockAbandonedRecoveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, _ = r.RecoveryTask(task.ResourceID)
	if stored.Stage != model.RecoveryFencing {
		t.Fatal("authorization drain interval was skipped")
	}
	r.now = func() time.Time { return now.Add(time.Hour + 16*time.Second) }
	if err := r.BlockAbandonedRecoveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, _ = r.RecoveryTask(task.ResourceID)
	cluster, _ := r.Cluster(task.ClusterID)
	if stored.Stage != model.RecoveryBlocked || !cluster.RecoveryFreeze || !cluster.Recovery.IncidentActive {
		t.Fatal("expired task did not retain recovery protection")
	}
	if _, _, ok := r.RecoveryAuthorization(task.ClusterID, task.Members[0].ResourceID, r.now()); ok {
		t.Fatal("abandoned task issued recovery authorization")
	}
	for _, member := range r.Instances(task.ClusterID) {
		if !member.Maintenance {
			t.Fatal("abandoned recovery cleared maintenance")
		}
	}
}

func TestRecoveryAuthorizationRequiresSelectedInventoryAndLiveLease(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		t.Run(string(engine), func(t *testing.T) {
			r, task := disasterFixture(t, engine)
			if permit, _, ok := r.RecoveryAuthorization(task.ClusterID, task.Members[0].ResourceID, r.now()); ok && (engine != model.EngineMySQL || permit.Stage != model.RecoveryFencing || permit.PrimaryID != "") {
				t.Fatal("pre-selection writer authorization")
			}
			task = advanceDisasterFixture(t, r, task)
			permit, expiry, ok := r.RecoveryAuthorization(task.ClusterID, task.PrimaryID, r.now())
			if !ok || permit.ResourceID != task.ResourceID || permit.LeaseID != task.LeaseID || !expiry.After(r.now()) {
				t.Fatal("selected recovery primary not authorized")
			}
			if _, _, ok = r.RecoveryAuthorization(task.ClusterID, task.Members[1].ResourceID, r.now()); ok {
				t.Fatal("replica received a primary permit")
			}
			if _, _, ok = r.RecoveryAuthorization(task.ClusterID, task.PrimaryID, expiry); ok {
				t.Fatal("expired operation lease authorized a primary")
			}
			r.mu.Lock()
			r.snapshot.InventoryGenerations[task.ClusterID]++
			r.mu.Unlock()
			if _, _, ok = r.RecoveryAuthorization(task.ClusterID, task.PrimaryID, r.now()); ok {
				t.Fatal("changed inventory still authorized")
			}
			r.mu.Lock()
			r.snapshot.InventoryGenerations[task.ClusterID]--
			r.mu.Unlock()
			if _, err := r.AdvanceRecovery(context.Background(), task, model.RecoveryEvent{Stage: model.RecoveryBlocked, Message: "blocked fixture"}); err != nil {
				t.Fatal(err)
			}
			if _, _, ok = r.RecoveryAuthorization(task.ClusterID, task.PrimaryID, r.now().Add(time.Second)); ok {
				t.Fatal("blocked task still authorized")
			}
		})
	}
}
