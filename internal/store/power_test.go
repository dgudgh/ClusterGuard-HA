package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestPowerOperationsByClusterReturnsMostRecentFirst(t *testing.T) {
	repository := NewMemory()
	now := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.UTC)
	repository.now = func() time.Time { return now }
	cluster := newPowerCluster(t, repository, "power-history-order")

	older := createPowerOperation(t, repository, cluster.ResourceID)
	older, err := repository.TransitionPowerOperation(context.Background(), older.ResourceID,
		older.MetadataRevision, model.PowerFailed, "tester", "older failure", nil)
	if err != nil {
		t.Fatalf("fail older operation: %v", err)
	}

	now = now.Add(time.Hour)
	newer := createPowerOperation(t, repository, cluster.ResourceID)
	for _, target := range []model.PowerState{
		model.PowerPrechecking, model.PowerMaintenance, model.PowerShutdownPlanned,
		model.PowerShuttingDown, model.PowerPoweredOff,
	} {
		newer, err = repository.TransitionPowerOperation(context.Background(), newer.ResourceID,
			newer.MetadataRevision, target, "tester", string(target), nil)
		if err != nil {
			t.Fatalf("transition newer operation to %s: %v", target, err)
		}
	}

	for attempt := 0; attempt < 100; attempt++ {
		history := repository.PowerOperationsByCluster(cluster.ResourceID)
		if len(history) != 2 {
			t.Fatalf("history length=%d, want 2", len(history))
		}
		if history[0].ResourceID != newer.ResourceID || history[1].ResourceID != older.ResourceID {
			t.Fatalf("history is not newest first: %s then %s", history[0].ResourceID, history[1].ResourceID)
		}
	}
}

func newPowerCluster(t *testing.T, repository *Repository, displayName string) model.DatabaseCluster {
	t.Helper()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: displayName})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	return cluster
}

func createPowerOperation(t *testing.T, repository *Repository, clusterID model.ResourceID) model.PowerOperation {
	t.Helper()
	operation, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		ClusterID:     clusterID,
		OperationType: model.PowerService,
		Mode:          "lab-test",
		RequestedBy:   "tester",
		AutoRecovery:  true,
	})
	if err != nil {
		t.Fatalf("create power operation: %v", err)
	}
	return operation
}

func TestCreatePowerOperationPersistsInitialState(t *testing.T) {
	repository := NewMemory()
	cluster := newPowerCluster(t, repository, "power-create")

	operation := createPowerOperation(t, repository, cluster.ResourceID)
	if operation.State != model.PowerNormal {
		t.Fatalf("initial state=%s, want normal", operation.State)
	}
	if operation.Engine != model.EngineMySQL {
		t.Fatalf("engine=%s, want mysql", operation.Engine)
	}
	if operation.RequestedBy != "tester" {
		t.Fatalf("requested_by=%s, want tester", operation.RequestedBy)
	}

	stored, found := repository.PowerOperation(operation.ResourceID)
	if !found || stored.ResourceID != operation.ResourceID || stored.State != model.PowerNormal {
		t.Fatalf("power operation did not persist: %+v", stored)
	}
}

func TestCreatePowerOperationRejectsDuplicateActive(t *testing.T) {
	repository := NewMemory()
	cluster := newPowerCluster(t, repository, "power-dup")

	createPowerOperation(t, repository, cluster.ResourceID)
	_, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		ClusterID:     cluster.ResourceID,
		OperationType: model.PowerPowerOff,
		RequestedBy:   "tester",
	})
	if err == nil {
		t.Fatal("expected conflict for a second active power operation")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestCreatePowerOperationRejectsInvalidInput(t *testing.T) {
	repository := NewMemory()
	if _, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		OperationType: model.PowerService, RequestedBy: "tester",
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error for missing cluster, got %v", err)
	}

	cluster := newPowerCluster(t, repository, "power-invalid")
	if _, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		ClusterID:     cluster.ResourceID,
		OperationType: model.PowerOperationType("reboot"),
		RequestedBy:   "tester",
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error for bad operation type, got %v", err)
	}
	if _, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		ClusterID:     cluster.ResourceID,
		OperationType: model.PowerService,
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error for missing requester, got %v", err)
	}
}

func TestTransitionPowerOperationFullPath(t *testing.T) {
	repository := NewMemory()
	cluster := newPowerCluster(t, repository, "power-path")
	operation := createPowerOperation(t, repository, cluster.ResourceID)

	path := []struct {
		target model.PowerState
		note   string
	}{
		{model.PowerPrechecking, "precheck"},
		{model.PowerMaintenance, "maintenance"},
		{model.PowerShutdownPlanned, "plan"},
		{model.PowerShuttingDown, "execute"},
		{model.PowerPoweredOff, "power off"},
		{model.PowerBootDetected, "boot"},
		{model.PowerRecovering, "recover"},
		{model.PowerVerifying, "verify"},
		{model.PowerCompleted, "complete"},
	}
	current := operation
	for _, step := range path {
		next, err := repository.TransitionPowerOperation(context.Background(), current.ResourceID,
			current.MetadataRevision, step.target, "", step.note, nil)
		if err != nil {
			t.Fatalf("transition to %s: %v", step.target, err)
		}
		if next.State != step.target {
			t.Fatalf("state=%s, want %s", next.State, step.target)
		}
		current = next
	}
	if current.CompletedAt.IsZero() {
		t.Fatal("completed operation must record CompletedAt")
	}
}

func TestTransitionPowerOperationBlocksInvalidSkips(t *testing.T) {
	repository := NewMemory()
	cluster := newPowerCluster(t, repository, "power-skip")
	operation := createPowerOperation(t, repository, cluster.ResourceID)

	if _, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
		operation.MetadataRevision, model.PowerPoweredOff, "", "", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict for normal -> power_off, got %v", err)
	}
}

func TestTransitionPowerOperationTerminalIsImmutable(t *testing.T) {
	repository := NewMemory()
	cluster := newPowerCluster(t, repository, "power-terminal")
	operation := createPowerOperation(t, repository, cluster.ResourceID)

	operation, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
		operation.MetadataRevision, model.PowerFailed, "", "simulated failure", nil)
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if operation.State != model.PowerFailed {
		t.Fatalf("state=%s, want failed", operation.State)
	}
	// FAILED cannot transition anywhere (fail-closed).
	if _, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
		operation.MetadataRevision, model.PowerNormal, "", "", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict for failed -> normal, got %v", err)
	}
}

func TestTransitionPowerOperationRejectsStaleRevision(t *testing.T) {
	repository := NewMemory()
	cluster := newPowerCluster(t, repository, "power-stale")
	operation := createPowerOperation(t, repository, cluster.ResourceID)

	if _, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
		operation.MetadataRevision-1, model.PowerPrechecking, "", "", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict for stale revision, got %v", err)
	}
}

func TestTransitionPowerOperationAttachesSnapshot(t *testing.T) {
	repository := NewMemory()
	cluster := newPowerCluster(t, repository, "power-snapshot")
	operation := createPowerOperation(t, repository, cluster.ResourceID)

	// Walk the state machine up to shutdown_planned (where the snapshot is
	// captured) rather than skipping states.
	for _, target := range []model.PowerState{model.PowerPrechecking, model.PowerMaintenance} {
		next, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
			operation.MetadataRevision, target, "", "", nil)
		if err != nil {
			t.Fatalf("transition to %s: %v", target, err)
		}
		operation = next
	}

	instanceID := model.NewResourceID()
	snapshot := &model.PowerSnapshot{
		ClusterID:   cluster.ResourceID,
		ClusterName: cluster.DisplayName,
		Engine:      model.EngineMySQL,
		Primary:     model.PowerInstanceRef{InstanceID: instanceID, Hostname: "mysql-01", IPAddress: "192.0.2.10", Port: 3306},
		Replicas: []model.PowerInstanceRef{
			{InstanceID: model.NewResourceID(), Hostname: "mysql-02", IPAddress: "192.0.2.11", Port: 3306},
		},
	}
	operation, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
		operation.MetadataRevision, model.PowerShutdownPlanned, "", "snapshot captured", snapshot)
	if err != nil {
		t.Fatalf("attach snapshot: %v", err)
	}
	if operation.Snapshot == nil || operation.Snapshot.ClusterID != cluster.ResourceID {
		t.Fatal("snapshot was not attached")
	}
	if len(operation.Snapshot.Replicas) != 1 || operation.Snapshot.Replicas[0].Hostname != "mysql-02" {
		t.Fatalf("replicas not preserved: %+v", operation.Snapshot.Replicas)
	}
	// The cloned copy must not share backing slices with the caller.
	operation.Snapshot.Replicas[0].Hostname = "mutated"
	stored, _ := repository.PowerOperation(operation.ResourceID)
	if stored.Snapshot.Replicas[0].Hostname == "mutated" {
		t.Fatal("snapshot clone shares backing storage")
	}
}

func TestActivePowerOperationAndTerminalAllowsNew(t *testing.T) {
	repository := NewMemory()
	cluster := newPowerCluster(t, repository, "power-active")

	operation := createPowerOperation(t, repository, cluster.ResourceID)
	active, found := repository.ActivePowerOperation(context.Background(), cluster.ResourceID)
	if !found || active.ResourceID != operation.ResourceID {
		t.Fatalf("expected active power operation, got %+v", active)
	}

	// Walk the state machine to completion; a new one may then start.
	for _, target := range []model.PowerState{model.PowerPrechecking, model.PowerMaintenance,
		model.PowerShutdownPlanned, model.PowerShuttingDown, model.PowerPoweredOff,
		model.PowerBootDetected, model.PowerRecovering, model.PowerVerifying, model.PowerCompleted} {
		next, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
			operation.MetadataRevision, target, "", "", nil)
		if err != nil {
			t.Fatalf("transition to %s: %v", target, err)
		}
		operation = next
	}
	if _, found := repository.ActivePowerOperation(context.Background(), cluster.ResourceID); found {
		t.Fatal("terminal operation must not be active")
	}
	if _, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		ClusterID: cluster.ResourceID, OperationType: model.PowerService, RequestedBy: "tester",
	}); err != nil {
		t.Fatalf("new operation after completion: %v", err)
	}
}

func TestApplyAndReleasePowerProtections(t *testing.T) {
	repository := NewMemory()
	cluster := newPowerCluster(t, repository, "power-protect")

	// Add two instances so maintenance covers them all.
	instanceA := model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": "power-a-uuid"},
		DisplayName:    "mysql-01", Hostname: "mysql-01", IPAddress: "192.0.2.10", Port: 3306}
	instanceB := model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": "power-b-uuid"},
		DisplayName:    "mysql-02", Hostname: "mysql-02", IPAddress: "192.0.2.11", Port: 3306}
	if _, err := repository.ReconcileInstance(instanceA); err != nil {
		t.Fatalf("create instance A: %v", err)
	}
	if _, err := repository.ReconcileInstance(instanceB); err != nil {
		t.Fatalf("create instance B: %v", err)
	}

	if err := repository.ApplyPowerProtections(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("apply protections: %v", err)
	}
	frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || !frozen {
		t.Fatalf("recovery freeze not applied: frozen=%t err=%v", frozen, err)
	}
	instances := repository.Instances(cluster.ResourceID)
	if len(instances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(instances))
	}
	for _, instance := range instances {
		if !instance.Maintenance {
			t.Fatalf("instance %s not in maintenance", instance.ResourceID)
		}
	}
	// Double-apply must be idempotent.
	if err := repository.ApplyPowerProtections(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("apply protections twice: %v", err)
	}

	if err := repository.ReleasePowerProtections(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("release protections: %v", err)
	}
	frozen, err = repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || frozen {
		t.Fatalf("recovery freeze not released: frozen=%t err=%v", frozen, err)
	}
	for _, instance := range repository.Instances(cluster.ResourceID) {
		if instance.Maintenance {
			t.Fatalf("instance %s still in maintenance", instance.ResourceID)
		}
	}
}

func TestIsClusterPowerProtected(t *testing.T) {
	repository := NewMemory()
	cluster := newPowerCluster(t, repository, "power-protected")
	operation := createPowerOperation(t, repository, cluster.ResourceID)

	if protected, _ := repository.IsClusterPowerProtected(cluster.ResourceID); protected {
		t.Fatal("normal state must not report protection")
	}
	// Walk up to shutting_down (where the shutdown actually starts) without
	// skipping states.
	for _, target := range []model.PowerState{model.PowerPrechecking, model.PowerMaintenance,
		model.PowerShutdownPlanned, model.PowerShuttingDown} {
		next, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
			operation.MetadataRevision, target, "", "", nil)
		if err != nil {
			t.Fatalf("transition to %s: %v", target, err)
		}
		operation = next
	}
	protected, state := repository.IsClusterPowerProtected(cluster.ResourceID)
	if !protected || state != model.PowerShuttingDown {
		t.Fatalf("protected=%t state=%s, want true/shutting_down", protected, state)
	}
	// FAILED is terminal for state transitions, but it remains power-protected:
	// fail-closed shutdown protections are released only by a verified complete.
	off, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
		operation.MetadataRevision, model.PowerPoweredOff, "", "", nil)
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if _, err := repository.TransitionPowerOperation(context.Background(), off.ResourceID,
		off.MetadataRevision, model.PowerFailed, "", "", nil); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if protected, state := repository.IsClusterPowerProtected(cluster.ResourceID); !protected || state != model.PowerFailed {
		t.Fatalf("failed lifecycle protection=%t state=%s, want true/failed", protected, state)
	}

	// A later successful lifecycle supersedes historical failures; otherwise a
	// single old incident would make the cluster appear protected forever.
	successful := createPowerOperation(t, repository, cluster.ResourceID)
	for _, target := range []model.PowerState{model.PowerPrechecking, model.PowerMaintenance,
		model.PowerShutdownPlanned, model.PowerShuttingDown, model.PowerPoweredOff,
		model.PowerBootDetected, model.PowerRecovering, model.PowerVerifying, model.PowerCompleted} {
		next, transitionErr := repository.TransitionPowerOperation(context.Background(), successful.ResourceID,
			successful.MetadataRevision, target, "", "", nil)
		if transitionErr != nil {
			t.Fatalf("successful transition to %s: %v", target, transitionErr)
		}
		successful = next
	}
	if protected, state := repository.IsClusterPowerProtected(cluster.ResourceID); protected || state != model.PowerNormal {
		t.Fatalf("latest completed lifecycle protection=%t state=%s, want false/normal", protected, state)
	}
}
