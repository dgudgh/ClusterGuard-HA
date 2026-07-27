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

type ExternalFenceRequest struct {
	ClusterID   model.ResourceID       `json:"cluster_id"`
	OperationID model.ResourceID       `json:"operation_id"`
	LeaseID     model.ResourceID       `json:"lease_id,omitempty"`
	Instance    model.DatabaseInstance `json:"instance"`
}

type ExternalFencer interface {
	Fence(context.Context, ExternalFenceRequest) error
	Status(context.Context, ExternalFenceRequest) (bool, error)
}

type GuardedFailoverSafety struct {
	failures  FailureStability
	authority MutationAuthority
	inventory FailoverInventory
	leases    endpoint.LeaseStore
	transport endpoint.AgentTransport
	external  ExternalFencer
	secret    string
	now       func() time.Time
}

type GuardedFailoverOption func(*GuardedFailoverSafety)

func WithExternalFencer(fencer ExternalFencer) GuardedFailoverOption {
	return func(provider *GuardedFailoverSafety) { provider.external = fencer }
}

func NewGuardedFailoverSafety(failures FailureStability, authority MutationAuthority, inventory FailoverInventory, leases endpoint.LeaseStore, transport endpoint.AgentTransport, secret string, now func() time.Time, options ...GuardedFailoverOption) *GuardedFailoverSafety {
	if now == nil {
		now = time.Now
	}
	provider := &GuardedFailoverSafety{
		failures: failures, authority: authority, inventory: inventory, leases: leases,
		transport: transport, secret: strings.TrimSpace(secret), now: now,
	}
	for _, option := range options {
		if option != nil {
			option(provider)
		}
	}
	return provider
}

func (provider *GuardedFailoverSafety) configured() bool {
	if provider == nil || provider.failures == nil || provider.authority == nil || provider.inventory == nil || provider.leases == nil {
		return false
	}
	return (provider.transport != nil && provider.secret != "") || provider.external != nil
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
	probe, err := provider.probeIsolation(ctx, resolved)
	if err != nil {
		checks = append(checks, model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary isolation path is unreachable or incomplete"})
	} else if probe.isolated {
		checks = append(checks, model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "old primary isolation is verified through " + probe.method})
	} else {
		checks = append(checks, model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: probe.method + " old-primary isolation path is reachable and will run before promotion"})
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
		OperationID: resolved.OperationID, OwnerID: resolved.Target.ResourceID,
		PreviousOwnerID: resolved.Primary.ResourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("acquire failover quorum lease: %w", err)
	}
	var agentFailure error
	if provider.transport != nil && provider.secret != "" {
		request, requestErr := provider.signedRequest(resolved, resource, agent.CommandSelfIsolate, lease.ResourceID)
		if requestErr == nil {
			response, sendErr := provider.transport.Send(ctx, resolved.Primary, request)
			switch {
			case sendErr != nil:
				agentFailure = fmt.Errorf("isolate old primary through restricted agent: %w", sendErr)
			case response.Status != agent.StatusOK:
				agentFailure = fmt.Errorf("old-primary agent blocked isolation")
			default:
				return nil
			}
		} else {
			agentFailure = requestErr
		}
	}
	if provider.external == nil {
		if agentFailure != nil {
			return agentFailure
		}
		return fmt.Errorf("old-primary isolation path is unavailable")
	}
	externalRequest := provider.externalFenceRequest(resolved, lease.ResourceID)
	if err := provider.external.Fence(ctx, externalRequest); err != nil {
		return fmt.Errorf("externally fence old primary: %w", err)
	}
	fenced, err := provider.external.Status(ctx, externalRequest)
	if err != nil {
		return fmt.Errorf("verify external fencing: %w", err)
	}
	if !fenced {
		return fmt.Errorf("verify external fencing: old primary is not proven isolated")
	}
	return nil
}

func (provider *GuardedFailoverSafety) Verify(ctx context.Context, resolved adapter.ResolvedOperation) model.Check {
	if !provider.configured() {
		return model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary fencing is not configured"}
	}
	probe, err := provider.probeIsolation(ctx, resolved)
	if err != nil {
		return model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary isolation cannot be verified"}
	}
	if !probe.isolated {
		return model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old primary still owns the VIP or remains writable"}
	}
	return model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "old-primary isolation is verified through " + probe.method}
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
		Engine:  resolved.Cluster.Engine,
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

type isolationProbe struct {
	isolated bool
	method   string
}

func (provider *GuardedFailoverSafety) externalFenceRequest(resolved adapter.ResolvedOperation, leaseID model.ResourceID) ExternalFenceRequest {
	return ExternalFenceRequest{
		ClusterID: resolved.Cluster.ResourceID, OperationID: resolved.OperationID,
		LeaseID: leaseID, Instance: resolved.Primary,
	}
}

func (provider *GuardedFailoverSafety) probeIsolation(ctx context.Context, resolved adapter.ResolvedOperation) (isolationProbe, error) {
	if provider.transport != nil && provider.secret != "" {
		resource, err := provider.activeVIP(resolved.Cluster.ResourceID)
		if err == nil {
			vipRequest, requestErr := provider.signedRequest(resolved, resource, agent.CommandVIPStatus, "")
			if requestErr == nil {
				vipResponse, sendErr := provider.transport.Send(ctx, resolved.Primary, vipRequest)
				if sendErr == nil && vipResponse.Status == agent.StatusOK && vipResponse.OwnsVIP != nil {
					roleCommand := agent.CommandRoleStatus
					if resolved.Cluster.Engine == model.EnginePostgreSQL {
						roleCommand = agent.CommandPostgreSQLStatus
					}
					roleRequest, roleRequestErr := provider.signedRequest(resolved, resource, roleCommand, "")
					if roleRequestErr == nil {
						roleResponse, roleErr := provider.transport.Send(ctx, resolved.Primary, roleRequest)
						if resolved.Cluster.Engine == model.EnginePostgreSQL && roleErr == nil && roleResponse.Status == agent.StatusOK && roleResponse.ServiceRunning != nil {
							return isolationProbe{
								isolated: !*vipResponse.OwnsVIP && !*roleResponse.ServiceRunning,
								method:   "restricted PostgreSQL agent",
							}, nil
						}
						if resolved.Cluster.Engine != model.EnginePostgreSQL && roleErr == nil && roleResponse.Status == agent.StatusOK && roleResponse.ReadOnly != nil && roleResponse.SuperReadOnly != nil {
							return isolationProbe{
								isolated: !*vipResponse.OwnsVIP && *roleResponse.ReadOnly && *roleResponse.SuperReadOnly,
								method:   "restricted agent",
							}, nil
						}
					}
				}
			}
		}
	}
	if provider.external != nil {
		fenced, err := provider.external.Status(ctx, provider.externalFenceRequest(resolved, ""))
		if err != nil {
			return isolationProbe{}, fmt.Errorf("external old-primary fence status is unavailable: %w", err)
		}
		return isolationProbe{isolated: fenced, method: "external fencing"}, nil
	}
	return isolationProbe{}, fmt.Errorf("old-primary VIP and role status are unavailable")
}
