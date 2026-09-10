package store

import (
	"context"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

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
