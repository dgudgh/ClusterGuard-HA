package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type operationReaderStub struct {
	cluster  model.DatabaseCluster
	snapshot model.TopologySnapshot
}

func (reader operationReaderStub) Cluster(resourceID model.ResourceID) (model.DatabaseCluster, bool) {
	return reader.cluster, reader.cluster.ResourceID == resourceID
}

func (reader operationReaderStub) TopologySnapshot(resourceID model.ResourceID) (model.TopologySnapshot, bool) {
	return reader.snapshot, reader.snapshot.ClusterID == resourceID
}

func resolvedOperationFixture() (operationReaderStub, adapter.OperationRequest) {
	clusterID := model.NewResourceID()
	primaryID := model.NewResourceID()
	targetID := model.NewResourceID()
	observedAt := time.Now().UTC()
	cluster := model.DatabaseCluster{
		ResourceMeta: model.ResourceMeta{ResourceID: clusterID, MetadataRevision: 3},
		Engine:       model.EngineMySQL,
		DisplayName:  "mysql-production",
	}
	primary := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: primaryID, MetadataRevision: 5},
		ClusterID:    clusterID,
		Engine:       model.EngineMySQL,
		Role:         model.RolePrimary,
		Hostname:     "db-primary",
		Port:         3306,
	}
	target := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: targetID, MetadataRevision: 7},
		ClusterID:    clusterID,
		Engine:       model.EngineMySQL,
		Role:         model.RoleReplica,
		Hostname:     "db-replica",
		Port:         3306,
	}
	reader := operationReaderStub{
		cluster: cluster,
		snapshot: model.TopologySnapshot{
			ClusterID:  clusterID,
			Instances:  []model.DatabaseInstance{primary, target},
			ObservedAt: observedAt,
		},
	}
	request := adapter.OperationRequest{
		Operation: model.Operation{ClusterID: clusterID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover},
		TargetID:  targetID,
	}
	return reader, request
}

func TestRepositoryResolverResolvesUUIDScopedContext(t *testing.T) {
	reader, request := resolvedOperationFixture()
	resolver := RepositoryResolver{
		Reader: reader,
		Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
			return adapter.OperationCredentials{
				Administrative: adapter.Credentials{Username: "clusterguard", Password: "secret"},
				Replication:    adapter.Credentials{Username: "replicator", Password: "replication-secret"},
			}, nil
		}),
	}
	resolved, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("resolve operation: %v", err)
	}
	if resolved.Resolved == nil || resolved.Resolved.Primary.Role != model.RolePrimary || resolved.Resolved.Target.ResourceID != request.TargetID {
		t.Fatalf("unexpected resolved context: %+v", resolved.Resolved)
	}
	if resolved.Resolved.Credentials.Username != "clusterguard" || resolved.Resolved.Credentials.Password != "secret" {
		t.Fatalf("server-side credentials were not injected")
	}
	if resolved.Resolved.ReplicationCredentials.Username != "replicator" || resolved.Resolved.ReplicationCredentials.Password != "replication-secret" {
		t.Fatalf("server-side replication credentials were not injected")
	}
}

func TestRepositoryResolverResolvesAutomaticFailoverSourceAfterRuntimeRoleIsCleared(t *testing.T) {
	reader, request := resolvedOperationFixture()
	sourceID := reader.snapshot.Instances[0].ResourceID
	observedAt := reader.snapshot.ObservedAt
	reader.snapshot.Instances[0].Role = model.RoleUnknown
	reader.snapshot.Instances[0].Health = model.Health{State: model.HealthUnhealthy, ObservedAt: observedAt}
	reader.snapshot.Probes = []model.ProbeStatus{
		{
			InstanceID: sourceID,
			Outcome:    model.ProbeOutcomeDatabaseUnavailable,
			Health:     model.Health{State: model.HealthUnhealthy, ObservedAt: observedAt},
		},
		{
			InstanceID:          reader.snapshot.Instances[1].ResourceID,
			Outcome:             model.ProbeOutcomeReachable,
			Health:              model.Health{State: model.HealthHealthy, ObservedAt: observedAt},
			DiscoveryObservedAt: observedAt,
		},
	}
	request.Operation.Kind = model.OperationFailover
	request.Operation.RequestedBy = AutomaticRecoveryActor
	request.SourceID = sourceID
	resolver := RepositoryResolver{
		Reader: reader,
		Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
			return adapter.OperationCredentials{
				Administrative: adapter.Credentials{Username: "clusterguard"},
				Replication:    adapter.Credentials{Username: "replicator"},
			}, nil
		}),
	}

	resolved, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("resolve automatic failover source: %v", err)
	}
	if resolved.Resolved == nil || resolved.Resolved.Primary.ResourceID != sourceID {
		t.Fatalf("resolved automatic failover source=%+v, want %s", resolved.Resolved, sourceID)
	}
}

func TestRepositoryResolverRejectsAutomaticFailoverSourceWithoutCurrentDatabaseFailure(t *testing.T) {
	reader, request := resolvedOperationFixture()
	sourceID := reader.snapshot.Instances[0].ResourceID
	reader.snapshot.Instances[0].Role = model.RoleUnknown
	reader.snapshot.Instances[0].Health = model.Health{State: model.HealthUnknown, ObservedAt: reader.snapshot.ObservedAt}
	reader.snapshot.Probes = []model.ProbeStatus{{
		InstanceID: sourceID,
		Outcome:    model.ProbeOutcomeCredentialsUnavailable,
		Health:     model.Health{State: model.HealthUnknown, ObservedAt: reader.snapshot.ObservedAt},
	}}
	request.Operation.Kind = model.OperationFailover
	request.Operation.RequestedBy = AutomaticRecoveryActor
	request.SourceID = sourceID
	resolver := RepositoryResolver{
		Reader: reader,
		Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
			return adapter.OperationCredentials{
				Administrative: adapter.Credentials{Username: "clusterguard"},
				Replication:    adapter.Credentials{Username: "replicator"},
			}, nil
		}),
	}

	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "current database-failure evidence") {
		t.Fatalf("automatic failover without database-failure evidence error=%v", err)
	}
}

func TestRepositoryResolverAllowsAdministrativeOnlyCredentialsForOracle(t *testing.T) {
	reader, request := resolvedOperationFixture()
	reader.cluster.Engine = model.EngineOracle
	request.Operation.Engine = model.EngineOracle
	for index := range reader.snapshot.Instances {
		reader.snapshot.Instances[index].Engine = model.EngineOracle
	}

	resolver := RepositoryResolver{
		Reader: reader,
		Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
			return adapter.OperationCredentials{
				Administrative: adapter.Credentials{Username: "clusterguard_dg", Password: "secret"},
			}, nil
		}),
	}
	resolved, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("resolve Oracle operation: %v", err)
	}
	if resolved.Resolved == nil || resolved.Resolved.Credentials.Username != "clusterguard_dg" {
		t.Fatalf("Oracle administrative credentials were not injected: %+v", resolved.Resolved)
	}
	if resolved.Resolved.ReplicationCredentials.Username != "" {
		t.Fatalf("Oracle operation unexpectedly required replication credentials: %+v", resolved.Resolved.ReplicationCredentials)
	}
}

func TestRepositoryResolverStillRequiresReplicationCredentialsForMySQL(t *testing.T) {
	reader, request := resolvedOperationFixture()
	resolver := RepositoryResolver{
		Reader: reader,
		Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
			return adapter.OperationCredentials{
				Administrative: adapter.Credentials{Username: "clusterguard", Password: "secret"},
			}, nil
		}),
	}
	_, err := resolver.Resolve(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "replication credentials") {
		t.Fatalf("MySQL missing replication credential error=%v", err)
	}
}

func TestRepositoryResolverResolvesFormerPrimaryRejoinWithFailedHistoricalPrimary(t *testing.T) {
	reader, request := resolvedOperationFixture()
	formerPrimaryID := reader.snapshot.Instances[0].ResourceID
	currentPrimaryID := reader.snapshot.Instances[1].ResourceID
	observedAt := reader.snapshot.ObservedAt

	reader.cluster.Engine = model.EnginePostgreSQL
	reader.snapshot.Instances[0].Engine = model.EnginePostgreSQL
	reader.snapshot.Instances[0].Health = model.Health{
		State: model.HealthUnknown, ObservedAt: observedAt,
	}
	reader.snapshot.Instances[1].Engine = model.EnginePostgreSQL
	reader.snapshot.Instances[1].Role = model.RolePrimary
	reader.snapshot.Instances[1].Health = model.Health{
		State: model.HealthHealthy, ObservedAt: observedAt,
	}
	reader.snapshot.Instances[1].EngineMetadata = map[string]string{
		"in_recovery": "false", "transaction_read_only": "false",
	}
	reader.snapshot.Probes = []model.ProbeStatus{
		{
			InstanceID: formerPrimaryID,
			Health:     model.Health{State: model.HealthUnknown, ObservedAt: observedAt},
		},
		{
			InstanceID: currentPrimaryID, DiscoveryObservedAt: observedAt,
			Health: model.Health{State: model.HealthHealthy, ObservedAt: observedAt},
		},
	}
	request.Operation.Engine = model.EnginePostgreSQL
	request.Operation.Kind = model.OperationFormerPrimaryRejoin
	request.TargetID = formerPrimaryID

	resolver := RepositoryResolver{
		Reader: reader,
		Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
			return adapter.OperationCredentials{
				Administrative: adapter.Credentials{Username: "clusterguard"},
				Replication:    adapter.Credentials{Username: "replicator"},
			}, nil
		}),
	}
	resolved, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("resolve former-primary rejoin: %v", err)
	}
	if resolved.Resolved.Primary.ResourceID != currentPrimaryID || resolved.Resolved.Target.ResourceID != formerPrimaryID {
		t.Fatalf("resolved rejoin resources=%+v", resolved.Resolved)
	}
}

func TestRepositoryResolverRejectsAmbiguousFormerPrimaryRejoinEvidence(t *testing.T) {
	reader, request := resolvedOperationFixture()
	formerPrimaryID := reader.snapshot.Instances[0].ResourceID
	currentPrimaryID := reader.snapshot.Instances[1].ResourceID
	observedAt := reader.snapshot.ObservedAt

	reader.cluster.Engine = model.EnginePostgreSQL
	for index := range reader.snapshot.Instances {
		reader.snapshot.Instances[index].Engine = model.EnginePostgreSQL
		reader.snapshot.Instances[index].Role = model.RolePrimary
		reader.snapshot.Instances[index].Health = model.Health{State: model.HealthHealthy, ObservedAt: observedAt}
		reader.snapshot.Instances[index].EngineMetadata = map[string]string{
			"in_recovery": "false", "transaction_read_only": "false",
		}
	}
	reader.snapshot.Probes = []model.ProbeStatus{
		{InstanceID: formerPrimaryID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy, ObservedAt: observedAt}},
		{InstanceID: currentPrimaryID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy, ObservedAt: observedAt}},
	}
	request.Operation.Engine = model.EnginePostgreSQL
	request.Operation.Kind = model.OperationFormerPrimaryRejoin
	request.TargetID = formerPrimaryID

	resolver := RepositoryResolver{
		Reader: reader,
		Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
			return adapter.OperationCredentials{
				Administrative: adapter.Credentials{Username: "clusterguard"},
				Replication:    adapter.Credentials{Username: "replicator"},
			}, nil
		}),
	}
	if _, err := resolver.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "exactly one current primary") {
		t.Fatalf("ambiguous rejoin evidence error=%v", err)
	}
}

func TestRepositoryResolverUsesCapturedSnapshotInsteadOfLaterRepositoryState(t *testing.T) {
	reader, request := resolvedOperationFixture()
	captured := reader.snapshot
	reader.snapshot.Instances = append([]model.DatabaseInstance{}, reader.snapshot.Instances...)
	reader.snapshot.Instances[0].Role = model.RoleReplica
	resolver := RepositoryResolver{
		Reader: reader,
		Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
			return adapter.OperationCredentials{
				Administrative: adapter.Credentials{Username: "clusterguard"},
				Replication:    adapter.Credentials{Username: "replicator"},
			}, nil
		}),
	}
	resolved, err := resolver.ResolveCaptured(context.Background(), request, captured)
	if err != nil {
		t.Fatalf("resolve captured operation: %v", err)
	}
	if resolved.Resolved == nil || resolved.Resolved.Primary.ResourceID != captured.Instances[0].ResourceID || resolved.Resolved.Target.ResourceID != request.TargetID {
		t.Fatalf("resolver did not use the captured topology: %+v", resolved.Resolved)
	}
}

func TestRepositoryResolverUsesImmutablePlanResourcesForPostCommitVerification(t *testing.T) {
	for _, state := range []string{"target promoted", "no current primary"} {
		t.Run(state, func(t *testing.T) {
			reader, request := resolvedOperationFixture()
			sourceID := reader.snapshot.Instances[0].ResourceID
			targetID := reader.snapshot.Instances[1].ResourceID
			reader.snapshot.Instances[0].Role = model.RoleReplica
			if state == "target promoted" {
				reader.snapshot.Instances[1].Role = model.RolePrimary
			} else {
				reader.snapshot.Instances[1].Role = model.RoleReplica
			}
			request.Operation.Status = model.OperationIndeterminate
			request.Plan = &model.OperationPlan{SourceID: sourceID, TargetID: targetID}
			resolver := RepositoryResolver{
				Reader: reader,
				Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
					return adapter.OperationCredentials{Administrative: adapter.Credentials{Username: "clusterguard"}, Replication: adapter.Credentials{Username: "replicator"}}, nil
				}),
			}
			resolved, err := resolver.Resolve(context.Background(), request)
			if err != nil {
				t.Fatalf("resolve post-commit resources: %v", err)
			}
			if resolved.Resolved.Primary.ResourceID != sourceID || resolved.Resolved.Target.ResourceID != targetID {
				t.Fatalf("resolved context=%+v", resolved.Resolved)
			}
		})
	}
}

func TestRepositoryResolverRejectsAmbiguousOrExternalResources(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*operationReaderStub, *adapter.OperationRequest)
		want   string
	}{
		{
			name: "target outside inventory",
			mutate: func(_ *operationReaderStub, request *adapter.OperationRequest) {
				request.TargetID = model.NewResourceID()
			},
			want: "target is not in the selected cluster inventory",
		},
		{
			name: "no primary",
			mutate: func(reader *operationReaderStub, _ *adapter.OperationRequest) {
				reader.snapshot.Instances[0].Role = model.RoleReplica
			},
			want: "exactly one current primary",
		},
		{
			name: "duplicate primary",
			mutate: func(reader *operationReaderStub, _ *adapter.OperationRequest) {
				reader.snapshot.Instances[1].Role = model.RolePrimary
			},
			want: "exactly one current primary",
		},
		{
			name: "missing observation",
			mutate: func(reader *operationReaderStub, _ *adapter.OperationRequest) {
				reader.snapshot.ObservedAt = time.Time{}
			},
			want: "current topology observation",
		},
		{
			name: "caller supplied endpoint",
			mutate: func(_ *operationReaderStub, request *adapter.OperationRequest) {
				request.Parameters = map[string]string{"target_host": "outside.example", "target_port": "3306"}
			},
			want: "endpoint parameters are not accepted",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader, request := resolvedOperationFixture()
			test.mutate(&reader, &request)
			resolver := RepositoryResolver{Reader: reader, Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
				return adapter.OperationCredentials{Administrative: adapter.Credentials{Username: "clusterguard"}, Replication: adapter.Credentials{Username: "replicator"}}, nil
			})}
			_, err := resolver.Resolve(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("resolve error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestRepositoryResolverFailsClosedWhenCredentialsCannotResolve(t *testing.T) {
	reader, request := resolvedOperationFixture()
	credentialFailure := errors.New("secret backend unavailable")
	resolver := RepositoryResolver{Reader: reader, Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
		return adapter.OperationCredentials{}, credentialFailure
	})}
	_, err := resolver.Resolve(context.Background(), request)
	if !errors.Is(err, credentialFailure) {
		t.Fatalf("credential failure was not preserved: %v", err)
	}
}
