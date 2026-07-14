package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

type SnapshotConsensus interface {
	Commit([]byte) error
}

type snapshotConsensusSynchronizer interface {
	Synchronize() error
}

type snapshotConsensusProtocolGate interface {
	SnapshotCASActive() bool
}

const (
	snapshotStateRevisionField = "clusterguard_state_revision"
	snapshotStateDigestField   = "clusterguard_state_digest"
	snapshotBaseDigestField    = "clusterguard_base_digest"
)

type snapshotRevisionMetadata struct {
	StateRevision *uint64 `json:"clusterguard_state_revision"`
	StateDigest   string  `json:"clusterguard_state_digest"`
	BaseDigest    string  `json:"clusterguard_base_digest"`
}

func validSnapshotDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func canonicalSnapshotContents(value snapshot) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	normalized, _, err := decodeSnapshotState(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func snapshotDigest(value snapshot) (string, error) {
	contents, err := canonicalSnapshotContents(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}

func encodeSnapshotRevision(value snapshot, stateRevision uint64, baseDigest string) ([]byte, error) {
	contents, err := canonicalSnapshotContents(value)
	if err != nil {
		return nil, err
	}
	if len(contents) < 2 || contents[0] != '{' || contents[len(contents)-1] != '}' {
		return nil, fmt.Errorf("metadata snapshot must encode as an object")
	}
	digest := sha256.Sum256(contents)
	stateDigest := hex.EncodeToString(digest[:])
	prefix := fmt.Sprintf(`{"%s":%d,"%s":"%s",`, snapshotStateRevisionField, stateRevision, snapshotStateDigestField, stateDigest)
	if baseDigest != "" {
		if !validSnapshotDigest(baseDigest) {
			return nil, fmt.Errorf("metadata snapshot base digest is invalid")
		}
		prefix = fmt.Sprintf(`{"%s":%d,"%s":"%s","%s":"%s",`, snapshotStateRevisionField, stateRevision, snapshotStateDigestField, stateDigest, snapshotBaseDigestField, baseDigest)
	}
	encoded := make([]byte, 0, len(prefix)+len(contents)-1)
	encoded = append(encoded, prefix...)
	encoded = append(encoded, contents[1:]...)
	return encoded, nil
}

func encodeConsensusSnapshot(value, base snapshot, stateRevision uint64) ([]byte, error) {
	if stateRevision == 0 {
		return nil, fmt.Errorf("consensus snapshot revision is invalid")
	}
	baseDigest, err := snapshotDigest(base)
	if err != nil {
		return nil, err
	}
	return encodeSnapshotRevision(value, stateRevision, baseDigest)
}

func decodeSnapshotState(contents []byte) (snapshot, snapshotRevisionMetadata, error) {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(contents, &raw); err != nil {
		return snapshot{}, snapshotRevisionMetadata{}, err
	}
	if _, found := raw["clusters"]; !found {
		return snapshot{}, snapshotRevisionMetadata{}, fmt.Errorf("metadata snapshot payload is missing")
	}
	metadata := snapshotRevisionMetadata{}
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return snapshot{}, snapshotRevisionMetadata{}, err
	}
	metadata.StateDigest = strings.ToLower(strings.TrimSpace(metadata.StateDigest))
	metadata.BaseDigest = strings.ToLower(strings.TrimSpace(metadata.BaseDigest))
	if metadata.StateDigest != "" && (!validSnapshotDigest(metadata.StateDigest) || metadata.StateRevision == nil) {
		return snapshot{}, snapshotRevisionMetadata{}, fmt.Errorf("metadata snapshot state digest is invalid")
	}
	if metadata.BaseDigest != "" && (!validSnapshotDigest(metadata.BaseDigest) || metadata.StateDigest == "" || metadata.StateRevision == nil || *metadata.StateRevision == 0) {
		return snapshot{}, snapshotRevisionMetadata{}, fmt.Errorf("metadata snapshot base digest is invalid")
	}

	decoded := snapshot{}
	if err := json.Unmarshal(contents, &decoded); err != nil {
		return snapshot{}, snapshotRevisionMetadata{}, err
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
	if decoded.OperationLocks == nil {
		decoded.OperationLocks = map[model.ResourceID]coordination.OperationLockRecord{}
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
			return snapshot{}, snapshotRevisionMetadata{}, fmt.Errorf("invalid operation record")
		}
		if existing, found := decoded.OperationKeys[key]; found && existing != resourceID {
			return snapshot{}, snapshotRevisionMetadata{}, fmt.Errorf("duplicate operation idempotency key")
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
			return snapshot{}, snapshotRevisionMetadata{}, fmt.Errorf("report status is not terminal")
		}
	}
	if metadata.StateDigest != "" {
		actualDigest, err := snapshotDigest(decoded)
		if err != nil {
			return snapshot{}, snapshotRevisionMetadata{}, err
		}
		if actualDigest != metadata.StateDigest {
			return snapshot{}, snapshotRevisionMetadata{}, fmt.Errorf("metadata snapshot state digest does not match contents")
		}
	}
	return decoded, metadata, nil
}

func decodeSnapshotContents(contents []byte) (snapshot, error) {
	decoded, _, err := decodeSnapshotState(contents)
	return decoded, err
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
	return encodeSnapshotRevision(repository.snapshot, repository.stateRevision, "")
}

func (repository *Repository) ValidateReplicatedState(contents []byte) error {
	_, err := decodeSnapshotContents(contents)
	return err
}

func (repository *Repository) ApplyReplicatedState(contents []byte) error {
	return repository.applyReplicatedState(contents, false)
}

func (repository *Repository) RestoreReplicatedState(contents []byte) error {
	return repository.applyReplicatedState(contents, true)
}

func (repository *Repository) applyReplicatedState(contents []byte, authoritative bool) error {
	decoded, metadata, err := decodeSnapshotState(contents)
	if err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	currentDigest, err := snapshotDigest(repository.snapshot)
	if err != nil {
		return err
	}
	if !authoritative && metadata.StateDigest != "" && metadata.StateDigest == currentDigest {
		if reflect.DeepEqual(decoded, repository.snapshot) {
			return nil
		}
		return conflictError("replicated metadata digest has different contents")
	}
	nextRevision := repository.stateRevision + 1
	if metadata.StateRevision != nil {
		nextRevision = *metadata.StateRevision
	}
	if !authoritative && metadata.BaseDigest != "" && metadata.BaseDigest != currentDigest {
		return conflictError("replicated metadata base digest does not match local state")
	}
	if !authoritative && metadata.BaseDigest == "" && metadata.StateRevision != nil && *metadata.StateRevision < repository.stateRevision {
		return conflictError("replicated metadata revision %d is older than local revision %d", *metadata.StateRevision, repository.stateRevision)
	}
	if err := repository.persistSnapshotRevisionLocked(decoded, nextRevision); err != nil {
		return err
	}
	return nil
}

func (repository *Repository) commitSnapshotLocked(value snapshot) error {
	if repository.consensus == nil {
		return repository.persistSnapshotRevisionLocked(value, repository.stateRevision+1)
	}
	if gate, ok := repository.consensus.(snapshotConsensusProtocolGate); ok && !gate.SnapshotCASActive() {
		return conflictError("snapshot CAS protocol is not activated on the controller quorum")
	}
	baseRevision := repository.stateRevision
	contents, err := encodeConsensusSnapshot(value, repository.snapshot, baseRevision+1)
	if err != nil {
		return fmt.Errorf("encode replicated metadata snapshot: %w", err)
	}

	repository.mu.Unlock()
	repository.consensusCommit.Lock()
	repository.mu.Lock()
	defer repository.consensusCommit.Unlock()
	if repository.stateRevision != baseRevision {
		return conflictError("metadata changed while waiting to synchronize controller state")
	}
	if synchronizer, ok := repository.consensus.(snapshotConsensusSynchronizer); ok {
		repository.mu.Unlock()
		synchronizeErr := synchronizer.Synchronize()
		repository.mu.Lock()
		if synchronizeErr != nil {
			return fmt.Errorf("synchronize controller state before commit: %w", synchronizeErr)
		}
		if repository.stateRevision != baseRevision {
			return conflictError("metadata changed while synchronizing controller state")
		}
	}
	repository.mu.Unlock()
	commitErr := repository.consensus.Commit(contents)
	repository.mu.Lock()
	if commitErr != nil {
		return fmt.Errorf("commit metadata through controller quorum: %w", commitErr)
	}
	if repository.stateRevision != baseRevision+1 {
		return conflictError("committed metadata revision was not applied locally")
	}
	return nil
}
