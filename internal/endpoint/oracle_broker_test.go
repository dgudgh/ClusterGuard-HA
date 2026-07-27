package endpoint

import (
	"context"
	"strings"
	"testing"
	"time"

	oracleadapter "clusterguard.io/ha/adapters/oracle"
	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type oracleAgentTransportStub struct {
	requests            []agent.Request
	transportLagSeconds *int64
	applyLagSeconds     *int64
}

func (transport *oracleAgentTransportStub) Send(_ context.Context, instance model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	transport.requests = append(transport.requests, request)
	role := "PRIMARY"
	if instance.Role == model.RoleStandby {
		role = "PHYSICAL STANDBY"
	}
	ready := true
	zero := int64(0)
	transportLag := &zero
	if transport.transportLagSeconds != nil {
		transportLag = transport.transportLagSeconds
	}
	applyLag := &zero
	if transport.applyLagSeconds != nil {
		applyLag = transport.applyLagSeconds
	}
	return agent.Response{
		Status: agent.StatusOK, ClusterID: instance.ClusterID, InstanceID: instance.ResourceID,
		OracleDatabase: instance.EngineIdentity["db_unique_name"], OracleRole: role,
		OracleConfigurationStatus: "SUCCESS", OracleReadyForSwitchover: &ready,
		OracleTransportLagSeconds: transportLag, OracleApplyLagSeconds: applyLag,
	}, nil
}

type incompleteOracleAgentTransport struct{}

func (incompleteOracleAgentTransport) Send(_ context.Context, instance model.DatabaseInstance, _ agent.Request) (agent.Response, error) {
	return agent.Response{Status: agent.StatusOK, ClusterID: instance.ClusterID, InstanceID: instance.ResourceID}, nil
}

func oracleResolvedOperation() adapter.ResolvedOperation {
	clusterID := model.NewResourceID()
	operationID := model.NewResourceID()
	primary := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 3},
		ClusterID:    clusterID, Engine: model.EngineOracle,
		EngineIdentity: model.EngineIdentity{"dbid": "12345", "db_unique_name": "mesdb"},
		Hostname:       "mesdb", IPAddress: "192.0.2.10", Port: 1521, Role: model.RolePrimary,
	}
	target := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 4},
		ClusterID:    clusterID, Engine: model.EngineOracle,
		EngineIdentity: model.EngineIdentity{"dbid": "12345", "db_unique_name": "reportdb"},
		Hostname:       "reportdb", IPAddress: "192.0.2.11", Port: 1521, Role: model.RoleStandby,
	}
	return adapter.ResolvedOperation{
		OperationID: operationID,
		Cluster: model.DatabaseCluster{
			ResourceMeta: model.ResourceMeta{ResourceID: clusterID, MetadataRevision: 2},
			Engine:       model.EngineOracle,
		},
		Snapshot: model.TopologySnapshot{ClusterID: clusterID, Instances: []model.DatabaseInstance{primary, target}},
		Primary:  primary, Target: target,
		PlanDigest: "sha256:" + strings.Repeat("a", 64),
	}
}

func TestOracleBrokerControllerSignsStatusAndSwitchoverForFrozenTopology(t *testing.T) {
	transport := &oracleAgentTransportStub{}
	now := time.Date(2026, time.July, 24, 9, 0, 0, 0, time.UTC)
	controller := NewOracleBrokerController(transport, "agent-secret", func() time.Time { return now })
	resolved := oracleResolvedOperation()

	checks := controller.Precheck(context.Background(), resolved)
	for _, check := range checks {
		if check.Status != model.CheckPass {
			t.Fatalf("precheck=%+v", checks)
		}
	}
	leaseID := model.NewResourceID()
	if err := controller.Switchover(context.Background(), resolved, leaseID); err != nil {
		t.Fatalf("switchover: %v", err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("agent request count=%d", len(transport.requests))
	}
	request := transport.requests[1]
	if request.Command != agent.CommandOracleBrokerSwitchover || request.OracleTarget != "reportdb" ||
		request.LeaseID != leaseID || request.PlanDigest != resolved.PlanDigest || request.Signature == "" {
		t.Fatalf("switchover request=%+v", request)
	}
	if request.ExpiresAt.After(now.Add(31*time.Second)) || request.Engine != model.EngineOracle {
		t.Fatalf("switchover request expiry/engine=%+v", request)
	}
}

func TestOracleBrokerControllerWarnsForKnownTransientLagWhenBrokerIsReady(t *testing.T) {
	one := int64(1)
	transport := &oracleAgentTransportStub{
		transportLagSeconds: &one,
		applyLagSeconds:     &one,
	}
	controller := NewOracleBrokerController(transport, "agent-secret", time.Now)
	checks := controller.Precheck(context.Background(), oracleResolvedOperation())

	foundLagWarning := false
	for _, check := range checks {
		if check.Name == "oracle_target_zero_lag" {
			foundLagWarning = check.Status == model.CheckWarn
		}
		if check.Status == model.CheckFail {
			t.Fatalf("known transient lag blocked Broker-ready switchover: %+v", checks)
		}
	}
	if !foundLagWarning {
		t.Fatalf("transient lag warning missing: %+v", checks)
	}
}

func TestOracleBrokerControllerRejectsMissingLeaseAndMutableTopology(t *testing.T) {
	controller := NewOracleBrokerController(&oracleAgentTransportStub{}, "agent-secret", time.Now)
	resolved := oracleResolvedOperation()
	if err := controller.Switchover(context.Background(), resolved, ""); err == nil || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("missing lease error=%v", err)
	}
	resolved.Target.Port++
	if _, err := controller.Status(context.Background(), resolved, resolved.Target, "mesdb"); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("mutable topology error=%v", err)
	}
}

func TestOracleBrokerControllerStatusRequiresCompleteAgentEvidence(t *testing.T) {
	controller := NewOracleBrokerController(incompleteOracleAgentTransport{}, "agent-secret", time.Now)
	resolved := oracleResolvedOperation()
	if _, err := controller.Status(context.Background(), resolved, resolved.Primary, "reportdb"); err == nil {
		t.Fatal("incomplete Oracle agent evidence was accepted")
	}
}

func TestOracleBrokerDiscoveryProviderSignsEndpointProbeAndBuildsIdentity(t *testing.T) {
	clusterID := model.NewResourceID()
	instanceID := model.NewResourceID()
	zero := int64(0)
	transport := &oracleDiscoveryTransportStub{response: agent.Response{
		Status: agent.StatusOK, ClusterID: clusterID, InstanceID: instanceID,
		OracleDBID: "1234567890", OracleDatabase: "reportdb", OracleInstanceName: "mesdb",
		OracleRole: "PHYSICAL STANDBY", OracleDatabaseStatus: "SUCCESS",
		OracleConfigurationStatus: "SUCCESS", OracleBrokerEnabled: true,
		OracleTransportLagSeconds: &zero, OracleApplyLagSeconds: &zero,
	}}
	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	provider := NewOracleBrokerDiscoveryProvider(transport, "agent-secret", func() time.Time { return now })
	result, err := provider.Discover(context.Background(), adapter.DiscoverRequest{
		ClusterID: clusterID,
		Endpoint:  adapter.Endpoint{Hostname: "orcl", IPAddress: "192.0.2.11", Port: 1521},
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if result.Instance.ResourceID != instanceID || result.Instance.Role != model.RoleStandby ||
		result.Instance.EngineIdentity["dbid"] != "1234567890" ||
		result.Instance.EngineIdentity["db_unique_name"] != "reportdb" ||
		result.Instance.EngineIdentity["instance_name"] != "mesdb" ||
		result.Instance.Health.State != model.HealthHealthy || !result.Instance.PromotionEligible {
		t.Fatalf("result=%+v", result)
	}
	if transport.request.Command != agent.CommandOracleBrokerDiscover ||
		transport.request.OracleTarget != "" || transport.request.Signature == "" ||
		transport.instance.IPAddress != "192.0.2.11" {
		t.Fatalf("request=%+v instance=%+v", transport.request, transport.instance)
	}
}

func TestOracleBrokerDiscoveryProviderAcceptsBrokerReadyStandbyWithTransientLag(t *testing.T) {
	clusterID := model.NewResourceID()
	instanceID := model.NewResourceID()
	one := int64(1)
	ready := true
	transport := &oracleDiscoveryTransportStub{response: agent.Response{
		Status: agent.StatusOK, ClusterID: clusterID, InstanceID: instanceID,
		OracleDBID: "1234567890", OracleDatabase: "reportdb", OracleInstanceName: "mesdb",
		OracleRole: "PHYSICAL STANDBY", OracleDatabaseStatus: "SUCCESS",
		OracleConfigurationStatus: "SUCCESS", OracleBrokerEnabled: true,
		OracleReadyForSwitchover:  &ready,
		OracleTransportLagSeconds: &one, OracleApplyLagSeconds: &one,
	}}
	provider := NewOracleBrokerDiscoveryProvider(transport, "agent-secret", time.Now)
	result, err := provider.Discover(context.Background(), adapter.DiscoverRequest{
		ClusterID: clusterID,
		Endpoint:  adapter.Endpoint{Hostname: "orcl", IPAddress: "192.0.2.11", Port: 1521},
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !result.Instance.PromotionEligible || result.Instance.Replication.LagSeconds == nil ||
		*result.Instance.Replication.LagSeconds != 1 {
		t.Fatalf("Broker-ready standby was not retained as a candidate: %+v", result.Instance)
	}
}

type oracleDiscoveryTransportStub struct {
	instance model.DatabaseInstance
	request  agent.Request
	response agent.Response
}

func (transport *oracleDiscoveryTransportStub) Send(_ context.Context, instance model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	transport.instance = instance
	transport.request = request
	return transport.response, nil
}

var _ oracleadapter.BrokerController = (*OracleBrokerController)(nil)
