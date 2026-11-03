package store

import (
	"bytes"
	"clusterguard.io/ha/pkg/model"
	"io"
	"os"
)

func runtimeRequirements(s snapshot) model.RuntimeCompatibilityFloor {
	if s.RuntimeCompatibilityFloor != nil {
		return *s.RuntimeCompatibilityFloor
	}
	// Legacy persisted policies already need protection, including disabled ones.
	for _, c := range s.Clusters {
		if c.FailoverPolicy != nil {
			return model.PolicyCompatibilityFloor()
		}
	}
	return model.RuntimeCompatibilityFloor{}
}
func (r *Repository) RuntimeCompatibilityRequirements() model.RuntimeCompatibilityFloor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return runtimeRequirements(r.snapshot)
}
func preserveRuntimeFloor(next *snapshot, previous snapshot) error {
	if previous.RuntimeCompatibilityFloor != nil && next.RuntimeCompatibilityFloor == nil {
		return conflictError("runtime compatibility floor cannot be removed")
	}
	before, after := runtimeRequirements(previous), runtimeRequirements(*next)
	if !after.Covers(before) {
		return conflictError("runtime compatibility floor cannot decrease")
	}
	return nil
}

// Restriction-only inspection of an unreadable generation: never accept state
// from these bytes. A recognizable persisted policy/floor means an older
// generation cannot establish the missing compatibility or policy semantics.
func protectedRuntimeGeneration(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maximumSnapshotBytes+1))
	if err != nil {
		return true
	}
	return bytes.Contains(data, []byte(`"runtime_compatibility_floor"`)) || bytes.Contains(data, []byte(`"failover_policy"`))
}
