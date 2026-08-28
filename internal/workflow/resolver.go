package workflow

import (
	"context"
	"fmt"
	"strings"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type OperationResourceReader interface {
	Cluster(model.ResourceID) (model.DatabaseCluster, bool)
	TopologySnapshot(model.ResourceID) (model.TopologySnapshot, bool)
}

type CredentialProvider interface {
	Credentials(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error)
}

type CredentialProviderFunc func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error)

func (provider CredentialProviderFunc) Credentials(ctx context.Context, cluster model.DatabaseCluster) (adapter.OperationCredentials, error) {
	return provider(ctx, cluster)
}

type OperationResolver interface {
	Resolve(context.Context, adapter.OperationRequest) (adapter.OperationRequest, error)
}

type CapturedOperationResolver interface {
	ResolveCaptured(context.Context, adapter.OperationRequest, model.TopologySnapshot) (adapter.OperationRequest, error)
}

type OperationResolverFunc func(context.Context, adapter.OperationRequest) (adapter.OperationRequest, error)

func (resolver OperationResolverFunc) Resolve(ctx context.Context, request adapter.OperationRequest) (adapter.OperationRequest, error) {
	return resolver(ctx, request)
}

type RepositoryResolver struct {
	Reader      OperationResourceReader
	Credentials CredentialProvider
}

var endpointParameterNames = map[string]struct{}{
	"host": {}, "hostname": {}, "ip": {}, "ip_address": {}, "port": {},
	"primary_host": {}, "primary_hostname": {}, "primary_ip": {}, "primary_port": {},
	"source_host": {}, "source_hostname": {}, "source_ip": {}, "source_port": {},
	"target_host": {}, "target_hostname": {}, "target_ip": {}, "target_port": {},
}

func rejectsEndpointParameters(parameters map[string]string) bool {
	for name := range parameters {
		if _, found := endpointParameterNames[strings.ToLower(strings.TrimSpace(name))]; found {
			return true
		}
	}
	return false
}

func operationRequiresReplicationCredentials(engine model.Engine) bool {
	switch engine {
	case model.EngineOracle, model.EngineSQLServer:
		return false
	default:
		return true
	}
}

func resolverCurrentSuccessfulProbe(snapshot model.TopologySnapshot, instanceID model.ResourceID) bool {
	found := false
	for _, probe := range snapshot.Probes {
		if probe.InstanceID != instanceID {
			continue
		}
		found = true
		if !probe.DiscoveryObservedAt.Equal(snapshot.ObservedAt) || probe.Health.State != model.HealthHealthy {
			return false
		}
	}
	return found
}

func resolverCurrentFailedProbe(snapshot model.TopologySnapshot, instanceID model.ResourceID) bool {
	found := false
	for _, probe := range snapshot.Probes {
		if probe.InstanceID != instanceID {
			continue
		}
		found = true
		if !probe.DiscoveryObservedAt.IsZero() || !probe.Health.ObservedAt.Equal(snapshot.ObservedAt) {
			return false
		}
		if probe.Health.State != model.HealthUnknown && probe.Health.State != model.HealthUnhealthy {
			return false
		}
	}
	return found
}

func resolverCurrentDatabaseFailureProbe(snapshot model.TopologySnapshot, instanceID model.ResourceID) bool {
	found := false
	for _, probe := range snapshot.Probes {
		if probe.InstanceID != instanceID {
			continue
		}
		found = true
		if probe.Outcome != model.ProbeOutcomeDatabaseUnavailable ||
			!probe.DiscoveryObservedAt.IsZero() ||
			!probe.Health.ObservedAt.Equal(snapshot.ObservedAt) ||
			(probe.Health.State != model.HealthUnknown && probe.Health.State != model.HealthUnhealthy) {
			return false
		}
	}
	return found
}

func resolverPrimaryProvenWritable(instance model.DatabaseInstance) bool {
	switch instance.Engine {
	case model.EngineMySQL:
		return strings.EqualFold(strings.TrimSpace(instance.EngineMetadata["read_only"]), "false") &&
			strings.EqualFold(strings.TrimSpace(instance.EngineMetadata["super_read_only"]), "false")
	case model.EnginePostgreSQL:
		return strings.EqualFold(strings.TrimSpace(instance.EngineMetadata["in_recovery"]), "false") &&
			strings.EqualFold(strings.TrimSpace(instance.EngineMetadata["transaction_read_only"]), "false")
	default:
		return false
	}
}

func resolveFormerPrimaryRejoinPrimary(snapshot model.TopologySnapshot, target model.DatabaseInstance) (model.DatabaseInstance, bool) {
	var primary model.DatabaseInstance
	for _, instance := range snapshot.Instances {
		if instance.Role != model.RolePrimary {
			continue
		}
		if instance.ResourceID == target.ResourceID {
			if !resolverCurrentFailedProbe(snapshot, instance.ResourceID) {
				return model.DatabaseInstance{}, false
			}
			continue
		}
		if instance.Health.State != model.HealthHealthy || !resolverPrimaryProvenWritable(instance) ||
			!resolverCurrentSuccessfulProbe(snapshot, instance.ResourceID) || primary.ResourceID != "" {
			return model.DatabaseInstance{}, false
		}
		primary = instance
	}
	return primary, model.ValidResourceID(primary.ResourceID)
}

func (resolver RepositoryResolver) Resolve(ctx context.Context, request adapter.OperationRequest) (adapter.OperationRequest, error) {
	return resolver.resolve(ctx, request, nil)
}

func (resolver RepositoryResolver) ResolveCaptured(ctx context.Context, request adapter.OperationRequest, snapshot model.TopologySnapshot) (adapter.OperationRequest, error) {
	return resolver.resolve(ctx, request, &snapshot)
}

func resolveCapturedOperation(ctx context.Context, resolver OperationResolver, request adapter.OperationRequest, observation ObservationToken) (adapter.OperationRequest, error) {
	if captured, ok := resolver.(CapturedOperationResolver); ok && observation.Snapshot.ClusterID != "" {
		return captured.ResolveCaptured(ctx, request, observation.Snapshot)
	}
	return resolver.Resolve(ctx, request)
}

func (resolver RepositoryResolver) resolve(ctx context.Context, request adapter.OperationRequest, captured *model.TopologySnapshot) (adapter.OperationRequest, error) {
	if err := ctx.Err(); err != nil {
		return adapter.OperationRequest{}, err
	}
	if resolver.Reader == nil || resolver.Credentials == nil {
		return adapter.OperationRequest{}, fmt.Errorf("operation resolver is not configured")
	}
	if !model.ValidResourceID(request.Operation.ClusterID) {
		return adapter.OperationRequest{}, fmt.Errorf("operation cluster ID is invalid")
	}
	if !model.ValidResourceID(request.TargetID) {
		return adapter.OperationRequest{}, fmt.Errorf("operation target ID is invalid")
	}
	if rejectsEndpointParameters(request.Parameters) {
		return adapter.OperationRequest{}, fmt.Errorf("endpoint parameters are not accepted; select inventory resources by UUID")
	}
	if request.SourceID != "" && (request.Operation.Kind != model.OperationFailover || request.Operation.RequestedBy != AutomaticRecoveryActor) {
		return adapter.OperationRequest{}, fmt.Errorf("explicit operation source is reserved for automatic failover")
	}
	cluster, found := resolver.Reader.Cluster(request.Operation.ClusterID)
	if !found {
		return adapter.OperationRequest{}, fmt.Errorf("selected cluster does not exist")
	}
	if cluster.Engine != request.Operation.Engine {
		return adapter.OperationRequest{}, fmt.Errorf("operation engine does not match the selected cluster")
	}
	snapshot := model.TopologySnapshot{}
	snapshotFound := false
	if captured != nil {
		snapshot = *captured
		snapshotFound = true
	} else {
		snapshot, snapshotFound = resolver.Reader.TopologySnapshot(cluster.ResourceID)
	}
	if !snapshotFound || snapshot.ObservedAt.IsZero() {
		return adapter.OperationRequest{}, fmt.Errorf("a current topology observation is required")
	}
	if snapshot.ClusterID != cluster.ResourceID {
		return adapter.OperationRequest{}, fmt.Errorf("topology observation belongs to another cluster")
	}

	var primary model.DatabaseInstance
	var target model.DatabaseInstance
	var explicitSource model.DatabaseInstance
	primaryCount := 0
	postCommitResolution := request.Plan != nil
	if postCommitResolution {
		if !model.ValidResourceID(request.Plan.SourceID) || !model.ValidResourceID(request.Plan.TargetID) || request.Plan.TargetID != request.TargetID {
			return adapter.OperationRequest{}, fmt.Errorf("operation plan resources are invalid for verification")
		}
		// Power shutdown is a whole-cluster operation: the target is the primary
		// being stopped, so a single-resource plan is valid.
		if request.Plan.SourceID == request.Plan.TargetID && request.Operation.Kind != model.OperationPowerShutdown {
			return adapter.OperationRequest{}, fmt.Errorf("operation plan source and target must differ")
		}
	}
	for _, instance := range snapshot.Instances {
		if instance.ClusterID != cluster.ResourceID || instance.Engine != cluster.Engine || !model.ValidResourceID(instance.ResourceID) {
			return adapter.OperationRequest{}, fmt.Errorf("topology contains an instance outside the selected cluster inventory")
		}
		if postCommitResolution && instance.ResourceID == request.Plan.SourceID {
			primary = instance
		} else if !postCommitResolution && instance.Role == model.RolePrimary {
			primary = instance
			primaryCount++
		}
		if instance.ResourceID == request.TargetID {
			target = instance
		}
		if instance.ResourceID == request.SourceID {
			explicitSource = instance
		}
	}
	if postCommitResolution {
		if primary.ResourceID == "" {
			return adapter.OperationRequest{}, fmt.Errorf("operation plan source is not in the selected cluster inventory")
		}
	} else {
		if request.Operation.Kind == model.OperationFailover && request.SourceID != "" {
			if !model.ValidResourceID(request.SourceID) || explicitSource.ResourceID == "" {
				return adapter.OperationRequest{}, fmt.Errorf("automatic failover source is not in the selected cluster inventory")
			}
			if explicitSource.Health.State != model.HealthUnknown && explicitSource.Health.State != model.HealthUnhealthy {
				return adapter.OperationRequest{}, fmt.Errorf("automatic failover source is not failed")
			}
			if !resolverCurrentDatabaseFailureProbe(snapshot, explicitSource.ResourceID) {
				return adapter.OperationRequest{}, fmt.Errorf("automatic failover source has no current database-failure evidence")
			}
			for _, instance := range snapshot.Instances {
				if instance.Role == model.RolePrimary && instance.ResourceID != explicitSource.ResourceID {
					return adapter.OperationRequest{}, fmt.Errorf("automatic failover is blocked because another primary is already observed")
				}
			}
			primary = explicitSource
		} else if request.Operation.Kind == model.OperationFormerPrimaryRejoin {
			var resolved bool
			primary, resolved = resolveFormerPrimaryRejoinPrimary(snapshot, target)
			if !resolved {
				return adapter.OperationRequest{}, fmt.Errorf("exactly one current primary is required")
			}
		} else if primaryCount != 1 {
			return adapter.OperationRequest{}, fmt.Errorf("exactly one current primary is required")
		}
	}
	if target.ResourceID == "" {
		return adapter.OperationRequest{}, fmt.Errorf("target is not in the selected cluster inventory")
	}
	if !postCommitResolution && target.ResourceID == primary.ResourceID && request.Operation.Kind != model.OperationPowerShutdown {
		return adapter.OperationRequest{}, fmt.Errorf("target must differ from the current primary")
	}
	credentials, err := resolver.Credentials.Credentials(ctx, cluster)
	if err != nil {
		return adapter.OperationRequest{}, fmt.Errorf("resolve operation credentials: %w", err)
	}
	if strings.TrimSpace(credentials.Administrative.Username) == "" {
		return adapter.OperationRequest{}, fmt.Errorf("resolved operation credentials have no username")
	}
	if operationRequiresReplicationCredentials(cluster.Engine) && strings.TrimSpace(credentials.Replication.Username) == "" {
		return adapter.OperationRequest{}, fmt.Errorf("resolved replication credentials have no username")
	}

	request.Credentials = credentials.Administrative
	request.ReplicationCredentials = credentials.Replication
	request.Resolved = &adapter.ResolvedOperation{
		OperationID:            request.Operation.ResourceID,
		Cluster:                cluster,
		Snapshot:               snapshot,
		Primary:                primary,
		Target:                 target,
		Credentials:            credentials.Administrative,
		ReplicationCredentials: credentials.Replication,
	}
	if request.Plan != nil {
		request.Resolved.PlanDigest = request.Plan.Digest
	}
	return request, nil
}
