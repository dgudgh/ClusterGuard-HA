package endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type postgresqlAgentTransportStub struct {
	requests  []agent.Request
	instances []model.ResourceID
	responses map[model.ResourceID]agent.Response
	err       error
}

func (transport *postgresqlAgentTransportStub) Send(_ context.Context, instance model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	transport.requests = append(transport.requests, request)
	transport.instances = append(transport.instances, instance.ResourceID)
	if transport.err != nil {
		return agent.Response{}, transport.err
	}
	response, found := transport.responses[instance.ResourceID]
	if !found {
		return agent.Response{Status: agent.StatusOK, ClusterID: instance.ClusterID, InstanceID: instance.ResourceID}, nil
	}
	return response, nil
}

func postgresqlNodeResolved() adapter.ResolvedOperation {
	clusterID := model.NewResourceID()
	primary := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: clusterID,
		Engine: model.EnginePostgreSQL, EngineIdentity: model.EngineIdentity{"resource_id": string(model.NewResourceID())}, Hostname: "pg-01", IPAddress: "192.0.2.10", Port: 5432, Role: model.RolePrimary,
	}
	target := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: clusterID,
		Engine: model.EnginePostgreSQL, EngineIdentity: model.EngineIdentity{"resource_id": string(model.NewResourceID())}, Hostname: "pg-02", IPAddress: "192.0.2.11", Port: 5432, Role: model.RoleStandby,
	}
	return adapter.ResolvedOperation{
		OperationID: model.NewResourceID(), PlanDigest: "sha256:" + strings.Repeat("a", 64),
		Cluster:  model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: clusterID}, Engine: model.EnginePostgreSQL},
		Snapshot: model.TopologySnapshot{ClusterID: clusterID, Instances: []model.DatabaseInstance{primary, target}},
		Primary:  primary, Target: target,
	}
}

func postgresqlStatusResponse(instance model.DatabaseInstance, running, inRecovery bool) agent.Response {
	return agent.Response{
		Status: agent.StatusOK, ClusterID: instance.ClusterID, InstanceID: instance.ResourceID,
		ServiceRunning: &running, InRecovery: &inRecovery,
	}
}

func TestPostgreSQLNodeControllerPrecheckVerifiesNativeRoles(t *testing.T) {
	resolved := postgresqlNodeResolved()
	transport := &postgresqlAgentTransportStub{responses: map[model.ResourceID]agent.Response{
		resolved.Primary.ResourceID: postgresqlStatusResponse(resolved.Primary, true, false),
		resolved.Target.ResourceID:  postgresqlStatusResponse(resolved.Target, true, true),
	}}
	controller := NewPostgreSQLNodeController(transport, "agent-secret", func() time.Time { return time.Unix(100, 0).UTC() })
	checks := controller.Precheck(context.Background(), resolved)
	if len(checks) != 2 {
		t.Fatalf("checks=%+v", checks)
	}
	for _, check := range checks {
		if check.Status != model.CheckPass {
			t.Fatalf("role precheck=%+v", checks)
		}
	}
	for _, request := range transport.requests {
		if request.Command != agent.CommandPostgreSQLStatus || request.Engine != model.EnginePostgreSQL || request.Signature == "" {
			t.Fatalf("status request=%+v", request)
		}
	}
}

func TestPostgreSQLNodeControllerMutationBindsLeaseAndSourceIdentity(t *testing.T) {
	resolved := postgresqlNodeResolved()
	transport := &postgresqlAgentTransportStub{responses: map[model.ResourceID]agent.Response{}}
	now := time.Unix(200, 0).UTC()
	controller := NewPostgreSQLNodeController(transport, "agent-secret", func() time.Time { return now })
	leaseID := model.NewResourceID()
	if err := controller.Repoint(context.Background(), resolved, resolved.Primary, resolved.Target, leaseID); err != nil {
		t.Fatalf("repoint: %v", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests=%d", len(transport.requests))
	}
	request := transport.requests[0]
	if request.Command != agent.CommandPostgreSQLRepoint || request.Engine != model.EnginePostgreSQL || request.LeaseID != leaseID ||
		request.SourceInstanceID != resolved.Target.ResourceID || request.SourceHostname != resolved.Target.Hostname ||
		request.SourceIPAddress != resolved.Target.IPAddress || request.SourcePort != resolved.Target.Port {
		t.Fatalf("repoint request=%+v", request)
	}
	expectedSignature, err := agent.SignRequest(request, "agent-secret")
	if err != nil || request.Signature != expectedSignature {
		t.Fatalf("signature=%q expected=%q err=%v", request.Signature, expectedSignature, err)
	}
	if request.ExpiresAt != now.Add(30*time.Second) {
		t.Fatalf("expires_at=%v", request.ExpiresAt)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	wantNativeSource := `"source_node_id":"` + resolved.Target.EngineIdentity["resource_id"] + `"`
	if !strings.Contains(string(encoded), wantNativeSource) {
		t.Fatalf("signed request does not bind native source identity %s: %s", wantNativeSource, encoded)
	}
}

func TestPostgreSQLNodeControllerBaseBackupUsesSignedInventorySource(t *testing.T) {
	resolved := postgresqlNodeResolved()
	transport := &postgresqlAgentTransportStub{responses: map[model.ResourceID]agent.Response{}}
	controller := NewPostgreSQLNodeController(transport, "agent-secret", func() time.Time { return time.Unix(300, 0).UTC() })
	permitID := model.NewResourceID()
	if err := controller.BaseBackup(context.Background(), resolved, resolved.Target, resolved.Primary, permitID); err != nil {
		t.Fatalf("base backup: %v", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests=%d", len(transport.requests))
	}
	request := transport.requests[0]
	if request.Command != agent.CommandPostgreSQLBaseBackup || request.LeaseID != permitID ||
		request.SourceInstanceID != resolved.Primary.ResourceID || request.SourceHostname != resolved.Primary.Hostname ||
		request.SourceIPAddress != resolved.Primary.IPAddress || request.SourcePort != resolved.Primary.Port ||
		request.PlanDigest != resolved.PlanDigest || request.Signature == "" {
		t.Fatalf("base backup request=%+v", request)
	}
}

func TestPostgreSQLNodeControllerRejectsMutationWithoutLease(t *testing.T) {
	resolved := postgresqlNodeResolved()
	transport := &postgresqlAgentTransportStub{responses: map[model.ResourceID]agent.Response{}}
	controller := NewPostgreSQLNodeController(transport, "agent-secret", nil)
	if err := controller.Promote(context.Background(), resolved, resolved.Target, ""); err == nil {
		t.Fatal("promotion without lease was accepted")
	}
	if len(transport.requests) != 0 {
		t.Fatalf("unsigned mutation was sent: %+v", transport.requests)
	}
}

func TestPostgreSQLNodeControllerRejectsMutationWithoutDurablePlanDigest(t *testing.T) {
	resolved := postgresqlNodeResolved()
	resolved.PlanDigest = ""
	transport := &postgresqlAgentTransportStub{responses: map[model.ResourceID]agent.Response{}}
	controller := NewPostgreSQLNodeController(transport, "agent-secret", nil)
	if err := controller.Promote(context.Background(), resolved, resolved.Target, model.NewResourceID()); err == nil {
		t.Fatal("promotion without a durable plan digest was accepted")
	}
	if len(transport.requests) != 0 {
		t.Fatalf("planless mutation was sent: %+v", transport.requests)
	}
}

func TestPostgreSQLNodeControllerRejectsSourceOutsideFrozenTopology(t *testing.T) {
	resolved := postgresqlNodeResolved()
	outside := resolved.Primary
	outside.ResourceID = model.NewResourceID()
	outside.Hostname = "pg-outside"
	outside.IPAddress = "192.0.2.99"
	transport := &postgresqlAgentTransportStub{responses: map[model.ResourceID]agent.Response{}}
	controller := NewPostgreSQLNodeController(transport, "agent-secret", nil)
	if err := controller.Repoint(context.Background(), resolved, resolved.Target, outside, model.NewResourceID()); err == nil {
		t.Fatal("source outside the frozen topology was accepted")
	}
	if len(transport.requests) != 0 {
		t.Fatalf("out-of-scope source mutation was sent: %+v", transport.requests)
	}
}

func TestPostgreSQLNodeControllerFailsClosedOnIncompleteAgentStatus(t *testing.T) {
	resolved := postgresqlNodeResolved()
	transport := &postgresqlAgentTransportStub{responses: map[model.ResourceID]agent.Response{
		resolved.Primary.ResourceID: {Status: agent.StatusOK, ClusterID: resolved.Cluster.ResourceID, InstanceID: resolved.Primary.ResourceID},
		resolved.Target.ResourceID:  postgresqlStatusResponse(resolved.Target, true, true),
	}}
	controller := NewPostgreSQLNodeController(transport, "agent-secret", nil)
	checks := controller.Precheck(context.Background(), resolved)
	if len(checks) == 0 || checks[0].Status != model.CheckFail {
		t.Fatalf("incomplete status checks=%+v", checks)
	}
}

func TestPostgreSQLNodeControllerFailsClosedOnAgentError(t *testing.T) {
	resolved := postgresqlNodeResolved()
	transport := &postgresqlAgentTransportStub{err: errors.New("ssh unavailable")}
	controller := NewPostgreSQLNodeController(transport, "agent-secret", nil)
	if stopped, err := controller.IsStopped(context.Background(), resolved, resolved.Primary); err == nil || stopped {
		t.Fatalf("stopped=%v err=%v", stopped, err)
	}
}

func TestPostgreSQLNodeControllerSurfacesAgentMutationError(t *testing.T) {
	resolved := postgresqlNodeResolved()
	transport := &postgresqlAgentTransportStub{responses: map[model.ResourceID]agent.Response{
		resolved.Target.ResourceID: {
			Status: agent.StatusError, ClusterID: resolved.Cluster.ResourceID, InstanceID: resolved.Target.ResourceID,
			Message: "agent command failed", Error: "standby did not converge on approved source",
		},
	}}
	controller := NewPostgreSQLNodeController(transport, "agent-secret", nil)
	err := controller.Repoint(context.Background(), resolved, resolved.Target, resolved.Primary, model.NewResourceID())
	if err == nil || !strings.Contains(err.Error(), "standby did not converge on approved source") || !strings.Contains(err.Error(), "postgresql_repoint") {
		t.Fatalf("agent error not surfaced: %v", err)
	}
}

func TestPostgreSQLNodeControllerExecutableRequiresTransportAndSecret(t *testing.T) {
	if NewPostgreSQLNodeController(nil, "secret", nil).Executable(context.Background()) {
		t.Fatal("nil transport advertised execution")
	}
	if NewPostgreSQLNodeController(&postgresqlAgentTransportStub{}, "", nil).Executable(context.Background()) {
		t.Fatal("empty secret advertised execution")
	}
}
