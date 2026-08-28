package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"sync"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type MutationAuthority interface {
	RequireMutationAuthority(context.Context) error
}

type AuthoritySafetyGuard struct {
	Authority MutationAuthority
}

func (guard AuthoritySafetyGuard) Evaluate(ctx context.Context, operation model.Operation) error {
	if guard.Authority == nil {
		return fmt.Errorf("mutation authority safety guard is not configured")
	}
	if !model.ValidResourceID(operation.ResourceID) || !model.ValidResourceID(operation.ClusterID) {
		return fmt.Errorf("operation and cluster UUIDs are required by the safety guard")
	}
	if err := guard.Authority.RequireMutationAuthority(ctx); err != nil {
		return fmt.Errorf("leader-backed controller majority is required: %w", err)
	}
	return nil
}

type MaintenanceChecker interface {
	Check(context.Context) error
}

type MaintenanceSafetyGuard struct {
	Gate MaintenanceChecker
}

func (guard MaintenanceSafetyGuard) Evaluate(ctx context.Context, _ model.Operation) error {
	if guard.Gate == nil {
		return fmt.Errorf("software update maintenance guard is not configured")
	}
	if err := guard.Gate.Check(ctx); err != nil {
		return fmt.Errorf("software update maintenance gate blocked execution: %w", err)
	}
	return nil
}

type CompositeSafetyGuard struct {
	guards []SafetyGuard
}

func NewCompositeSafetyGuard(guards ...SafetyGuard) CompositeSafetyGuard {
	return CompositeSafetyGuard{guards: append([]SafetyGuard{}, guards...)}
}

func (guard CompositeSafetyGuard) Evaluate(ctx context.Context, operation model.Operation) error {
	if len(guard.guards) == 0 {
		return fmt.Errorf("no safety guards are configured")
	}
	for _, candidate := range guard.guards {
		if candidate == nil {
			return fmt.Errorf("nil safety guard is configured")
		}
		if err := candidate.Evaluate(ctx, operation); err != nil {
			return err
		}
	}
	return nil
}

type TopologyReader interface {
	TopologySnapshot(model.ResourceID) (model.TopologySnapshot, bool)
}

type TopologyDiscovery struct {
	Reader TopologyReader
}

type topologyObservation struct {
	ClusterID model.ResourceID              `json:"cluster_id"`
	Instances []topologyObservationInstance `json:"instances"`
	Links     []topologyObservationLink     `json:"links"`
	Probes    []topologyObservationProbe    `json:"probes"`
	Health    topologyObservationHealth     `json:"health"`
	Anomalies []topologyObservationAnomaly  `json:"anomalies"`
}

type topologyObservationHealth struct {
	State       model.HealthState `json:"state"`
	Replication string            `json:"replication"`
}

type topologyObservationInstance struct {
	ResourceID        model.ResourceID          `json:"resource_id"`
	ClusterID         model.ResourceID          `json:"cluster_id"`
	NodeID            model.ResourceID          `json:"node_id"`
	Engine            model.Engine              `json:"engine"`
	EngineIdentity    model.EngineIdentity      `json:"engine_identity"`
	DisplayName       string                    `json:"display_name"`
	Hostname          string                    `json:"hostname"`
	IPAddress         string                    `json:"ip_address"`
	Port              int                       `json:"port"`
	Aliases           []string                  `json:"aliases"`
	Role              model.InstanceRole        `json:"role"`
	Health            topologyObservationHealth `json:"health"`
	SourceIdentity    model.EngineIdentity      `json:"source_identity"`
	IOThread          model.ThreadState         `json:"io_thread"`
	SQLThread         model.ThreadState         `json:"sql_thread"`
	LastError         string                    `json:"last_error"`
	Maintenance       bool                      `json:"maintenance"`
	PromotionEligible bool                      `json:"promotion_eligible"`
	EngineMetadata    map[string]string         `json:"engine_metadata"`
}

type topologyObservationLink struct {
	ResourceID model.ResourceID `json:"resource_id"`
	SourceID   model.ResourceID `json:"source_id"`
	TargetID   model.ResourceID `json:"target_id"`
	Healthy    bool             `json:"healthy"`
}

type topologyObservationProbe struct {
	EndpointID model.ResourceID          `json:"endpoint_id"`
	InstanceID model.ResourceID          `json:"instance_id"`
	Health     topologyObservationHealth `json:"health"`
}

type topologyObservationAnomaly struct {
	ClusterID model.ResourceID `json:"cluster_id"`
	Engine    model.Engine     `json:"engine"`
	Kind      string           `json:"kind"`
	Severity  string           `json:"severity"`
	Message   string           `json:"message"`
}

var mysqlReconnectAttemptPattern = regexp.MustCompile(`(?i)(\bthis was attempt )\d+(/\d+\b)`)

func stableReplicationError(value string) string {
	return mysqlReconnectAttemptPattern.ReplaceAllString(value, "${1}*${2}")
}

func stableHealth(value model.Health) topologyObservationHealth {
	return topologyObservationHealth{State: value.State, Replication: value.Replication}
}

func stableEngineMetadata(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		switch key {
		case "gtid_executed", "current_lsn", "receive_lsn", "replay_lsn",
			"receiver_latest_end_lsn", "transport_lag_seconds", "apply_lag_seconds":
			continue
		}
		result[key] = value
	}
	return result
}

func stablePromotionEligible(instance model.DatabaseInstance) bool {
	if instance.Engine == model.EngineOracle {
		return instance.Role == model.RoleStandby && instance.Health.State == model.HealthHealthy
	}
	return instance.PromotionEligible
}

func topologyDigest(snapshot model.TopologySnapshot) (string, error) {
	observation := topologyObservation{
		ClusterID: snapshot.ClusterID,
		Health:    stableHealth(snapshot.Health),
		Instances: make([]topologyObservationInstance, 0, len(snapshot.Instances)),
		Links:     make([]topologyObservationLink, 0, len(snapshot.Links)),
		Probes:    make([]topologyObservationProbe, 0, len(snapshot.Probes)),
		Anomalies: make([]topologyObservationAnomaly, 0, len(snapshot.Anomalies)),
	}
	for _, instance := range snapshot.Instances {
		aliases := append([]string{}, instance.Aliases...)
		sort.Strings(aliases)
		observation.Instances = append(observation.Instances, topologyObservationInstance{
			ResourceID: instance.ResourceID, ClusterID: instance.ClusterID, NodeID: instance.NodeID,
			Engine: instance.Engine, EngineIdentity: instance.EngineIdentity.Clone(), DisplayName: instance.DisplayName,
			Hostname: instance.Hostname, IPAddress: instance.IPAddress, Port: instance.Port, Aliases: aliases,
			Role: instance.Role, Health: stableHealth(instance.Health), SourceIdentity: instance.Replication.SourceIdentity.Clone(),
			IOThread: instance.Replication.IOThread, SQLThread: instance.Replication.SQLThread, LastError: stableReplicationError(instance.Replication.LastError),
			Maintenance: instance.Maintenance, PromotionEligible: stablePromotionEligible(instance),
			EngineMetadata: stableEngineMetadata(instance.EngineMetadata),
		})
	}
	sort.Slice(observation.Instances, func(i, j int) bool { return observation.Instances[i].ResourceID < observation.Instances[j].ResourceID })
	for _, link := range snapshot.Links {
		observation.Links = append(observation.Links, topologyObservationLink{
			ResourceID: link.ResourceID, SourceID: link.SourceInstanceID, TargetID: link.TargetInstanceID, Healthy: link.Healthy,
		})
	}
	sort.Slice(observation.Links, func(i, j int) bool {
		left, right := observation.Links[i], observation.Links[j]
		if left.SourceID != right.SourceID {
			return left.SourceID < right.SourceID
		}
		if left.TargetID != right.TargetID {
			return left.TargetID < right.TargetID
		}
		return left.ResourceID < right.ResourceID
	})
	for _, probe := range snapshot.Probes {
		observation.Probes = append(observation.Probes, topologyObservationProbe{
			EndpointID: probe.EndpointID, InstanceID: probe.InstanceID, Health: stableHealth(probe.Health),
		})
	}
	sort.Slice(observation.Probes, func(i, j int) bool {
		if observation.Probes[i].EndpointID != observation.Probes[j].EndpointID {
			return observation.Probes[i].EndpointID < observation.Probes[j].EndpointID
		}
		return observation.Probes[i].InstanceID < observation.Probes[j].InstanceID
	})
	for _, anomaly := range snapshot.Anomalies {
		observation.Anomalies = append(observation.Anomalies, topologyObservationAnomaly{
			ClusterID: anomaly.ClusterID, Engine: anomaly.Engine,
			Kind: anomaly.Kind, Severity: anomaly.Severity, Message: anomaly.Message,
		})
	}
	sort.Slice(observation.Anomalies, func(i, j int) bool {
		left, right := observation.Anomalies[i], observation.Anomalies[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Severity != right.Severity {
			return left.Severity < right.Severity
		}
		return left.Message < right.Message
	})
	encoded, err := json.Marshal(observation)
	if err != nil {
		return "", fmt.Errorf("encode topology observation: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (gate TopologyDiscovery) CaptureObservation(ctx context.Context, operation model.Operation) (ObservationToken, error) {
	if err := ctx.Err(); err != nil {
		return ObservationToken{}, err
	}
	if gate.Reader == nil {
		return ObservationToken{}, fmt.Errorf("topology reader is not configured")
	}
	if operation.ClusterID == "" {
		return ObservationToken{}, fmt.Errorf("cluster ID is required for discovery validation")
	}
	snapshot, found := gate.Reader.TopologySnapshot(operation.ClusterID)
	if !found || snapshot.ObservedAt.IsZero() {
		return ObservationToken{}, fmt.Errorf("a current topology observation is required")
	}
	if snapshot.ClusterID != "" && snapshot.ClusterID != operation.ClusterID {
		return ObservationToken{}, fmt.Errorf("topology observation belongs to another cluster")
	}
	digest, err := topologyDigest(snapshot)
	if err != nil {
		return ObservationToken{}, err
	}
	return ObservationToken{ClusterID: operation.ClusterID, ObservedAt: snapshot.ObservedAt.UTC(), Digest: digest, Snapshot: snapshot}, nil
}

func (gate TopologyDiscovery) RevalidateObservation(ctx context.Context, operation model.Operation, token ObservationToken) error {
	if token.ClusterID != operation.ClusterID || token.ObservedAt.IsZero() {
		return fmt.Errorf("topology observation token does not match the operation")
	}
	current, err := gate.CaptureObservation(ctx, operation)
	if err != nil {
		return err
	}
	if current.ClusterID != token.ClusterID || (token.Digest != "" && current.Digest != token.Digest) ||
		(token.Digest == "" && !current.ObservedAt.Equal(token.ObservedAt)) {
		return fmt.Errorf("topology observation changed")
	}
	return nil
}

type AllowAllSafety struct{}

func (AllowAllSafety) Evaluate(context.Context, model.Operation) error { return nil }

type MemoryLocks struct {
	mu      sync.Mutex
	active  map[string]bool
	waiters map[string][]*memoryLockWaiter
}

type memoryLockWaiter struct {
	ready chan struct{}
}

type ClusterLockManager interface {
	Acquire(context.Context, model.Operation) (context.Context, func(), error)
	AcquireCluster(context.Context, model.ResourceID) (context.Context, func(), error)
}

type CompositeLocks struct {
	local  ClusterLockManager
	quorum ClusterLockManager
}

func NewCompositeLocks(local, quorum ClusterLockManager) *CompositeLocks {
	return &CompositeLocks{local: local, quorum: quorum}
}

func (locks *CompositeLocks) Acquire(ctx context.Context, operation model.Operation) (context.Context, func(), error) {
	if locks == nil || locks.local == nil || locks.quorum == nil {
		return nil, nil, fmt.Errorf("composite operation lock is not configured")
	}
	return acquireComposite(ctx,
		func(acquireCtx context.Context) (context.Context, func(), error) {
			return locks.local.Acquire(acquireCtx, operation)
		},
		func(acquireCtx context.Context) (context.Context, func(), error) {
			return locks.quorum.Acquire(acquireCtx, operation)
		},
	)
}

func (locks *CompositeLocks) AcquireCluster(ctx context.Context, clusterID model.ResourceID) (context.Context, func(), error) {
	if locks == nil || locks.local == nil || locks.quorum == nil {
		return nil, nil, fmt.Errorf("composite operation lock is not configured")
	}
	return acquireComposite(ctx,
		func(acquireCtx context.Context) (context.Context, func(), error) {
			return locks.local.AcquireCluster(acquireCtx, clusterID)
		},
		func(acquireCtx context.Context) (context.Context, func(), error) {
			return locks.quorum.AcquireCluster(acquireCtx, clusterID)
		},
	)
}

func acquireComposite(ctx context.Context, acquireLocal, acquireQuorum func(context.Context) (context.Context, func(), error)) (context.Context, func(), error) {
	localCtx, releaseLocal, err := acquireLocal(ctx)
	if err != nil {
		return nil, nil, err
	}
	quorumCtx, releaseQuorum, err := acquireQuorum(localCtx)
	if err != nil {
		releaseLocal()
		return nil, nil, err
	}
	var once sync.Once
	return quorumCtx, func() {
		once.Do(func() {
			releaseQuorum()
			releaseLocal()
		})
	}, nil
}

func NewMemoryLocks() *MemoryLocks {
	return &MemoryLocks{active: map[string]bool{}, waiters: map[string][]*memoryLockWaiter{}}
}

func (locks *MemoryLocks) Acquire(ctx context.Context, operation model.Operation) (context.Context, func(), error) {
	key := string(operation.ClusterID)
	if key == "" {
		key = string(operation.ResourceID)
	}
	return locks.acquire(ctx, key)
}

func (locks *MemoryLocks) AcquireCluster(ctx context.Context, clusterID model.ResourceID) (context.Context, func(), error) {
	return locks.acquire(ctx, string(clusterID))
}

func (locks *MemoryLocks) acquire(ctx context.Context, key string) (context.Context, func(), error) {
	if key == "" {
		return nil, nil, fmt.Errorf("operation lock resource is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	locks.mu.Lock()
	if locks.active == nil {
		locks.active = map[string]bool{}
	}
	if locks.waiters == nil {
		locks.waiters = map[string][]*memoryLockWaiter{}
	}
	if !locks.active[key] && len(locks.waiters[key]) == 0 {
		locks.active[key] = true
		locks.mu.Unlock()
		leaseCtx, release := locks.memoryLease(ctx, key)
		return leaseCtx, release, nil
	}
	waiter := &memoryLockWaiter{ready: make(chan struct{})}
	locks.waiters[key] = append(locks.waiters[key], waiter)
	locks.mu.Unlock()

	select {
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			locks.releaseMemoryLease(key)
			return nil, nil, err
		}
		leaseCtx, release := locks.memoryLease(ctx, key)
		return leaseCtx, release, nil
	case <-ctx.Done():
		locks.mu.Lock()
		queue := locks.waiters[key]
		for index, candidate := range queue {
			if candidate != waiter {
				continue
			}
			queue = append(queue[:index], queue[index+1:]...)
			if len(queue) == 0 {
				delete(locks.waiters, key)
			} else {
				locks.waiters[key] = queue
			}
			locks.mu.Unlock()
			return nil, nil, ctx.Err()
		}
		locks.mu.Unlock()
		// The release path granted this waiter at the same instant its
		// context was canceled. Pass the grant on so the queue cannot stall.
		locks.releaseMemoryLease(key)
		return nil, nil, ctx.Err()
	}
}

func (locks *MemoryLocks) memoryLease(ctx context.Context, key string) (context.Context, func()) {
	var once sync.Once
	leaseCtx := adapter.WithOperationLeaseID(ctx, model.NewResourceID())
	return leaseCtx, func() {
		once.Do(func() { locks.releaseMemoryLease(key) })
	}
}

func (locks *MemoryLocks) releaseMemoryLease(key string) {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	queue := locks.waiters[key]
	if len(queue) == 0 {
		delete(locks.active, key)
		delete(locks.waiters, key)
		return
	}
	next := queue[0]
	queue = queue[1:]
	if len(queue) == 0 {
		delete(locks.waiters, key)
	} else {
		locks.waiters[key] = queue
	}
	// The active marker remains set while ownership is handed to the oldest
	// waiter. This prevents a new recovery loop from jumping the queue.
	close(next.ready)
}

type AllowAllApproval struct{}

func (AllowAllApproval) Consume(_ context.Context, operation model.OperationRecord, _ string) (model.ResourceID, model.OperationRecord, error) {
	operation.Stage = model.StageApprove
	return model.NewResourceID(), operation, nil
}

func (AllowAllApproval) Validate(context.Context, model.Operation, string) error {
	return nil
}
