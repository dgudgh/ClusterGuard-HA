package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/pkg/model"
)

func unstartedRecoveryFixture(t *testing.T, engine model.Engine) (*Repository, model.RecoveryTask) {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "recovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.UpsertCluster(model.DatabaseCluster{Engine: engine, DisplayName: "binlog-gap-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		native := string(model.NewResourceID())
		identity := model.EngineIdentity{"server_uuid": native}
		if engine == model.EnginePostgreSQL {
			identity = model.EngineIdentity{"resource_id": native, "system_identifier": "7654321"}
		}
		if _, err = r.ReconcileInstance(model.DatabaseInstance{ClusterID: c.ResourceID, Engine: engine, EngineIdentity: identity, Hostname: fmt.Sprintf("member-%d", i), IPAddress: fmt.Sprintf("192.0.2.%d", i+1), Port: 3306, Role: model.RoleUnknown}); err != nil {
			t.Fatal(err)
		}
	}
	task, err := r.PlanRecovery(context.Background(), c.ResourceID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return r, task
}

func TestRecoveryReplanReplacesOnlyUnstartedStaleInventory(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		for _, blocked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/blocked=%t", engine, blocked), func(t *testing.T) {
				ctx := context.Background()
				r, old := unstartedRecoveryFixture(t, engine)
				if blocked {
					if err := r.RecordRecoveryStartFailure(ctx, old.ResourceID, old.MetadataRevision, "initial preflight rejected"); err != nil {
						t.Fatal(err)
					}
					old, _ = r.RecoveryTask(old.ResourceID)
				}
				// The live regression had identical member identities but a later inventory generation.
				r.snapshot.InventoryGenerations[old.ClusterID]++
				if err := r.ValidateRecoveryPlan(ctx, old); !errors.Is(err, ErrConflict) {
					t.Fatalf("stale preflight: %v", err)
				}
				fresh, err := r.PlanRecovery(ctx, old.ClusterID, "admin")
				if err != nil {
					t.Fatal(err)
				}
				if fresh.ResourceID == old.ResourceID || fresh.InventoryGeneration != old.InventoryGeneration+1 || fresh.PlanDigest == old.PlanDigest || fresh.Stage != model.RecoveryPlanned {
					t.Fatalf("stale plan reused: %+v", fresh)
				}
				retained, _ := r.RecoveryTask(old.ResourceID)
				if retained.Stage != model.RecoverySuperseded || retained.PlanDigest != old.PlanDigest || !reflect.DeepEqual(retained.Members, old.Members) || len(retained.Events) != len(old.Events)+1 {
					t.Fatalf("old evidence lost: %+v", retained)
				}
				for i := range old.Events {
					if retained.Events[i] != old.Events[i] {
						t.Fatal("old event overwritten")
					}
				}
				if err = r.ValidateRecoveryPlan(ctx, retained); !errors.Is(err, ErrConflict) {
					t.Fatalf("retired plan passed preflight: %v", err)
				}
				if _, err = r.BeginRecovery(ctx, old.ResourceID, retained.MetadataRevision, model.NewResourceID()); !errors.Is(err, ErrConflict) {
					t.Fatalf("old confirmation accepted: %v", err)
				}
				reopened, err := Open(r.path)
				if err != nil {
					t.Fatal(err)
				}
				same, err := reopened.PlanRecovery(ctx, old.ClusterID, "admin")
				if err != nil || same.ResourceID != fresh.ResourceID {
					t.Fatalf("reopen reused historical plan: %+v %v", same, err)
				}
				if err = r.ValidateRecoveryPlan(ctx, fresh); err != nil {
					t.Fatal(err)
				}
				if _, err = r.BeginRecovery(ctx, fresh.ResourceID, fresh.MetadataRevision, model.NewResourceID()); !errors.Is(err, ErrConflict) {
					t.Fatal("new plan bypassed lease")
				}
				lease := model.NewResourceID()
				now := time.Now().UTC()
				if err = r.PutCoordinationOperationLock(coordination.OperationLockRecord{ResourceID: lease, ClusterID: fresh.ClusterID, OperationID: fresh.ResourceID, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
					t.Fatal(err)
				}
				begun, err := r.BeginRecovery(ctx, fresh.ResourceID, fresh.MetadataRevision, lease)
				if err != nil || begun.Stage != model.RecoveryFencing {
					t.Fatalf("fresh plan cannot begin: %+v %v", begun, err)
				}
			})
		}
	}
}

func TestRecoveryReplanPreservesStartedProtectedOrLeasedPlans(t *testing.T) {
	for _, guard := range []string{"started", "protected", "active lease", "evidence", "primary", "committed"} {
		t.Run(guard, func(t *testing.T) {
			r, old := unstartedRecoveryFixture(t, model.EngineMySQL)
			now := time.Now().UTC()
			switch guard {
			case "started":
				old.AttemptStartedAt = now
			case "protected":
				c := r.snapshot.Clusters[old.ClusterID]
				c.RecoveryFreeze = true
				c.Recovery = &model.RecoveryState{TaskID: old.ResourceID, LastRecoveryStatus: "blocked"}
				r.snapshot.Clusters[old.ClusterID] = c
			case "active lease":
				lease := model.NewResourceID()
				if err := r.PutCoordinationOperationLock(coordination.OperationLockRecord{ResourceID: lease, ClusterID: old.ClusterID, OperationID: old.ResourceID, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
					t.Fatal(err)
				}
			case "evidence":
				old.Evidence = []model.RecoveryEvidence{{InstanceID: old.Members[0].ResourceID}}
			case "primary":
				old.PrimaryID = old.Members[0].ResourceID
			case "committed":
				old.CommittedAt = now
			}
			r.snapshot.RecoveryTasks[old.ResourceID] = old
			r.snapshot.InventoryGenerations[old.ClusterID]++
			planned, err := r.PlanRecovery(context.Background(), old.ClusterID, "admin")
			if err != nil || planned.ResourceID != old.ResourceID || planned.Stage != old.Stage || planned.PlanDigest != old.PlanDigest {
				t.Fatalf("guarded task replaced: %+v %v", planned, err)
			}
			if err = r.ValidateRecoveryPlan(context.Background(), planned); !errors.Is(err, ErrConflict) {
				t.Fatalf("guarded inventory mismatch passed: %v", err)
			}
			if len(r.RecoveryTasks(old.ClusterID)) != 1 {
				t.Fatal("new task bypassed existing protection")
			}
		})
	}
}

func TestRecoveryReplanReusesCurrentPlanWithoutChangingConfirmation(t *testing.T) {
	r, task := unstartedRecoveryFixture(t, model.EngineMySQL)
	fresh, err := r.PlanRecovery(context.Background(), task.ClusterID, "admin")
	if err != nil || !reflect.DeepEqual(fresh, task) {
		t.Fatalf("current plan changed: %+v %v", fresh, err)
	}
}
