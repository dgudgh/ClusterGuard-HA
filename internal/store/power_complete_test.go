package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestPowerRecoveryCompletionIsAtomic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "disk-failure"}[fail], func(t *testing.T) {
			r, err := Open(filepath.Join(t.TempDir(), "metadata.json"))
			if err != nil {
				t.Fatal(err)
			}
			cluster := newPowerCluster(t, r, "atomic-power")
			op := createPowerOperation(t, r, cluster.ResourceID)
			for _, stage := range []model.PowerState{model.PowerPrechecking, model.PowerMaintenance, model.PowerShutdownPlanned, model.PowerShuttingDown, model.PowerPoweredOff, model.PowerBootDetected, model.PowerRecovering, model.PowerVerifying} {
				op, err = r.TransitionPowerOperation(context.Background(), op.ResourceID, op.MetadataRevision, stage, "operator", "", nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := r.ApplyPowerProtections(context.Background(), cluster.ResourceID); err != nil {
				t.Fatal(err)
			}
			observed := time.Now().UTC()
			r.mu.Lock()
			r.snapshot.TopologySnapshots[cluster.ResourceID] = model.TopologySnapshot{ClusterID: cluster.ResourceID, ObservedAt: observed}
			r.mu.Unlock()
			if _, err := r.CompletePowerRecovery(context.Background(), op.ResourceID, op.MetadataRevision, observed.Add(-time.Second)); err == nil {
				t.Fatal("stale observation accepted")
			}
			if fail {
				r.syncFile = func(*os.File) error { return errors.New("injected disk failure") }
			}
			_, err = r.CompletePowerRecovery(context.Background(), op.ResourceID, op.MetadataRevision, observed)
			stored, _ := r.PowerOperation(op.ResourceID)
			current, _ := r.Cluster(cluster.ResourceID)
			if fail {
				if err == nil || stored.State != model.PowerVerifying || !current.RecoveryFreeze {
					t.Fatalf("failed commit changed protection: %+v %+v %v", stored, current, err)
				}
			} else if err != nil || stored.State != model.PowerCompleted || current.RecoveryFreeze {
				t.Fatalf("atomic completion failed: %+v %+v %v", stored, current, err)
			}
		})
	}
}
