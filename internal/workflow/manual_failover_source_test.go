package workflow

import (
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"context"
	"testing"
	"time"
)

type manualOwnerReader struct {
	operationReaderStub
	resources []model.HAEndpoint
	endpoints []model.Endpoint
}

func (r manualOwnerReader) HAEndpoints(model.ResourceID) []model.HAEndpoint { return r.resources }
func (r manualOwnerReader) Endpoints(model.ResourceID) []model.Endpoint     { return r.endpoints }
func TestManualFailoverUsesOnlyCanonicalFailedOwner(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		for _, scenario := range []string{"failed-owner", "credential-error", "stale-probe", "conflicting-owner", "duplicate-active", "inactive", "same-target", "other-primary", "explicit-source", "old-snapshot", "duplicate-probe", "replica-endpoint", "replica-source", "old-source-health"} {
			t.Run(string(engine)+scenario, func(t *testing.T) {
				base, req := resolvedOperationFixture()
				base.cluster.Engine = engine
				req.Operation.Engine = engine
				req.Operation.Kind = model.OperationFailover
				req.Operation.RequestedBy = "operator"
				for i := range base.snapshot.Instances {
					base.snapshot.Instances[i].Engine = engine
				}
				source := &base.snapshot.Instances[0]
				source.Role = model.RoleUnknown
				source.Health = model.Health{State: model.HealthUnhealthy, ObservedAt: base.snapshot.ObservedAt}
				base.snapshot.Probes = []model.ProbeStatus{{InstanceID: source.ResourceID, Outcome: model.ProbeOutcomeDatabaseUnavailable, Health: source.Health}}
				eid := model.NewResourceID()
				r := manualOwnerReader{operationReaderStub: base, resources: []model.HAEndpoint{{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: base.cluster.ResourceID, EndpointID: eid, OwnerID: source.ResourceID, Kind: model.EndpointService, Provider: model.EndpointProviderGateway}}, endpoints: []model.Endpoint{{ResourceMeta: model.ResourceMeta{ResourceID: eid}, ClusterID: base.cluster.ResourceID, InstanceID: source.ResourceID, Kind: model.EndpointService, Active: true}}}
				switch scenario {
				case "credential-error":
					r.snapshot.Probes[0].Outcome = model.ProbeOutcomeCredentialsUnavailable
				case "stale-probe":
					r.snapshot.Probes[0].Health.ObservedAt = r.snapshot.ObservedAt.Add(-time.Second)
				case "conflicting-owner":
					r.endpoints[0].InstanceID = req.TargetID
				case "duplicate-active":
					r.resources = append(r.resources, r.resources[0])
				case "inactive":
					r.endpoints[0].Active = false
				case "same-target":
					req.TargetID = source.ResourceID
				case "other-primary":
					r.snapshot.Instances[1].Role = model.RolePrimary
				case "old-snapshot":
					r.snapshot.ObservedAt = r.snapshot.ObservedAt.Add(-time.Minute)
					r.snapshot.Probes[0].Health.ObservedAt = r.snapshot.ObservedAt
				case "duplicate-probe":
					r.snapshot.Probes = append(r.snapshot.Probes, r.snapshot.Probes[0])
				case "replica-source":
					r.snapshot.Instances[0].Role = model.RoleReplica
				case "old-source-health":
					r.snapshot.Instances[0].Health.ObservedAt = r.snapshot.ObservedAt.Add(-time.Second)
				case "replica-endpoint":
					r.resources[0].DesiredRole = model.RoleReplica
				case "explicit-source":
					req.SourceID = source.ResourceID
				}
				resolver := RepositoryResolver{Reader: r, Credentials: CredentialProviderFunc(func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
					return adapter.OperationCredentials{Administrative: adapter.Credentials{Username: "operator"}, Replication: adapter.Credentials{Username: "replicator"}}, nil
				})}
				got, err := resolver.Resolve(context.Background(), req)
				if scenario == "failed-owner" {
					if err != nil || got.Resolved == nil || got.Resolved.Primary.ResourceID != source.ResourceID {
						t.Fatalf("canonical failed owner: %+v %v", got.Resolved, err)
					}
				} else if err == nil {
					t.Fatalf("unsafe manual source accepted: %s", scenario)
				}
			})
		}
	}
}
