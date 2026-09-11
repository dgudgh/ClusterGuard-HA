package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"clusterguard.io/ha/internal/controlstate"
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

type snapshotConsensusCommitSynchronizer interface {
	SynchronizeForCommit() error
}

type snapshotConsensusProtocolGate interface {
	SnapshotCASActive() bool
}

const (
	snapshotStateRevisionField  = "clusterguard_state_revision"
	snapshotStateDigestField    = "clusterguard_state_digest"
	snapshotBaseDigestField     = "clusterguard_base_digest"
	maximumSnapshotBytes        = controlstate.MaximumBytes
	maximumEncodedSnapshotBytes = controlstate.MaximumEncodedBytes
)

type snapshotRevisionMetadata struct {
	StateRevision  *uint64 `json:"clusterguard_state_revision"`
	StateDigest    string  `json:"clusterguard_state_digest"`
	BaseDigest     string  `json:"clusterguard_base_digest"`
	ContentsDigest string  `json:"-"`
}

type snapshotStateEnvelope struct {
	snapshot
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
	_, contents, err := normalizedSnapshotContents(value)
	return contents, err
}

func normalizedSnapshotContents(value snapshot) (snapshot, []byte, error) {
	normalized, err := normalizeSnapshot(value)
	if err != nil {
		return snapshot{}, nil, err
	}
	contents, err := json.Marshal(normalized)
	return normalized, contents, err
}

func snapshotDigest(value snapshot) (string, error) {
	contents, err := canonicalSnapshotContents(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}

func normalizeSnapshot(value snapshot) (snapshot, error) {
	normalized := value
	if normalized.RecoveryTasks == nil {
		normalized.RecoveryTasks = map[model.ResourceID]model.RecoveryTask{}
	}
	for id, task := range normalized.RecoveryTasks {
		if id != task.ResourceID || !model.ValidResourceID(id) || !model.ValidResourceID(task.ClusterID) || !task.Stage.Valid() {
			return snapshot{}, fmt.Errorf("invalid disaster recovery task")
		}
	}
	if normalized.Clusters == nil {
		normalized.Clusters = map[model.ResourceID]model.DatabaseCluster{}
	}
	if normalized.Nodes == nil {
		normalized.Nodes = map[model.ResourceID]model.DatabaseNode{}
	}
	if normalized.RuntimeTargets == nil {
		normalized.RuntimeTargets = map[model.ResourceID]model.RuntimeTarget{}
	}
	if normalized.WorkloadBindings == nil {
		normalized.WorkloadBindings = map[model.ResourceID]model.WorkloadBinding{}
	}
	if normalized.Instances == nil {
		normalized.Instances = map[model.ResourceID]model.DatabaseInstance{}
	}
	for resourceID, target := range normalized.RuntimeTargets {
		if target.ResourceID != resourceID {
			return snapshot{}, fmt.Errorf("invalid runtime target record")
		}
		if err := validateRuntimeTarget(target); err != nil {
			return snapshot{}, err
		}
	}
	for resourceID, binding := range normalized.WorkloadBindings {
		if binding.ResourceID != resourceID {
			return snapshot{}, fmt.Errorf("invalid workload binding record")
		}
		if err := validateWorkloadBinding(normalized, binding); err != nil {
			return snapshot{}, err
		}
	}
	if normalized.Endpoints == nil {
		normalized.Endpoints = map[model.ResourceID]map[model.ResourceID]model.Endpoint{}
	}
	if normalized.HAEndpoints == nil {
		normalized.HAEndpoints = map[model.ResourceID]model.HAEndpoint{}
	}
	if normalized.PlatformUsers == nil {
		normalized.PlatformUsers = map[model.ResourceID]model.PlatformUser{}
	}
	normalizedUsernames := make(map[string]model.ResourceID, len(normalized.PlatformUsers))
	for resourceID, user := range normalized.PlatformUsers {
		if user.ResourceID != resourceID {
			return snapshot{}, fmt.Errorf("invalid platform user record")
		}
		if err := validatePlatformUser(user); err != nil {
			return snapshot{}, err
		}
		username := normalizePlatformUsername(user.Username)
		if existing, found := normalizedUsernames[username]; found && existing != resourceID {
			return snapshot{}, fmt.Errorf("duplicate platform username")
		}
		normalizedUsernames[username] = resourceID
	}
	if normalized.PlatformSessions == nil {
		normalized.PlatformSessions = map[model.ResourceID]model.PlatformSession{}
	}
	for resourceID, session := range normalized.PlatformSessions {
		if session.ResourceID != resourceID {
			return snapshot{}, fmt.Errorf("invalid platform session record")
		}
		if err := validatePlatformSession(session); err != nil {
			return snapshot{}, err
		}
		user, found := normalized.PlatformUsers[session.UserID]
		if !found || session.UserAuthRevision > user.AuthRevision {
			return snapshot{}, fmt.Errorf("platform session user identity is invalid")
		}
	}
	if normalized.ApprovalGrants == nil {
		normalized.ApprovalGrants = map[model.ResourceID]model.ApprovalGrant{}
	}
	for resourceID, grant := range normalized.ApprovalGrants {
		if grant.ResourceID != resourceID {
			return snapshot{}, fmt.Errorf("invalid approval grant record")
		}
		if err := validateApprovalGrant(grant); err != nil {
			return snapshot{}, err
		}
	}
	if normalized.CoordinationLeases == nil {
		normalized.CoordinationLeases = map[model.ResourceID]coordination.LeaseRecord{}
	}
	if normalized.OperationLocks == nil {
		normalized.OperationLocks = map[model.ResourceID]coordination.OperationLockRecord{}
	}
	if normalized.SoftwareUpdateGate != nil {
		gate := *normalized.SoftwareUpdateGate
		if err := validateSoftwareUpdateGate(gate); err != nil {
			return snapshot{}, err
		}
		normalized.SoftwareUpdateGate = &gate
	}
	if normalized.LifecycleTasks == nil {
		normalized.LifecycleTasks = map[model.ResourceID]lifecycle.Task{}
	}
	if normalized.ReplicationLinks == nil {
		normalized.ReplicationLinks = map[model.ResourceID][]model.ReplicationLink{}
	}
	if normalized.MetricSamples == nil {
		normalized.MetricSamples = map[model.ResourceID][]model.MetricSample{}
	}
	if normalized.TopologySnapshots == nil {
		normalized.TopologySnapshots = map[model.ResourceID]model.TopologySnapshot{}
	}
	if normalized.ObservationWatermarks == nil {
		normalized.ObservationWatermarks = map[model.ResourceID]time.Time{}
	}
	watermarksCopied := false
	for clusterID, topology := range normalized.TopologySnapshots {
		if !topology.ObservedAt.After(normalized.ObservationWatermarks[clusterID]) {
			continue
		}
		if !watermarksCopied {
			normalized.ObservationWatermarks = cloneTimeMap(normalized.ObservationWatermarks)
			watermarksCopied = true
		}
		normalized.ObservationWatermarks[clusterID] = topology.ObservedAt
	}
	if normalized.InventoryGenerations == nil {
		normalized.InventoryGenerations = map[model.ResourceID]uint64{}
	}
	inventoryCopied := false
	for clusterID := range normalized.Clusters {
		if normalized.InventoryGenerations[clusterID] != 0 {
			continue
		}
		if !inventoryCopied {
			normalized.InventoryGenerations = cloneUint64Map(normalized.InventoryGenerations)
			inventoryCopied = true
		}
		normalized.InventoryGenerations[clusterID] = 1
	}
	if normalized.Anomalies == nil {
		normalized.Anomalies = map[model.ResourceID]model.MetadataAnomaly{}
	}
	if normalized.Operations == nil {
		normalized.Operations = map[model.ResourceID]model.OperationRecord{}
	}
	if normalized.OperationKeys == nil {
		normalized.OperationKeys = map[string]model.ResourceID{}
	}
	operationsCopied := false
	keysCopied := false
	for resourceID, operation := range normalized.Operations {
		changed := false
		if operation.ResourceID == "" {
			operation.ResourceID = resourceID
			changed = true
		}
		if operation.Operation.ResourceID == "" {
			operation.Operation.ResourceID = operation.ResourceID
			changed = true
		}
		key := strings.TrimSpace(operation.IdempotencyKey)
		if operation.ResourceID != resourceID || !model.ValidResourceID(resourceID) || key == "" {
			return snapshot{}, fmt.Errorf("invalid operation record")
		}
		if err := validatePersistedOperationReview(operation); err != nil {
			return snapshot{}, fmt.Errorf("invalid operation review: %w", err)
		}
		if existing, found := normalized.OperationKeys[key]; found && existing != resourceID {
			return snapshot{}, fmt.Errorf("duplicate operation idempotency key")
		}
		if operation.IdempotencyKey != key {
			operation.IdempotencyKey = key
			changed = true
		}
		if changed {
			if !operationsCopied {
				normalized.Operations = cloneOperationMap(normalized.Operations)
				operationsCopied = true
			}
			normalized.Operations[resourceID] = cloneOperationRecord(operation)
		}
		if normalized.OperationKeys[key] != resourceID {
			if !keysCopied {
				normalized.OperationKeys = cloneOperationKeyMap(normalized.OperationKeys)
				keysCopied = true
			}
			normalized.OperationKeys[key] = resourceID
		}
	}
	if normalized.Audits == nil {
		normalized.Audits = []model.AuditEvent{}
	}
	if normalized.Reports == nil {
		normalized.Reports = []model.Report{}
	}
	if normalized.SecurityEvents == nil {
		normalized.SecurityEvents = []model.SecurityEvent{}
	}
	for _, event := range normalized.SecurityEvents {
		if err := validateSecurityEvent(event); err != nil {
			return snapshot{}, err
		}
	}
	reportsCopied := false
	for index := range normalized.Reports {
		if normalized.Reports[index].Status == "" {
			if !reportsCopied {
				normalized.Reports = append([]model.Report{}, normalized.Reports...)
				reportsCopied = true
			}
			normalized.Reports[index].Status = model.OperationIndeterminate
			continue
		}
		if !terminalOperationStatus(normalized.Reports[index].Status) {
			return snapshot{}, fmt.Errorf("report status is not terminal")
		}
	}
	return normalized, nil
}

func encodeSnapshotRevisionState(value snapshot, stateRevision uint64, baseDigest string) ([]byte, string, snapshot, error) {
	normalized, contents, err := normalizedSnapshotContents(value)
	if err != nil {
		return nil, "", snapshot{}, err
	}
	if len(contents) > maximumEncodedSnapshotBytes {
		return nil, "", snapshot{}, validationError("metadata snapshot exceeds maximum size")
	}
	if len(contents) < 2 || contents[0] != '{' || contents[len(contents)-1] != '}' {
		return nil, "", snapshot{}, fmt.Errorf("metadata snapshot must encode as an object")
	}
	digest := sha256.Sum256(contents)
	stateDigest := hex.EncodeToString(digest[:])
	prefix := fmt.Sprintf(`{"%s":%d,"%s":"%s",`, snapshotStateRevisionField, stateRevision, snapshotStateDigestField, stateDigest)
	if baseDigest != "" {
		if !validSnapshotDigest(baseDigest) {
			return nil, "", snapshot{}, fmt.Errorf("metadata snapshot base digest is invalid")
		}
		prefix = fmt.Sprintf(`{"%s":%d,"%s":"%s","%s":"%s",`, snapshotStateRevisionField, stateRevision, snapshotStateDigestField, stateDigest, snapshotBaseDigestField, baseDigest)
	}
	if len(prefix)+len(contents)-1 > maximumEncodedSnapshotBytes {
		return nil, "", snapshot{}, validationError("metadata snapshot exceeds maximum size")
	}
	encoded := make([]byte, 0, len(prefix)+len(contents)-1)
	encoded = append(encoded, prefix...)
	encoded = append(encoded, contents[1:]...)
	return encoded, stateDigest, normalized, nil
}

func encodeSnapshotRevisionWithDigest(value snapshot, stateRevision uint64, baseDigest string) ([]byte, string, error) {
	contents, digest, _, err := encodeSnapshotRevisionState(value, stateRevision, baseDigest)
	return contents, digest, err
}

func encodeSnapshotRevision(value snapshot, stateRevision uint64, baseDigest string) ([]byte, error) {
	contents, _, err := encodeSnapshotRevisionWithDigest(value, stateRevision, baseDigest)
	return contents, err
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

func snapshotPayload(contents []byte) ([]byte, error) {
	marker := []byte(`"clusters":`)
	index := bytes.Index(contents, marker)
	if index < 0 {
		return nil, fmt.Errorf("metadata snapshot payload is missing")
	}
	previous := index - 1
	for previous >= 0 {
		switch contents[previous] {
		case ' ', '\t', '\r', '\n':
			previous--
			continue
		}
		break
	}
	if previous < 0 || (contents[previous] != '{' && contents[previous] != ',') {
		return nil, fmt.Errorf("metadata snapshot payload is missing")
	}
	payload := make([]byte, 1+len(contents)-index)
	payload[0] = '{'
	copy(payload[1:], contents[index:])
	return payload, nil
}

func digestSnapshotPayload(contents []byte) (string, error) {
	payload, err := snapshotPayload(contents)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func decodeSnapshotState(contents []byte) (snapshot, snapshotRevisionMetadata, error) {
	if len(contents) == 0 || len(contents) > maximumSnapshotBytes {
		return snapshot{}, snapshotRevisionMetadata{}, validationError("metadata snapshot exceeds maximum size")
	}
	if _, err := snapshotPayload(contents); err != nil {
		return snapshot{}, snapshotRevisionMetadata{}, err
	}
	envelope := snapshotStateEnvelope{}
	if err := json.Unmarshal(contents, &envelope); err != nil {
		return snapshot{}, snapshotRevisionMetadata{}, err
	}
	metadata := snapshotRevisionMetadata{
		StateRevision: envelope.StateRevision,
		StateDigest:   envelope.StateDigest,
		BaseDigest:    envelope.BaseDigest,
	}
	metadata.StateDigest = strings.ToLower(strings.TrimSpace(metadata.StateDigest))
	metadata.BaseDigest = strings.ToLower(strings.TrimSpace(metadata.BaseDigest))
	if metadata.StateDigest != "" && (!validSnapshotDigest(metadata.StateDigest) || metadata.StateRevision == nil) {
		return snapshot{}, snapshotRevisionMetadata{}, fmt.Errorf("metadata snapshot state digest is invalid")
	}
	if metadata.BaseDigest != "" && (!validSnapshotDigest(metadata.BaseDigest) || metadata.StateDigest == "" || metadata.StateRevision == nil || *metadata.StateRevision == 0) {
		return snapshot{}, snapshotRevisionMetadata{}, fmt.Errorf("metadata snapshot base digest is invalid")
	}

	decoded, err := normalizeSnapshot(envelope.snapshot)
	if err != nil {
		return snapshot{}, snapshotRevisionMetadata{}, err
	}
	if metadata.StateDigest != "" {
		actualDigest, err := digestSnapshotPayload(contents)
		if err != nil {
			return snapshot{}, snapshotRevisionMetadata{}, err
		}
		if actualDigest != metadata.StateDigest {
			return snapshot{}, snapshotRevisionMetadata{}, fmt.Errorf("metadata snapshot state digest does not match contents")
		}
		metadata.ContentsDigest = actualDigest
	} else {
		actualDigest, err := snapshotDigest(decoded)
		if err != nil {
			return snapshot{}, snapshotRevisionMetadata{}, err
		}
		metadata.ContentsDigest = actualDigest
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

// ApplyValidatesReplicatedState tells the Raft FSM that Apply performs the
// same fail-closed validation before changing repository state.
func (*Repository) ApplyValidatesReplicatedState() bool { return true }

func (repository *Repository) RestoreReplicatedState(contents []byte) error {
	return repository.applyReplicatedState(contents, true)
}

func (repository *Repository) applyReplicatedState(contents []byte, authoritative bool) error {
	decoded, metadata, err := decodeSnapshotState(contents)
	if err != nil {
		return err
	}
	if len(contents) > maximumEncodedSnapshotBytes {
		// Legacy snapshots predate bounded history retention. A zero cutoff keeps
		// unrevoked sessions while deterministically trimming replicated terminal
		// history on every controller during the upgrade.
		decoded = compactSnapshotHistory(decoded, time.Time{})
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	currentDigest := repository.stateDigest
	if currentDigest == "" {
		currentDigest, err = snapshotDigest(repository.snapshot)
		if err != nil {
			return err
		}
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
	if !authoritative && metadata.StateRevision != nil {
		switch {
		case *metadata.StateRevision < repository.stateRevision:
			return conflictError("replicated metadata revision %d is older than local revision %d", *metadata.StateRevision, repository.stateRevision)
		case *metadata.StateRevision == repository.stateRevision:
			if reflect.DeepEqual(decoded, repository.snapshot) {
				return nil
			}
			return conflictError("replicated metadata revision %d conflicts with local state", *metadata.StateRevision)
		}
		// A Raft log entry with a newer revision is already committed. Because
		// every entry contains the complete state, it is also the recovery path
		// for a follower whose durable metadata drifted behind the applied log.
		// Base-digest CAS is enforced before proposal; rejecting a newer committed
		// entry here would advance Raft's applied index while leaving the follower
		// permanently stale.
	} else if !authoritative && metadata.BaseDigest != "" && metadata.BaseDigest != currentDigest {
		return conflictError("replicated metadata base digest does not match local state")
	}
	if err := repository.persistSnapshotRevisionLocked(decoded, nextRevision); err != nil {
		return err
	}
	return nil
}

func (repository *Repository) commitSnapshotLocked(value snapshot) error {
	value = compactSnapshotHistory(value, repository.now().UTC())
	if repository.consensus == nil {
		return repository.persistSnapshotRevisionLocked(value, repository.stateRevision+1)
	}
	if gate, ok := repository.consensus.(snapshotConsensusProtocolGate); ok && !gate.SnapshotCASActive() {
		return conflictError("snapshot CAS protocol is not activated on the controller quorum")
	}
	baseRevision := repository.stateRevision
	baseDigest := repository.stateDigest
	if baseDigest == "" {
		calculated, digestErr := snapshotDigest(repository.snapshot)
		if digestErr != nil {
			return fmt.Errorf("digest replicated metadata base snapshot: %w", digestErr)
		}
		baseDigest = calculated
	}
	contents, err := encodeSnapshotRevision(value, baseRevision+1, baseDigest)
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
	if synchronizer, ok := repository.consensus.(snapshotConsensusCommitSynchronizer); ok {
		repository.mu.Unlock()
		synchronizeErr := synchronizer.SynchronizeForCommit()
		repository.mu.Lock()
		if synchronizeErr != nil {
			return fmt.Errorf("synchronize controller state before commit: %w", synchronizeErr)
		}
		if repository.stateRevision != baseRevision {
			return conflictError("metadata changed while synchronizing controller state")
		}
	} else if synchronizer, ok := repository.consensus.(snapshotConsensusSynchronizer); ok {
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
