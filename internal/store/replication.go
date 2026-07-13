package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

type SnapshotConsensus interface {
	Commit([]byte) error
}

func decodeSnapshotContents(contents []byte) (snapshot, error) {
	decoded := snapshot{}
	if err := json.Unmarshal(contents, &decoded); err != nil {
		return snapshot{}, err
	}
	if decoded.Clusters == nil {
		decoded.Clusters = map[model.ResourceID]model.DatabaseCluster{}
	}
	if decoded.Nodes == nil {
		decoded.Nodes = map[model.ResourceID]model.DatabaseNode{}
	}
	if decoded.Instances == nil {
		decoded.Instances = map[model.ResourceID]model.DatabaseInstance{}
	}
	if decoded.Endpoints == nil {
		decoded.Endpoints = map[model.ResourceID]map[model.ResourceID]model.Endpoint{}
	}
	if decoded.HAEndpoints == nil {
		decoded.HAEndpoints = map[model.ResourceID]model.HAEndpoint{}
	}
	if decoded.CoordinationLeases == nil {
		decoded.CoordinationLeases = map[model.ResourceID]coordination.LeaseRecord{}
	}
	if decoded.LifecycleTasks == nil {
		decoded.LifecycleTasks = map[model.ResourceID]lifecycle.Task{}
	}
	if decoded.ReplicationLinks == nil {
		decoded.ReplicationLinks = map[model.ResourceID][]model.ReplicationLink{}
	}
	if decoded.MetricSamples == nil {
		decoded.MetricSamples = map[model.ResourceID][]model.MetricSample{}
	}
	if decoded.TopologySnapshots == nil {
		decoded.TopologySnapshots = map[model.ResourceID]model.TopologySnapshot{}
	}
	if decoded.ObservationWatermarks == nil {
		decoded.ObservationWatermarks = map[model.ResourceID]time.Time{}
	}
	for clusterID, topology := range decoded.TopologySnapshots {
		if topology.ObservedAt.After(decoded.ObservationWatermarks[clusterID]) {
			decoded.ObservationWatermarks[clusterID] = topology.ObservedAt
		}
	}
	if decoded.InventoryGenerations == nil {
		decoded.InventoryGenerations = map[model.ResourceID]uint64{}
	}
	for clusterID := range decoded.Clusters {
		if decoded.InventoryGenerations[clusterID] == 0 {
			decoded.InventoryGenerations[clusterID] = 1
		}
	}
	if decoded.Anomalies == nil {
		decoded.Anomalies = map[model.ResourceID]model.MetadataAnomaly{}
	}
	if decoded.Operations == nil {
		decoded.Operations = map[model.ResourceID]model.OperationRecord{}
	}
	if decoded.OperationKeys == nil {
		decoded.OperationKeys = map[string]model.ResourceID{}
	}
	for resourceID, operation := range decoded.Operations {
		if operation.ResourceID == "" {
			operation.ResourceID = resourceID
		}
		if operation.Operation.ResourceID == "" {
			operation.Operation.ResourceID = operation.ResourceID
		}
		key := strings.TrimSpace(operation.IdempotencyKey)
		if operation.ResourceID != resourceID || !model.ValidResourceID(resourceID) || key == "" {
			return snapshot{}, fmt.Errorf("invalid operation record")
		}
		if existing, found := decoded.OperationKeys[key]; found && existing != resourceID {
			return snapshot{}, fmt.Errorf("duplicate operation idempotency key")
		}
		operation.IdempotencyKey = key
		decoded.Operations[resourceID] = cloneOperationRecord(operation)
		decoded.OperationKeys[key] = resourceID
	}
	if decoded.Audits == nil {
		decoded.Audits = []model.AuditEvent{}
	}
	if decoded.Reports == nil {
		decoded.Reports = []model.Report{}
	}
	for index := range decoded.Reports {
		if decoded.Reports[index].Status == "" {
			decoded.Reports[index].Status = model.OperationIndeterminate
			continue
		}
		if !terminalReportStatus(decoded.Reports[index].Status) {
			return snapshot{}, fmt.Errorf("report status is not terminal")
		}
	}
	return decoded, nil
}

func (repository *Repository) SetSnapshotConsensus(consensus SnapshotConsensus) error {
	if consensus == nil {
		return fmt.Errorf("snapshot consensus is required")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.consensus != nil {
		return fmt.Errorf("snapshot consensus is already configured")
	}
	repository.consensus = consensus
	return nil
}

func (repository *Repository) ReplicatedState() ([]byte, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return json.Marshal(repository.snapshot)
}

func (repository *Repository) ValidateReplicatedState(contents []byte) error {
	_, err := decodeSnapshotContents(contents)
	return err
}

func (repository *Repository) ApplyReplicatedState(contents []byte) error {
	decoded, err := decodeSnapshotContents(contents)
	if err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := repository.persistSnapshotLocked(decoded); err != nil {
		return err
	}
	repository.snapshot = decoded
	return nil
}

func (repository *Repository) commitSnapshotLocked(value snapshot) error {
	if repository.consensus == nil {
		return repository.persistSnapshotLocked(value)
	}
	contents, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode replicated metadata snapshot: %w", err)
	}
	if err := repository.consensus.Commit(contents); err != nil {
		return fmt.Errorf("commit metadata through controller quorum: %w", err)
	}
	if err := repository.persistSnapshotLocked(value); err != nil {
		repository.snapshot = value
		if errors.Is(err, ErrPostCommitDurability) {
			return err
		}
		return &postCommitDurabilityError{cause: err}
	}
	repository.snapshot = value
	return nil
}
