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
	primaryCount := 0
	postCommitResolution := request.Plan != nil
	if postCommitResolution {
		if !model.ValidResourceID(request.Plan.SourceID) || !model.ValidResourceID(request.Plan.TargetID) || request.Plan.TargetID != request.TargetID {
			return adapter.OperationRequest{}, fmt.Errorf("operation plan resources are invalid for verification")
		}
		if request.Plan.SourceID == request.Plan.TargetID {
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
	}
	if postCommitResolution {
		if primary.ResourceID == "" {
			return adapter.OperationRequest{}, fmt.Errorf("operation plan source is not in the selected cluster inventory")
		}
	} else {
		if primaryCount != 1 {
			return adapter.OperationRequest{}, fmt.Errorf("exactly one current primary is required")
		}
	}
	if target.ResourceID == "" {
		return adapter.OperationRequest{}, fmt.Errorf("target is not in the selected cluster inventory")
	}
	if !postCommitResolution && target.ResourceID == primary.ResourceID {
		return adapter.OperationRequest{}, fmt.Errorf("target must differ from the current primary")
	}
	credentials, err := resolver.Credentials.Credentials(ctx, cluster)
	if err != nil {
		return adapter.OperationRequest{}, fmt.Errorf("resolve operation credentials: %w", err)
	}
	if strings.TrimSpace(credentials.Administrative.Username) == "" {
		return adapter.OperationRequest{}, fmt.Errorf("resolved operation credentials have no username")
	}
	if strings.TrimSpace(credentials.Replication.Username) == "" {
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
