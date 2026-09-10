package store

import (
	"context"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestRecoveryAuthorizationRequiresSelectedInventoryAndLiveLease(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		t.Run(string(engine), func(t *testing.T) {
			r, task := disasterFixture(t, engine)
			if permit, _, ok := r.RecoveryAuthorization(task.ClusterID, task.Members[0].ResourceID, r.now()); ok && (engine!=model.EngineMySQL || permit.Stage!=model.RecoveryFencing || permit.PrimaryID!="") {
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
