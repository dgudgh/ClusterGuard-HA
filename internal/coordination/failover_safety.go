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

const (
	failoverAgentProbeTimeout      = 2 * time.Second
	agentAuthorizationExpiryMargin = time.Second
	isolationSettleAttempts        = 5
	isolationSettleInterval        = 500 * time.Millisecond
)

type FailureStability interface {
	Stable(model.ResourceID, time.Time) bool
}

type stableFailureIncident interface {
	StableIncident(model.ResourceID, time.Time) bool
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

type scopedExternalFencer interface {
	ExternalFencer
	Available(model.ResourceID) bool
}

type LeadershipEpochProvider interface {
	LeadershipEpoch() uint64
}

type GuardedFailoverSafety struct {
	failures         FailureStability
	authority        MutationAuthority
	inventory        FailoverInventory
	leases           endpoint.LeaseStore
	transport        endpoint.AgentTransport
	external         ExternalFencer
	agentQuorum      bool
	agentQuorumGrace time.Duration
	authorizations   *AgentAuthorizationTracker
	wait             func(context.Context, time.Duration) error
	secret           string
	now              func() time.Time
}

type GuardedFailoverOption func(*GuardedFailoverSafety)

func WithExternalFencer(fencer ExternalFencer) GuardedFailoverOption {
	return func(provider *GuardedFailoverSafety) { provider.external = fencer }
}

// WithAgentQuorumFencing enables database-level failover without requiring a
// hypervisor or BMC. The controller publishes the target transition through
// Raft, then waits for every stale Agent authorization to expire before it can
// promote the target. Agents fail closed by releasing the VIP and isolating
// the local writer when they lose majority authorization.
func WithAgentQuorumFencing(grace time.Duration) GuardedFailoverOption {
	return func(provider *GuardedFailoverSafety) {
		provider.agentQuorum = true
		if grace < 15*time.Second {
			grace = 15 * time.Second
		}
		provider.agentQuorumGrace = grace
	}
}

func WithAgentAuthorizationTracker(tracker *AgentAuthorizationTracker) GuardedFailoverOption {
	return func(provider *GuardedFailoverSafety) { provider.authorizations = tracker }
}

func withFailoverWaiter(waiter func(context.Context, time.Duration) error) GuardedFailoverOption {
	return func(provider *GuardedFailoverSafety) {
		if waiter != nil {
			provider.wait = waiter
		}
	}
}

func NewGuardedFailoverSafety(failures FailureStability, authority MutationAuthority, inventory FailoverInventory, leases endpoint.LeaseStore, transport endpoint.AgentTransport, secret string, now func() time.Time, options ...GuardedFailoverOption) *GuardedFailoverSafety {
	if now == nil {
		now = time.Now
	}
	provider := &GuardedFailoverSafety{
		failures: failures, authority: authority, inventory: inventory, leases: leases,
		transport: transport, secret: strings.TrimSpace(secret), now: now,
		agentQuorumGrace: 15 * time.Second,
		wait: func(ctx context.Context, duration time.Duration) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
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

func (provider *GuardedFailoverSafety) externalAvailable(instanceID model.ResourceID) bool {
	if provider == nil || provider.external == nil {
		return false
	}
	if scoped, ok := provider.external.(scopedExternalFencer); ok {
		return scoped.Available(instanceID)
	}
	return true
}

func (provider *GuardedFailoverSafety) agentQuorumEnabled(resolved adapter.ResolvedOperation) bool {
	engineSupported := resolved.Cluster.Engine == model.EngineMySQL || resolved.Cluster.Engine == model.EnginePostgreSQL
	resource, err := provider.activeVIP(resolved.Cluster.ResourceID)
	linuxEndpoint := err == nil && endpointProviderKind(resource) == model.EndpointProviderLinuxVIP
	return provider != nil && provider.agentQuorum && engineSupported && linuxEndpoint &&
		provider.transport != nil && provider.secret != ""
}

func (provider *GuardedFailoverSafety) Precheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	checks := make([]model.Check, 0, 4)
	if resolved.Cluster.RecoveryFreeze {
		return []model.Check{
			{Name: "recovery_frozen", Status: model.CheckFail, Message: "cluster recovery is frozen by a planned shutdown"},
		}
	}
	if !provider.configured() {
		return []model.Check{
			{Name: "stable_primary_failure", Status: model.CheckFail, Message: "stable primary-failure observation is not configured"},
			{Name: "controller_quorum", Status: model.CheckFail, Message: "controller quorum is not configured"},
			{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary fencing is not configured"},
		}
	}
	stableFailure := provider.failures.Stable(resolved.Cluster.ResourceID, provider.now().UTC())
	if !resolved.AutomaticFailureIncidentAt.IsZero() {
		incidents, supported := provider.failures.(stableFailureIncident)
		stableFailure = supported && incidents.StableIncident(resolved.Cluster.ResourceID, resolved.AutomaticFailureIncidentAt)
	}
	if stableFailure {
		checks = append(checks, model.Check{Name: "stable_primary_failure", Status: model.CheckPass, Message: "consecutive primary-failure observations satisfy the configured stability window"})
	} else {
		checks = append(checks, model.Check{Name: "stable_primary_failure", Status: model.CheckFail, Message: "primary failure has not remained stable for the configured observation window"})
	}
	if err := provider.authority.RequireMutationAuthority(ctx); err != nil {
		checks = append(checks, model.Check{Name: "controller_quorum", Status: model.CheckFail, Message: "local controller lacks leader-backed majority authority"})
	} else {
		checks = append(checks, model.Check{Name: "controller_quorum", Status: model.CheckPass, Message: "local leader has controller majority authority"})
	}
	if provider.agentQuorumEnabled(resolved) {
		// Do not spend another network timeout against a primary that discovery
		// already proved unavailable. Fence performs the authoritative isolation
		// attempt and waits for the majority-backed Agent authorization to expire.
		checks = append(checks, model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "Raft majority lease and Agent fail-closed reconciliation will isolate the old primary before promotion"})
		return checks
	}
	probe, err := provider.probeIsolation(ctx, resolved)
	if err != nil {
		if provider.agentQuorumEnabled(resolved) {
			checks = append(checks, model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "Raft majority lease and Agent fail-closed reconciliation will isolate the old primary before promotion"})
		} else {
			checks = append(checks, model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary isolation path is unreachable or incomplete"})
		}
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
	leaseRequest := endpoint.LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: resource.haEndpoint.ResourceID,
		OperationID: resolved.OperationID, OwnerID: resolved.Target.ResourceID,
		PreviousOwnerID: resolved.Primary.ResourceID, TTL: provider.transitionLeaseTTL(resolved),
	}
	releaseTransition := func() {}
	if provider.authorizations != nil {
		releaseTransition = provider.authorizations.BeginTransition()
	}
	lease, err := provider.leases.Acquire(ctx, leaseRequest)
	transitionStartedAt := provider.now().UTC()
	trackedAuthorizationUntil, trackedAuthorization := provider.authorizationUntil(resolved)
	releaseTransition()
	if err != nil {
		return fmt.Errorf("acquire failover quorum lease: %w", err)
	}
	var agentFailure error
	if endpointProviderKind(resource) == model.EndpointProviderLinuxVIP && provider.transport != nil && provider.secret != "" {
		request, requestErr := provider.signedRequest(resolved, resource, agent.CommandSelfIsolate, lease.ResourceID)
		if requestErr == nil {
			probeContext, cancel := context.WithTimeout(ctx, failoverAgentProbeTimeout)
			response, sendErr := provider.transport.Send(probeContext, resolved.Primary, request)
			cancel()
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
	if !provider.externalAvailable(resolved.Primary.ResourceID) {
		if provider.agentQuorumEnabled(resolved) {
			return provider.awaitAgentQuorumFence(ctx, resolved, resource, leaseRequest, lease, transitionStartedAt, trackedAuthorizationUntil, trackedAuthorization)
		}
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

func (provider *GuardedFailoverSafety) authorizationUntil(resolved adapter.ResolvedOperation) (time.Time, bool) {
	if provider == nil || provider.authorizations == nil || provider.authority == nil {
		return time.Time{}, false
	}
	epochProvider, ok := provider.authority.(LeadershipEpochProvider)
	if !ok {
		return time.Time{}, false
	}
	return provider.authorizations.AuthorizationUntil(
		resolved.Cluster.ResourceID, resolved.Primary.ResourceID, epochProvider.LeadershipEpoch(),
	)
}

// transitionLeaseTTL deliberately covers the Agent fencing grace plus a
// bounded revalidation margin. A 30-second lease is sufficient for an
// immediate handoff, but it is too tight when the old node is unreachable and
// the controller must wait for every stale Agent authorization to expire.
func (provider *GuardedFailoverSafety) transitionLeaseTTL(resolved adapter.ResolvedOperation) time.Duration {
	if !provider.agentQuorumEnabled(resolved) {
		return 30 * time.Second
	}
	ttl := provider.agentQuorumGrace + 30*time.Second
	if ttl < 60*time.Second {
		ttl = 60 * time.Second
	}
	return ttl
}

func (provider *GuardedFailoverSafety) Verify(ctx context.Context, resolved adapter.ResolvedOperation) model.Check {
	if !provider.configured() {
		return model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary fencing is not configured"}
	}
	if provider.agentQuorumEnabled(resolved) && resolved.VerifiedIsolatedSourceID == resolved.Primary.ResourceID {
		if verifyErr := provider.verifyAgentQuorumLease(ctx, resolved); verifyErr == nil {
			return model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "old-primary isolation was established and the majority ownership lease remains active"}
		}
	}
	probeContext, cancel := context.WithTimeout(ctx, failoverAgentProbeTimeout)
	probe, err := provider.probeIsolation(probeContext, resolved)
	cancel()
	if err != nil {
		if provider.agentQuorumEnabled(resolved) {
			if verifyErr := provider.verifyAgentQuorumLease(ctx, resolved); verifyErr == nil {
				return model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "old-primary authorization expired and the majority transition lease remains active"}
			}
		}
		return model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary isolation cannot be verified"}
	}
	if !probe.isolated {
		return model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old primary still owns the writer endpoint or remains writable"}
	}
	return model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "old-primary isolation is verified through " + probe.method}
}

func (provider *GuardedFailoverSafety) awaitAgentQuorumFence(ctx context.Context, resolved adapter.ResolvedOperation, resource failoverVIPResource, request endpoint.LeaseRequest, transition endpoint.Lease, transitionStartedAt, authorizationUntil time.Time, trackedAuthorization bool) error {
	if provider.wait == nil {
		return fmt.Errorf("agent quorum fencing wait is not configured")
	}
	fenceDeadline := transitionStartedAt.Add(provider.agentQuorumGrace)
	if trackedAuthorization {
		fenceDeadline = authorizationUntil.UTC().Add(agentAuthorizationExpiryMargin)
	}
	remainingGrace := fenceDeadline.Sub(provider.now().UTC())
	if remainingGrace > 0 {
		if err := provider.wait(ctx, remainingGrace); err != nil {
			return fmt.Errorf("wait for stale Agent authorization expiry: %w", err)
		}
	}
	if err := provider.authority.RequireMutationAuthority(ctx); err != nil {
		return fmt.Errorf("revalidate controller majority after Agent fencing grace: %w", err)
	}
	// The workflow holds the discovery publication fence during this grace.
	// Requiring a new failure observation here would make the operation fail
	// because its own lock intentionally prevents that publication. Entry into
	// this method already required stable evidence. Re-acquire the exact lease
	// from the current leader majority instead, then prove that the unreachable
	// source has no remaining Agent authorization.
	renewed, err := provider.leases.Acquire(ctx, request)
	if err != nil {
		return fmt.Errorf("renew failover quorum lease after Agent fencing grace: %w", err)
	}
	transition = renewed
	if err := provider.leases.Validate(ctx, transition); err != nil {
		return fmt.Errorf("revalidate failover quorum lease: %w", err)
	}
	if err := provider.verifyAgentQuorumLeaseForResource(ctx, resolved, resource, false); err != nil {
		return err
	}
	var lastProbe isolationProbe
	for attempt := 0; attempt < isolationSettleAttempts; attempt++ {
		probeContext, cancel := context.WithTimeout(ctx, failoverAgentProbeTimeout)
		probe, probeErr := provider.probeIsolation(probeContext, resolved)
		cancel()
		if probeErr != nil {
			// An unreachable old node is accepted only after its short-lived
			// controller authorization has expired and the exact target
			// transition remains backed by the current Raft majority.
			return nil
		}
		lastProbe = probe
		if probe.isolated {
			return nil
		}
		if attempt+1 < isolationSettleAttempts {
			if err := provider.wait(ctx, isolationSettleInterval); err != nil {
				return fmt.Errorf("wait for old-primary isolation to settle: %w", err)
			}
		}
	}
	return fmt.Errorf("old primary remains reachable and is not isolated after the Agent fencing grace: %s", lastProbe.method)
}

func (provider *GuardedFailoverSafety) verifyAgentQuorumLease(ctx context.Context, resolved adapter.ResolvedOperation) error {
	resource, err := provider.activeVIP(resolved.Cluster.ResourceID)
	if err != nil {
		return err
	}
	if err := provider.authority.RequireMutationAuthority(ctx); err != nil {
		return err
	}
	return provider.verifyAgentQuorumLeaseForResource(ctx, resolved, resource, true)
}

func (provider *GuardedFailoverSafety) verifyAgentQuorumLeaseForResource(ctx context.Context, resolved adapter.ResolvedOperation, resource failoverVIPResource, allowFinalized bool) error {
	reader, ok := provider.leases.(endpoint.CurrentLeaseReader)
	if !ok {
		return fmt.Errorf("current majority lease cannot be inspected")
	}
	lease, err := reader.Current(ctx, resolved.Cluster.ResourceID, resource.haEndpoint.ResourceID)
	if err != nil {
		return fmt.Errorf("read current failover quorum lease: %w", err)
	}
	exactTransition := lease.OperationID == resolved.OperationID && lease.OwnerID == resolved.Target.ResourceID && lease.PreviousOwnerID == resolved.Primary.ResourceID
	finalizedTarget := allowFinalized && lease.OperationID == resource.haEndpoint.ResourceID && lease.OwnerID == resolved.Target.ResourceID &&
		resource.haEndpoint.OwnerID == resolved.Target.ResourceID && resource.endpoint.InstanceID == resolved.Target.ResourceID
	if !exactTransition && !finalizedTarget {
		return fmt.Errorf("current majority lease does not authorize the exact failover transition or its finalized target")
	}
	if err := provider.leases.Validate(ctx, lease); err != nil {
		return fmt.Errorf("validate current failover quorum lease: %w", err)
	}
	return nil
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
		if !exists || !candidate.Active {
			continue
		}
		kind := resource.Provider
		if kind == "" && resource.Kind == model.EndpointVIP && candidate.Kind == model.EndpointVIP {
			kind = model.EndpointProviderLinuxVIP
		}
		if kind == model.EndpointProviderLinuxVIP && (resource.Kind != model.EndpointVIP || candidate.Kind != model.EndpointVIP) {
			continue
		}
		if kind == model.EndpointProviderKubernetesService && (resource.Kind != model.EndpointService || candidate.Kind != model.EndpointService) {
			continue
		}
		if !kind.Valid() {
			continue
		}
		found++
		result = failoverVIPResource{haEndpoint: resource, endpoint: candidate}
	}
	if found != 1 {
		return failoverVIPResource{}, fmt.Errorf("exactly one active writer endpoint is required for guarded failover")
	}
	switch endpointProviderKind(result) {
	case model.EndpointProviderLinuxVIP:
		if strings.TrimSpace(result.endpoint.IPAddress) == "" || strings.TrimSpace(result.haEndpoint.Interface) == "" || result.haEndpoint.Prefix < 1 || result.haEndpoint.Prefix > 32 {
			return failoverVIPResource{}, fmt.Errorf("active Linux VIP is incomplete")
		}
	case model.EndpointProviderKubernetesService:
		if strings.TrimSpace(result.endpoint.Hostname) == "" || result.endpoint.Port < 1 || result.endpoint.Port > 65535 || strings.TrimSpace(result.haEndpoint.ProviderRef) == "" {
			return failoverVIPResource{}, fmt.Errorf("active Kubernetes Service endpoint is incomplete")
		}
	}
	return result, nil
}

func endpointProviderKind(resource failoverVIPResource) model.EndpointProviderKind {
	kind := resource.haEndpoint.Provider
	if kind == "" && resource.haEndpoint.Kind == model.EndpointVIP && resource.endpoint.Kind == model.EndpointVIP {
		return model.EndpointProviderLinuxVIP
	}
	return kind
}

func (provider *GuardedFailoverSafety) signedRequest(resolved adapter.ResolvedOperation, resource failoverVIPResource, command string, leaseID model.ResourceID) (agent.Request, error) {
	if endpointProviderKind(resource) != model.EndpointProviderLinuxVIP {
		return agent.Request{}, fmt.Errorf("restricted node Agent commands require a Linux VIP endpoint")
	}
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
	resource, resourceErr := provider.activeVIP(resolved.Cluster.ResourceID)
	if resourceErr == nil && endpointProviderKind(resource) == model.EndpointProviderLinuxVIP && provider.transport != nil && provider.secret != "" {
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
								method:   fmt.Sprintf("restricted PostgreSQL agent (owns_vip=%t service_running=%t)", *vipResponse.OwnsVIP, *roleResponse.ServiceRunning),
							}, nil
						}
						if resolved.Cluster.Engine != model.EnginePostgreSQL && roleErr == nil && roleResponse.Status == agent.StatusOK && roleResponse.ReadOnly != nil && roleResponse.SuperReadOnly != nil {
							if roleResponse.DatabaseReachable != nil && roleResponse.ServiceRunning != nil &&
								roleResponse.RestartReadOnly != nil && roleResponse.PersistedReadOnly != nil {
								if !*roleResponse.RestartReadOnly {
									return isolationProbe{}, fmt.Errorf("old-primary MySQL restart defaults are writable")
								}
								if !*roleResponse.DatabaseReachable {
									if *roleResponse.ServiceRunning {
										return isolationProbe{}, fmt.Errorf("old-primary MySQL service is active but its role is unreachable")
									}
									return isolationProbe{
										isolated: !*vipResponse.OwnsVIP && *roleResponse.PersistedReadOnly,
										method: fmt.Sprintf("restricted agent durable restart fence (owns_vip=%t service_running=%t database_reachable=%t restart_read_only=%t persisted_read_only=%t)",
											*vipResponse.OwnsVIP, *roleResponse.ServiceRunning, *roleResponse.DatabaseReachable, *roleResponse.RestartReadOnly, *roleResponse.PersistedReadOnly),
									}, nil
								}
								return isolationProbe{
									isolated: !*vipResponse.OwnsVIP && *roleResponse.ReadOnly && *roleResponse.SuperReadOnly && *roleResponse.PersistedReadOnly,
									method: fmt.Sprintf("restricted agent durable restart fence (owns_vip=%t service_running=%t database_reachable=%t read_only=%t super_read_only=%t restart_read_only=%t persisted_read_only=%t)",
										*vipResponse.OwnsVIP, *roleResponse.ServiceRunning, *roleResponse.DatabaseReachable, *roleResponse.ReadOnly, *roleResponse.SuperReadOnly, *roleResponse.RestartReadOnly, *roleResponse.PersistedReadOnly),
								}, nil
							}
							return isolationProbe{
								isolated: !*vipResponse.OwnsVIP && *roleResponse.ReadOnly && *roleResponse.SuperReadOnly,
								method: fmt.Sprintf("restricted agent (owns_vip=%t read_only=%t super_read_only=%t)",
									*vipResponse.OwnsVIP, *roleResponse.ReadOnly, *roleResponse.SuperReadOnly),
							}, nil
						}
					}
				}
			}
		}
	}
	if provider.externalAvailable(resolved.Primary.ResourceID) {
		fenced, err := provider.external.Status(ctx, provider.externalFenceRequest(resolved, ""))
		if err != nil {
			return isolationProbe{}, fmt.Errorf("external old-primary fence status is unavailable: %w", err)
		}
		return isolationProbe{isolated: fenced, method: "external fencing"}, nil
	}
	return isolationProbe{}, fmt.Errorf("old-primary VIP and role status are unavailable")
}
