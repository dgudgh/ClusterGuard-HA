package store

import (
	"fmt"
	"strings"
	"time"
)

// SoftwareUpdateGate is the Raft-replicated half of the platform update gate.
// Local markers protect bootstrap and restart boundaries; this record keeps a
// newly elected Leader fenced while those markers are released one by one.
type SoftwareUpdateGate struct {
	PatchID     string    `json:"patch_id"`
	ExecutionID string    `json:"execution_id"`
	AcquiredAt  time.Time `json:"acquired_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func validSoftwareUpdateIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validateSoftwareUpdateGate(gate SoftwareUpdateGate) error {
	if !validSoftwareUpdateIdentifier(gate.PatchID) || !validSoftwareUpdateIdentifier(gate.ExecutionID) ||
		gate.AcquiredAt.IsZero() || gate.UpdatedAt.IsZero() || gate.UpdatedAt.Before(gate.AcquiredAt) {
		return validationError("software update gate is invalid")
	}
	return nil
}

func (repository *Repository) SoftwareUpdateGate() (SoftwareUpdateGate, bool) {
	if repository == nil {
		return SoftwareUpdateGate{}, false
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if repository.snapshot.SoftwareUpdateGate == nil {
		return SoftwareUpdateGate{}, false
	}
	return *repository.snapshot.SoftwareUpdateGate, true
}

func (repository *Repository) SoftwareUpdateMaintenanceActive() bool {
	_, active := repository.SoftwareUpdateGate()
	return active
}

func (repository *Repository) AcquireSoftwareUpdateGate(patchID, executionID string) (SoftwareUpdateGate, error) {
	return repository.ClaimSoftwareUpdateGate(patchID, executionID, "", "")
}

// ClaimSoftwareUpdateGate transfers a fenced execution without opening the gate.
// The privileged updater must first acquire every local lock from the previous
// execution; the expected identity provides the final replicated CAS.
func (repository *Repository) ClaimSoftwareUpdateGate(patchID, executionID, previousPatchID, previousExecutionID string) (SoftwareUpdateGate, error) {
	patchID = strings.TrimSpace(patchID)
	executionID = strings.TrimSpace(executionID)
	if !validSoftwareUpdateIdentifier(patchID) || !validSoftwareUpdateIdentifier(executionID) {
		return SoftwareUpdateGate{}, validationError("software update gate identity is invalid")
	}
	if (previousPatchID != "" || previousExecutionID != "") &&
		(!validSoftwareUpdateIdentifier(previousPatchID) || !validSoftwareUpdateIdentifier(previousExecutionID)) {
		return SoftwareUpdateGate{}, validationError("previous software update gate identity is invalid")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if existing := repository.snapshot.SoftwareUpdateGate; existing != nil {
		if existing.PatchID == patchID && existing.ExecutionID == executionID {
			return *existing, nil
		}
		if existing.PatchID != previousPatchID || existing.ExecutionID != previousExecutionID {
			return SoftwareUpdateGate{}, conflictError("software update gate is owned by another execution")
		}
	}
	now := repository.now().UTC()
	gate := SoftwareUpdateGate{PatchID: patchID, ExecutionID: executionID, AcquiredAt: now, UpdatedAt: now}
	next := repository.snapshot
	next.SoftwareUpdateGate = &gate
	if err := repository.commitSnapshotLocked(next); err != nil {
		return SoftwareUpdateGate{}, fmt.Errorf("persist software update gate: %w", err)
	}
	return gate, nil
}

func (repository *Repository) ReleaseSoftwareUpdateGate(patchID, executionID string) error {
	patchID = strings.TrimSpace(patchID)
	executionID = strings.TrimSpace(executionID)
	if !validSoftwareUpdateIdentifier(patchID) || !validSoftwareUpdateIdentifier(executionID) {
		return validationError("software update gate identity is invalid")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	existing := repository.snapshot.SoftwareUpdateGate
	if existing == nil {
		return nil
	}
	if existing.PatchID != patchID || existing.ExecutionID != executionID {
		return conflictError("software update gate is owned by another execution")
	}
	next := repository.snapshot
	next.SoftwareUpdateGate = nil
	if err := repository.commitSnapshotLocked(next); err != nil {
		return fmt.Errorf("release software update gate: %w", err)
	}
	return nil
}
