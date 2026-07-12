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
	Credentials(context.Context, model.DatabaseCluster) (adapter.Credentials, error)
}

type CredentialProviderFunc func(context.Context, model.DatabaseCluster) (adapter.Credentials, error)

func (provider CredentialProviderFunc) Credentials(ctx context.Context, cluster model.DatabaseCluster) (adapter.Credentials, error) {
	return provider(ctx, cluster)
}

type OperationResolver interface {
	Resolve(context.Context, adapter.OperationRequest) (adapter.OperationRequest, error)
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
	snapshot, found := resolver.Reader.TopologySnapshot(cluster.ResourceID)
	if !found || snapshot.ObservedAt.IsZero() {
		return adapter.OperationRequest{}, fmt.Errorf("a current topology observation is required")
	}
	if snapshot.ClusterID != cluster.ResourceID {
		return adapter.OperationRequest{}, fmt.Errorf("topology observation belongs to another cluster")
	}

	var primary model.DatabaseInstance
	var target model.DatabaseInstance
	primaryCount := 0
	for _, instance := range snapshot.Instances {
		if instance.ClusterID != cluster.ResourceID || instance.Engine != cluster.Engine || !model.ValidResourceID(instance.ResourceID) {
			return adapter.OperationRequest{}, fmt.Errorf("topology contains an instance outside the selected cluster inventory")
		}
		if instance.Role == model.RolePrimary {
			primary = instance
			primaryCount++
		}
		if instance.ResourceID == request.TargetID {
			target = instance
		}
	}
	if primaryCount != 1 {
		return adapter.OperationRequest{}, fmt.Errorf("exactly one current primary is required")
	}
	if target.ResourceID == "" {
		return adapter.OperationRequest{}, fmt.Errorf("target is not in the selected cluster inventory")
	}
	if target.ResourceID == primary.ResourceID {
		return adapter.OperationRequest{}, fmt.Errorf("target must differ from the current primary")
	}
	credentials, err := resolver.Credentials.Credentials(ctx, cluster)
	if err != nil {
		return adapter.OperationRequest{}, fmt.Errorf("resolve operation credentials: %w", err)
	}
	if strings.TrimSpace(credentials.Username) == "" {
		return adapter.OperationRequest{}, fmt.Errorf("resolved operation credentials have no username")
	}

	request.Credentials = credentials
	request.Resolved = &adapter.ResolvedOperation{
		Cluster:     cluster,
		Snapshot:    snapshot,
		Primary:     primary,
		Target:      target,
		Credentials: credentials,
	}
	return request, nil
}
