package coordination

import (
	"context"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type FailureStability interface {
	Stable(model.ResourceID, time.Time) bool
}

type FailoverInventory interface {
	HAEndpoints(model.ResourceID) []model.HAEndpoint
	Endpoint(model.ResourceID) (model.Endpoint, bool)
}

type GuardedFailoverSafety struct {
	failures  FailureStability
	authority MutationAuthority
	inventory FailoverInventory
	leases    endpoint.LeaseStore
	transport endpoint.AgentTransport
	secret    string
	now       func() time.Time
}

func NewGuardedFailoverSafety(failures FailureStability, authority MutationAuthority, inventory FailoverInventory, leases endpoint.LeaseStore, transport endpoint.AgentTransport, secret string, now func() time.Time) *GuardedFailoverSafety {
	if now == nil {
		now = time.Now
	}
	return &GuardedFailoverSafety{
		failures: failures, authority: authority, inventory: inventory, leases: leases,
		transport: transport, secret: strings.TrimSpace(secret), now: now,
	}
}

func (provider *GuardedFailoverSafety) configured() bool {
	return provider != nil && provider.failures != nil && provider.authority != nil && provider.inventory != nil && provider.leases != nil && provider.transport != nil && provider.secret != ""
}

func (provider *GuardedFailoverSafety) Precheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	checks := make([]model.Check, 0, 3)
	if !provider.configured() {
		return []model.Check{
			{Name: "stable_primary_failure", Status: model.CheckFail, Message: "stable primary-failure observation is not configured"},
			{Name: "controller_quorum", Status: model.CheckFail, Message: "controller quorum is not configured"},
			{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary fencing is not configured"},
		}
	}
	if provider.failures.Stable(resolved.Cluster.ResourceID, provider.now().UTC()) {
		checks = append(checks, model.Check{Name: "stable_primary_failure", Status: model.CheckPass, Message: "six follow-up failure checks span 30 seconds"})
	} else {
		checks = append(checks, model.Check{Name: "stable_primary_failure", Status: model.CheckFail, Message: "primary failure has not remained stable for six checks across 30 seconds"})
	}
	if err := provider.authority.RequireMutationAuthority(ctx); err != nil {
		checks = append(checks, model.Check{Name: "controller_quorum", Status: model.CheckFail, Message: "local controller lacks leader-backed majority authority"})
	} else {
		checks = append(checks, model.Check{Name: "controller_quorum", Status: model.CheckPass, Message: "local leader has controller majority authority"})
	}
	_, isolated, err := provider.probeIsolation(ctx, resolved)
	if err != nil {
		checks = append(checks, model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary isolation path is unreachable or incomplete"})
	} else if isolated {
		checks = append(checks, model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "old primary is already read-only and does not own the VIP"})
	} else {
		checks = append(checks, model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "restricted old-primary isolation path is reachable and will run before promotion"})
	}
	return checks
}

func (provider *GuardedFailoverSafety) Fence(ctx context.Context, resolved adapter.ResolvedOperation) error {
	if !provider.configured() {
		return fmt.Errorf("guarded failover safety is not configured")
	}
	if err := provider.authority.RequireMutationAuthority(ctx); err != nil {
		return err
	}
	if !provider.failures.Stable(resolved.Cluster.ResourceID, provider.now().UTC()) {
		return fmt.Errorf("stable primary-failure evidence expired")
	}
	resource, err := provider.activeVIP(resolved.Cluster.ResourceID)
	if err != nil {
		return err
	}
	lease, err := provider.leases.Acquire(ctx, endpoint.LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: resource.haEndpoint.ResourceID,
		OperationID: resolved.OperationID, OwnerID: resolved.Target.ResourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("acquire failover quorum lease: %w", err)
	}
	request, err := provider.signedRequest(resolved, resource, agent.CommandSelfIsolate, lease.ResourceID)
	if err != nil {
		return err
	}
	response, err := provider.transport.Send(ctx, resolved.Primary, request)
	if err != nil {
		return fmt.Errorf("isolate old primary: %w", err)
	}
	if response.Status != agent.StatusOK {
		return fmt.Errorf("old-primary agent blocked isolation")
	}
	return nil
}

func (provider *GuardedFailoverSafety) Verify(ctx context.Context, resolved adapter.ResolvedOperation) model.Check {
	if !provider.configured() {
		return model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary fencing is not configured"}
	}
	_, isolated, err := provider.probeIsolation(ctx, resolved)
	if err != nil {
		return model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary isolation cannot be verified"}
	}
	if !isolated {
		return model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old primary still owns the VIP or remains writable"}
	}
	return model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "old primary has no VIP and both read-only flags are enabled"}
}

type failoverVIPResource struct {
	haEndpoint model.HAEndpoint
	endpoint   model.Endpoint
}

func (provider *GuardedFailoverSafety) activeVIP(clusterID model.ResourceID) (failoverVIPResource, error) {
	var result failoverVIPResource
	found := 0
	for _, resource := range provider.inventory.HAEndpoints(clusterID) {
		candidate, exists := provider.inventory.Endpoint(resource.EndpointID)
		if !exists || resource.Kind != model.EndpointVIP || candidate.Kind != model.EndpointVIP || !candidate.Active {
			continue
		}
		found++
		result = failoverVIPResource{haEndpoint: resource, endpoint: candidate}
	}
	if found != 1 || strings.TrimSpace(result.endpoint.IPAddress) == "" || strings.TrimSpace(result.haEndpoint.Interface) == "" || result.haEndpoint.Prefix < 1 || result.haEndpoint.Prefix > 32 {
		return failoverVIPResource{}, fmt.Errorf("exactly one complete active VIP is required for guarded failover")
	}
	return result, nil
}

func (provider *GuardedFailoverSafety) signedRequest(resolved adapter.ResolvedOperation, resource failoverVIPResource, command string, leaseID model.ResourceID) (agent.Request, error) {
	request := agent.Request{
		Command: command, ClusterID: resolved.Cluster.ResourceID, OperationID: resolved.OperationID,
		LeaseID: leaseID, PlanDigest: resolved.PlanDigest, ExpiresAt: provider.now().UTC().Add(30 * time.Second),
		VIP: resource.endpoint.IPAddress, Interface: resource.haEndpoint.Interface, Prefix: resource.haEndpoint.Prefix,
	}
	if strings.TrimSpace(request.PlanDigest) == "" {
		request.PlanDigest = "sha256:failover-precheck:" + string(resolved.OperationID)
	}
	signature, err := agent.SignRequest(request, provider.secret)
	if err != nil {
		return agent.Request{}, err
	}
	request.Signature = signature
	return request, nil
}

func (provider *GuardedFailoverSafety) probeIsolation(ctx context.Context, resolved adapter.ResolvedOperation) (bool, bool, error) {
	resource, err := provider.activeVIP(resolved.Cluster.ResourceID)
	if err != nil {
		return false, false, err
	}
	vipRequest, err := provider.signedRequest(resolved, resource, agent.CommandVIPStatus, "")
	if err != nil {
		return false, false, err
	}
	vipResponse, err := provider.transport.Send(ctx, resolved.Primary, vipRequest)
	if err != nil || vipResponse.Status != agent.StatusOK || vipResponse.OwnsVIP == nil {
		return false, false, fmt.Errorf("old-primary VIP status is unavailable")
	}
	roleRequest, err := provider.signedRequest(resolved, resource, agent.CommandRoleStatus, "")
	if err != nil {
		return false, false, err
	}
	roleResponse, err := provider.transport.Send(ctx, resolved.Primary, roleRequest)
	if err != nil || roleResponse.Status != agent.StatusOK || roleResponse.ReadOnly == nil || roleResponse.SuperReadOnly == nil {
		return false, false, fmt.Errorf("old-primary role status is unavailable")
	}
	isolated := !*vipResponse.OwnsVIP && *roleResponse.ReadOnly && *roleResponse.SuperReadOnly
	return true, isolated, nil
}
