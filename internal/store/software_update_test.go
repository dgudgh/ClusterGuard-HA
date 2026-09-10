package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSoftwareUpdateGatePersistsAndTransfersWithoutUnfencing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcquireSoftwareUpdateGate("patch-1", "execution-1"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.SoftwareUpdateMaintenanceActive() {
		t.Fatal("restart lost the maintenance gate")
	}
	if _, err := reopened.ClaimSoftwareUpdateGate("patch-2", "execution-2", "patch-1", "wrong-owner"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale takeover accepted: %v", err)
	}
	if _, err := reopened.ClaimSoftwareUpdateGate("patch-2", "execution-2", "patch-1", "execution-1"); err != nil {
		t.Fatal(err)
	}
	if !reopened.SoftwareUpdateMaintenanceActive() {
		t.Fatal("takeover opened maintenance gate")
	}
	if err := reopened.ReleaseSoftwareUpdateGate("patch-1", "execution-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("old executor released new gate: %v", err)
	}
	if err := reopened.ReleaseSoftwareUpdateGate("patch-2", "execution-2"); err != nil {
		t.Fatal(err)
	}
	final, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if final.SoftwareUpdateMaintenanceActive() {
		t.Fatal("release was not durable")
	}
}

func TestSoftwareUpdateGateOwnershipAndRelease(t *testing.T) {
	repository := NewMemory()
	gate, err := repository.AcquireSoftwareUpdateGate("cgupgrade-2.2-71-to-2.2-72-x86_64", "execution-1")
	if err != nil || gate.ExecutionID != "execution-1" || !repository.SoftwareUpdateMaintenanceActive() {
		t.Fatalf("acquire software update gate: gate=%+v err=%v", gate, err)
	}
	if _, err := repository.AcquireSoftwareUpdateGate(gate.PatchID, gate.ExecutionID); err != nil {
		t.Fatalf("idempotent acquire: %v", err)
	}
	if _, err := repository.AcquireSoftwareUpdateGate(gate.PatchID, "execution-2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("competing acquire error = %v", err)
	}
	if err := repository.ReleaseSoftwareUpdateGate(gate.PatchID, "execution-2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign release error = %v", err)
	}
	if err := repository.ReleaseSoftwareUpdateGate(gate.PatchID, gate.ExecutionID); err != nil {
		t.Fatalf("release software update gate: %v", err)
	}
	if repository.SoftwareUpdateMaintenanceActive() {
		t.Fatal("software update gate remained active")
	}
}

func TestSoftwareUpdateGateRejectsInvalidIdentity(t *testing.T) {
	repository := NewMemory()
	if _, err := repository.AcquireSoftwareUpdateGate("bad patch", "execution-1"); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid patch error = %v", err)
	}
}

func TestSoftwareUpdateGateSurvivesReplicatedLeaderChange(t *testing.T) {
	leader, follower := NewMemory(), NewMemory()
	consensus := &snapshotConsensusStub{apply: func(state []byte) error {
		if err := leader.ApplyReplicatedState(state); err != nil {
			return err
		}
		return follower.ApplyReplicatedState(state)
	}}
	if err := leader.SetSnapshotConsensus(consensus); err != nil {
		t.Fatal(err)
	}
	if _, err := leader.AcquireSoftwareUpdateGate("patch", "run-1"); err != nil {
		t.Fatal(err)
	}
	if !follower.SoftwareUpdateMaintenanceActive() {
		t.Fatal("replicated follower lost maintenance")
	}
	newConsensus := &snapshotConsensusStub{apply: func(state []byte) error {
		if err := follower.ApplyReplicatedState(state); err != nil {
			return err
		}
		return leader.ApplyReplicatedState(state)
	}}
	if err := follower.SetSnapshotConsensus(newConsensus); err != nil {
		t.Fatal(err)
	}
	if err := follower.ReleaseSoftwareUpdateGate("patch", "wrong-run"); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign release: %v", err)
	}
	if err := follower.ReleaseSoftwareUpdateGate("patch", "run-1"); err != nil {
		t.Fatal(err)
	}
	if leader.SoftwareUpdateMaintenanceActive() || follower.SoftwareUpdateMaintenanceActive() {
		t.Fatal("committed release did not replicate")
	}
}
