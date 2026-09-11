package endpoint

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type Inventory interface {
	HAEndpoints(model.ResourceID) []model.HAEndpoint
	Endpoint(model.ResourceID) (model.Endpoint, bool)
	CommitHAEndpointOwner(model.ResourceID, model.ResourceID, model.ResourceID, bool) error
}

type AgentTransport interface {
	Send(context.Context, model.DatabaseInstance, agent.Request) (agent.Response, error)
}

type LinuxVIPProvider struct {
	inventory               Inventory
	transport               AgentTransport
	leases                  LeaseStore
	secret                  string
	now                     func() time.Time
	transitionLeaseTTL      time.Duration
	transitionRenewInterval time.Duration
}

func NewLinuxVIPProvider(inventory Inventory, transport AgentTransport, leases LeaseStore, secret string, now func() time.Time) *LinuxVIPProvider {
	if now == nil {
		now = time.Now
	}
	return &LinuxVIPProvider{
		inventory: inventory, transport: transport, leases: leases, secret: secret, now: now,
		transitionLeaseTTL: 30 * time.Second, transitionRenewInterval: 10 * time.Second,
	}
}

func (provider *LinuxVIPProvider) Executable(context.Context) bool {
	return provider != nil && provider.inventory != nil && provider.transport != nil && provider.leases != nil && strings.TrimSpace(provider.secret) != ""
}

type endpointResource struct {
	resource model.HAEndpoint
	endpoint model.Endpoint
}

type OwnershipObservation struct {
	HAEndpointID        model.ResourceID   `json:"ha_endpoint_id"`
	HAEndpointRevision  uint64             `json:"ha_endpoint_revision"`
	EndpointRevision    uint64             `json:"endpoint_revision"`
	CanonicalOwnerID    model.ResourceID   `json:"canonical_owner_id,omitempty"`
	EndpointOwnerID     model.ResourceID   `json:"endpoint_owner_id,omitempty"`
	OwnerIDs            []model.ResourceID `json:"owner_ids"`
	ObservedInstanceIDs []model.ResourceID `json:"observed_instance_ids"`
	Complete            bool               `json:"complete"`
}

func (provider *LinuxVIPProvider) resource(clusterID model.ResourceID) (endpointResource, error) {
	resources := provider.inventory.HAEndpoints(clusterID)
	active := make([]model.HAEndpoint, 0, 1)
	for _, resource := range resources {
		endpoint, found := provider.inventory.Endpoint(resource.EndpointID)
		if found && resource.Kind == model.EndpointVIP && endpoint.Kind == model.EndpointVIP && endpoint.Active {
			active = append(active, resource)
		}
	}
	if len(active) != 1 {
		return endpointResource{}, fmt.Errorf("exactly one active VIP resource is required")
	}
	providerKind := active[0].Provider
	if providerKind == "" {
		providerKind = model.EndpointProviderLinuxVIP
	}
	if providerKind != model.EndpointProviderLinuxVIP {
		return endpointResource{}, fmt.Errorf("active VIP requires endpoint provider %s, which is unsupported by the Linux VIP runtime", providerKind)
	}
	endpoint, found := provider.inventory.Endpoint(active[0].EndpointID)
	if !found || strings.TrimSpace(endpoint.IPAddress) == "" || strings.TrimSpace(active[0].Interface) == "" || active[0].Prefix < 1 || active[0].Prefix > 32 {
		return endpointResource{}, fmt.Errorf("active VIP resource is incomplete")
	}
	return endpointResource{resource: active[0], endpoint: endpoint}, nil
}

type ownerObservation struct {
	instance model.DatabaseInstance
	owns     bool
	err      error
}

func (provider *LinuxVIPProvider) signedRequest(resolved adapter.ResolvedOperation, resource endpointResource, command string, leaseID model.ResourceID) (agent.Request, error) {
	digest := strings.TrimSpace(resolved.PlanDigest)
	if digest == "" {
		digest = "sha256:precheck:" + string(resolved.OperationID)
	}
	request := agent.Request{
		Command: command, ClusterID: resolved.Cluster.ResourceID, OperationID: resolved.OperationID,
		LeaseID: leaseID, PlanDigest: digest, ExpiresAt: provider.now().UTC().Add(30 * time.Second),
		Engine: resolved.Cluster.Engine,
		VIP:    resource.endpoint.IPAddress, Interface: resource.resource.Interface, Prefix: resource.resource.Prefix,
	}
	signature, err := agent.SignRequest(request, provider.secret)
	if err != nil {
		return agent.Request{}, err
	}
	request.Signature = signature
	return request, nil
}

func (provider *LinuxVIPProvider) observe(ctx context.Context, resolved adapter.ResolvedOperation, resource endpointResource) []ownerObservation {
	return provider.observeWithSkip(ctx, resolved, resource, "")
}

func (provider *LinuxVIPProvider) observeTransition(ctx context.Context, resolved adapter.ResolvedOperation, resource endpointResource) []ownerObservation {
	verifiedSource := resolved.VerifiedIsolatedSourceID
	if verifiedSource != resolved.Primary.ResourceID || verifiedSource == resolved.Target.ResourceID {
		verifiedSource = ""
	}
	return provider.observeWithSkip(ctx, resolved, resource, verifiedSource)
}

func (provider *LinuxVIPProvider) observeWithSkip(ctx context.Context, resolved adapter.ResolvedOperation, resource endpointResource, skip model.ResourceID) []ownerObservation {
	observations := make([]ownerObservation, len(resolved.Snapshot.Instances))
	var wait sync.WaitGroup
	for index, instance := range resolved.Snapshot.Instances {
		observations[index].instance = instance
		if model.ValidResourceID(skip) && instance.ResourceID == skip {
			observations[index].err = fmt.Errorf("source isolation was independently verified")
			continue
		}
		wait.Add(1)
		go func(index int, instance model.DatabaseInstance) {
			defer wait.Done()
			request, err := provider.signedRequest(resolved, resource, agent.CommandVIPStatus, "")
			if err != nil {
				observations[index].err = err
				return
			}
			response, err := provider.transport.Send(ctx, instance, request)
			if err != nil || response.Status != agent.StatusOK || response.OwnsVIP == nil {
				if err == nil {
					reason := strings.TrimSpace(response.Error)
					if reason == "" {
						reason = strings.TrimSpace(response.Message)
					}
					if reason == "" {
						reason = "agent returned incomplete VIP status"
					}
					err = fmt.Errorf("agent VIP status was not accepted: %s", reason)
				}
				observations[index].err = err
				return
			}
			observations[index].owns = *response.OwnsVIP
		}(index, instance)
	}
	wait.Wait()
	return observations
}

func observationCoverage(observations []ownerObservation) ([]model.ResourceID, []model.ResourceID, bool) {
	owners := make([]model.ResourceID, 0, 1)
	observed := make([]model.ResourceID, 0, len(observations))
	complete := len(observations) > 0
	for _, observation := range observations {
		if observation.err != nil {
			complete = false
			continue
		}
		observed = append(observed, observation.instance.ResourceID)
		if observation.owns {
			owners = append(owners, observation.instance.ResourceID)
		}
	}
	return owners, observed, complete
}

func observationSummary(observations []ownerObservation) ([]model.ResourceID, bool) {
	owners, _, complete := observationCoverage(observations)
	return owners, complete
}

// transitionObservationSummary permits exactly one missing probe: the operation
// source whose isolation was independently verified by the engine failover
// safety provider. Every other instance must still answer, so a second unknown
// node or an observed duplicate VIP owner remains fail-closed.
func transitionObservationSummary(resolved adapter.ResolvedOperation, observations []ownerObservation) ([]model.ResourceID, bool) {
	owners := make([]model.ResourceID, 0, 1)
	complete := len(observations) > 0
	verifiedSource := resolved.VerifiedIsolatedSourceID
	allowIsolatedSourceGap := model.ValidResourceID(verifiedSource) &&
		verifiedSource == resolved.Primary.ResourceID && resolved.Primary.ResourceID != resolved.Target.ResourceID
	for _, observation := range observations {
		if observation.err != nil {
			if allowIsolatedSourceGap && observation.instance.ResourceID == verifiedSource {
				continue
			}
			complete = false
			continue
		}
		if observation.owns {
			owners = append(owners, observation.instance.ResourceID)
		}
	}
	return owners, complete
}

func (provider *LinuxVIPProvider) ObserveOwnership(ctx context.Context, cluster model.DatabaseCluster, snapshot model.TopologySnapshot) (OwnershipObservation, error) {
	if !provider.Executable(ctx) {
		return OwnershipObservation{}, fmt.Errorf("writer endpoint provider is not configured")
	}
	if cluster.ResourceID == "" || snapshot.ClusterID != cluster.ResourceID {
		return OwnershipObservation{}, fmt.Errorf("VIP observation cluster scope is invalid")
	}
	resource, err := provider.resource(cluster.ResourceID)
	if err != nil {
		return OwnershipObservation{}, err
	}
	resolved := adapter.ResolvedOperation{
		OperationID: resource.resource.ResourceID,
		Cluster:     cluster,
		Snapshot:    snapshot,
		PlanDigest:  "sha256:ownership:" + string(resource.resource.ResourceID),
	}
	owners, observed, complete := observationCoverage(provider.observe(ctx, resolved, resource))
	return OwnershipObservation{
		HAEndpointID: resource.resource.ResourceID, HAEndpointRevision: resource.resource.MetadataRevision,
		EndpointRevision: resource.endpoint.MetadataRevision, CanonicalOwnerID: resource.resource.OwnerID,
		EndpointOwnerID: resource.endpoint.InstanceID, OwnerIDs: owners,
		ObservedInstanceIDs: observed, Complete: complete,
	}, nil
}

func (provider *LinuxVIPProvider) Precheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	if !provider.Executable(ctx) {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: "writer endpoint provider is not configured"}}
	}
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: err.Error()}}
	}
	owners, complete := observationSummary(provider.observe(ctx, resolved, resource))
	if !complete {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: "VIP probe coverage is incomplete"}}
	}
	if len(owners) > 1 {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: "multiple VIP owners were detected"}}
	}
	if len(owners) != 1 || owners[0] != resolved.Primary.ResourceID {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: "VIP must be owned only by the current primary"}}
	}
	return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckPass, Message: "VIP has one current-primary owner with complete probe coverage"}}
}

// FormerPrimaryPrecheck proves the narrower invariant needed to rejoin a
// recovered primary. The current primary and recovered node must both answer,
// while an unrelated probe gap is tolerated only with majority coverage and a
// matching stable quorum lease.
func (provider *LinuxVIPProvider) FormerPrimaryPrecheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	fail := func(message string) []model.Check {
		return []model.Check{{Name: "former_primary_vip_absent", Status: model.CheckFail, Message: message}}
	}
	pass := func(message string) []model.Check {
		return []model.Check{{Name: "former_primary_vip_absent", Status: model.CheckPass, Message: message}}
	}
	if !provider.Executable(ctx) {
		return fail("writer endpoint provider is not configured")
	}
	if !model.ValidResourceID(resolved.Primary.ResourceID) || !model.ValidResourceID(resolved.Target.ResourceID) || resolved.Primary.ResourceID == resolved.Target.ResourceID {
		return fail("current primary and former primary identities are invalid")
	}
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return fail(err.Error())
	}
	if resource.resource.OwnerID != resolved.Primary.ResourceID || resource.endpoint.InstanceID != resolved.Primary.ResourceID {
		return fail("canonical VIP ownership does not identify the current primary")
	}

	observations := provider.observeTransition(ctx, resolved, resource)
	owners, observed, _ := observationCoverage(observations)
	byID := make(map[model.ResourceID]ownerObservation, len(observations))
	for _, observation := range observations {
		if !model.ValidResourceID(observation.instance.ResourceID) || byID[observation.instance.ResourceID].instance.ResourceID != "" {
			return fail("VIP observation contains an invalid or duplicate instance identity")
		}
		byID[observation.instance.ResourceID] = observation
	}
	primaryObservation, primaryFound := byID[resolved.Primary.ResourceID]
	formerObservation, formerFound := byID[resolved.Target.ResourceID]
	if !primaryFound || !formerFound {
		return fail("current primary or former primary is outside the observed topology")
	}
	instanceLabel := func(instance model.DatabaseInstance) string {
		host := strings.TrimSpace(instance.Hostname)
		if host == "" {
			host = strings.TrimSpace(instance.IPAddress)
		}
		if instance.Port > 0 {
			return fmt.Sprintf("%s:%d", host, instance.Port)
		}
		return host
	}
	if primaryObservation.err != nil {
		return fail(fmt.Sprintf("current primary %s VIP probe failed: %v", instanceLabel(primaryObservation.instance), primaryObservation.err))
	}
	if formerObservation.err != nil {
		return fail(fmt.Sprintf("former primary %s VIP probe failed: %v", instanceLabel(formerObservation.instance), formerObservation.err))
	}
	if !primaryObservation.owns {
		return fail("current primary does not own the configured VIP")
	}
	if formerObservation.owns {
		return fail("former primary still owns the configured VIP")
	}
	if len(owners) != 1 || owners[0] != resolved.Primary.ResourceID {
		return fail("an observed node other than the current primary owns the configured VIP")
	}
	if len(observed) <= len(resolved.Snapshot.Instances)/2 {
		return fail(fmt.Sprintf("VIP ownership probe has no majority coverage (%d/%d)", len(observed), len(resolved.Snapshot.Instances)))
	}

	reader, ok := provider.leases.(CurrentLeaseReader)
	if !ok {
		return fail("current majority-backed VIP lease cannot be inspected")
	}
	lease, err := reader.Current(ctx, resolved.Cluster.ResourceID, resource.resource.ResourceID)
	if err != nil {
		return fail(fmt.Sprintf("current majority-backed VIP lease is unavailable: %v", err))
	}
	if lease.OperationID != resource.resource.ResourceID || lease.OwnerID != resolved.Primary.ResourceID || lease.PreviousOwnerID != "" {
		return fail("current VIP lease is not a stable lease for the current primary")
	}
	if err := provider.leases.Validate(ctx, lease); err != nil {
		return fail(fmt.Sprintf("current majority-backed VIP lease is invalid: %v", err))
	}
	if len(observed) == len(resolved.Snapshot.Instances) {
		return pass("former primary has no VIP; current primary is the sole owner with complete probe coverage and a stable majority lease")
	}
	return pass(fmt.Sprintf("former primary has no VIP; current primary ownership is directly verified with majority coverage (%d/%d) and a stable lease", len(observed), len(resolved.Snapshot.Instances)))
}

func (provider *LinuxVIPProvider) sendMutation(ctx context.Context, resolved adapter.ResolvedOperation, resource endpointResource, instance model.DatabaseInstance, command string, lease Lease) error {
	if err := provider.leases.Validate(ctx, lease); err != nil {
		return err
	}
	request, err := provider.signedRequest(resolved, resource, command, lease.ResourceID)
	if err != nil {
		return err
	}
	response, err := provider.transport.Send(ctx, instance, request)
	if err != nil {
		return err
	}
	if response.Status != agent.StatusOK {
		reason := strings.TrimSpace(response.Error)
		if reason == "" {
			reason = strings.TrimSpace(response.Message)
		}
		if reason == "" {
			return fmt.Errorf("agent blocked VIP mutation")
		}
		return fmt.Errorf("agent blocked VIP mutation: %s", reason)
	}
	return nil
}

func (provider *LinuxVIPProvider) acquireTransitionLease(ctx context.Context, resolved adapter.ResolvedOperation, resource endpointResource) (Lease, error) {
	ttl := provider.transitionLeaseTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	lease, err := provider.leases.Acquire(ctx, LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: resource.resource.ResourceID,
		OperationID: resolved.OperationID, OwnerID: resolved.Target.ResourceID,
		PreviousOwnerID: resolved.Primary.ResourceID, TTL: ttl,
	})
	if err != nil {
		return Lease{}, fmt.Errorf("acquire endpoint lease: %w", err)
	}
	return lease, nil
}

func (provider *LinuxVIPProvider) AuthorizeStableOwner(ctx context.Context, resolved adapter.ResolvedOperation) (adapter.StableOwnershipAuthorization, error) {
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return adapter.StableOwnershipAuthorization{}, err
	}
	if resource.resource.OwnerID != resolved.Primary.ResourceID || resource.endpoint.InstanceID != resolved.Primary.ResourceID {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("canonical writer endpoint does not identify the current primary")
	}
	reader, ok := provider.leases.(CurrentLeaseReader)
	if !ok {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("current stable endpoint lease cannot be inspected")
	}
	current, err := reader.Current(ctx, resolved.Cluster.ResourceID, resource.resource.ResourceID)
	if err != nil {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("inspect current stable endpoint lease: %w", err)
	}
	if current.OperationID != resource.resource.ResourceID || current.OwnerID != resolved.Primary.ResourceID || current.PreviousOwnerID != "" {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("current endpoint lease is not stable for the current primary")
	}
	if err := provider.leases.Validate(ctx, current); err != nil {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("validate current stable endpoint lease: %w", err)
	}

	ttl := provider.transitionLeaseTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	interval := provider.transitionRenewInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	request := LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: resource.resource.ResourceID,
		OperationID: resource.resource.ResourceID, OwnerID: resolved.Primary.ResourceID,
		TTL: ttl, RenewOnly: true,
	}
	renewed, err := provider.leases.Acquire(ctx, request)
	if err != nil {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("renew current stable endpoint lease: %w", err)
	}
	if !SameLeaseIdentity(current, renewed) {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("stable endpoint lease identity changed during renewal")
	}

	guarded, cancelCause := context.WithCancelCause(ctx)
	stopRenewal := make(chan struct{})
	renewalStopped := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		defer close(renewalStopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-guarded.Done():
				return
			case <-stopRenewal:
				return
			case <-ticker.C:
				lease, renewErr := provider.leases.Acquire(guarded, request)
				if renewErr != nil {
					cancelCause(fmt.Errorf("renew stable primary endpoint lease: %w", renewErr))
					return
				}
				if !SameLeaseIdentity(current, lease) {
					cancelCause(fmt.Errorf("stable primary endpoint lease identity changed"))
					return
				}
			}
		}
	}()
	stop := func() {
		stopOnce.Do(func() { close(stopRenewal) })
		<-renewalStopped
	}
	return adapter.StableOwnershipAuthorization{
		Context: guarded,
		LeaseID: renewed.ResourceID,
		Cancel: func() {
			stop()
			cancelCause(context.Canceled)
		},
	}, nil
}

func (provider *LinuxVIPProvider) AuthorizeTransition(ctx context.Context, resolved adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return adapter.TransitionAuthorization{}, err
	}
	lease, err := provider.acquireTransitionLease(ctx, resolved, resource)
	if err != nil {
		return adapter.TransitionAuthorization{}, err
	}
	guarded, cancelCause := context.WithCancelCause(ctx)
	ttl := provider.transitionLeaseTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	interval := provider.transitionRenewInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	var renewalMu sync.Mutex
	renewalLease := lease
	renewalRequest := LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: resource.resource.ResourceID,
		OperationID: resolved.OperationID, OwnerID: resolved.Target.ResourceID,
		PreviousOwnerID: resolved.Primary.ResourceID, TTL: ttl, RenewOnly: true,
	}
	renew := func(renewContext context.Context) error {
		renewalMu.Lock()
		defer renewalMu.Unlock()
		renewed, renewErr := provider.leases.Acquire(renewContext, renewalRequest)
		if renewErr == nil && !SameLeaseIdentity(renewalLease, renewed) {
			renewErr = fmt.Errorf("target lease identity changed during renewal")
		}
		if renewErr == nil {
			renewalLease = renewed
		}
		return renewErr
	}
	stopRenewal := make(chan struct{})
	renewalStopped := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		defer close(renewalStopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-guarded.Done():
				return
			case <-stopRenewal:
				return
			case <-ticker.C:
				if renewErr := renew(guarded); renewErr != nil {
					cancelCause(fmt.Errorf("renew target transition lease: %w", renewErr))
					return
				}
			}
		}
	}()
	stop := func() {
		stopOnce.Do(func() { close(stopRenewal) })
		<-renewalStopped
	}
	var terminalMu sync.Mutex
	terminalAction := ""
	var terminalErr error
	finalize := func(finalizeContext context.Context) error {
		terminalMu.Lock()
		defer terminalMu.Unlock()
		if terminalAction != "" {
			if terminalAction == "finalize" {
				return terminalErr
			}
			return fmt.Errorf("target transition lease was already aborted")
		}
		if guarded.Err() != nil {
			return context.Cause(guarded)
		}
		terminalAction = "finalize"
		renewalMu.Lock()
		stable, finalizeErr := provider.leases.FinalizeTransition(finalizeContext, renewalLease, ttl)
		if finalizeErr == nil {
			renewalLease = stable
			renewalRequest = LeaseRequest{
				ClusterID: stable.ClusterID, HAEndpointID: stable.HAEndpointID,
				OperationID: stable.HAEndpointID, OwnerID: stable.OwnerID,
				TTL: ttl, RenewOnly: true,
			}
		}
		renewalMu.Unlock()
		terminalErr = finalizeErr
		if terminalErr != nil {
			cancelCause(fmt.Errorf("finalize target transition lease: %w", terminalErr))
		}
		return terminalErr
	}
	abort := func(abortContext context.Context) error {
		terminalMu.Lock()
		defer terminalMu.Unlock()
		if terminalAction != "" {
			if terminalAction == "abort" {
				return terminalErr
			}
			return fmt.Errorf("target transition lease was already finalized")
		}
		stop()
		terminalAction = "abort"
		renewalMu.Lock()
		currentLease := renewalLease
		renewalMu.Unlock()
		_, terminalErr = provider.leases.RollbackTransition(abortContext, currentLease, ttl)
		if terminalErr != nil {
			cancelCause(fmt.Errorf("rollback target transition lease: %w", terminalErr))
		}
		return terminalErr
	}
	return adapter.TransitionAuthorization{
		Context: guarded,
		LeaseID: lease.ResourceID,
		Cancel: func() {
			stop()
			cancelCause(context.Canceled)
		},
		Abort:    abort,
		Finalize: finalize,
	}, nil
}

func (provider *LinuxVIPProvider) Transfer(ctx context.Context, resolved adapter.ResolvedOperation) error {
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return err
	}
	lease, err := provider.acquireTransitionLease(ctx, resolved, resource)
	if err != nil {
		return err
	}
	observations := provider.observeTransition(ctx, resolved, resource)
	owners, complete := transitionObservationSummary(resolved, observations)
	if !complete || len(owners) > 1 {
		return fmt.Errorf("VIP ownership is unsafe before transfer")
	}
	targetAlreadyOwns := len(owners) == 1 && owners[0] == resolved.Target.ResourceID
	if !targetAlreadyOwns {
		releasedOwner := false
		for _, observation := range observations {
			if observation.owns && observation.instance.ResourceID != resolved.Target.ResourceID {
				if err := provider.sendMutation(ctx, resolved, resource, observation.instance, agent.CommandVIPRelease, lease); err != nil {
					return fmt.Errorf("release source VIP: %w", err)
				}
				releasedOwner = true
			}
		}
		// A second cluster-wide observation is required only after a release.
		// If the first complete observation already proved zero owners, repeating
		// the same read adds latency without strengthening the invariant.
		if releasedOwner {
			owners, complete = transitionObservationSummary(resolved, provider.observeTransition(ctx, resolved, resource))
			if !complete || len(owners) != 0 {
				return fmt.Errorf("zero VIP ownership could not be proven")
			}
		}
		if err := provider.sendMutation(ctx, resolved, resource, resolved.Target, agent.CommandVIPAcquire, lease); err != nil {
			return fmt.Errorf("acquire target VIP: %w", err)
		}
	}
	owners, complete = transitionObservationSummary(resolved, provider.observeTransition(ctx, resolved, resource))
	if !complete || len(owners) != 1 || owners[0] != resolved.Target.ResourceID {
		return fmt.Errorf("target-only VIP ownership could not be verified")
	}
	if err := provider.inventory.CommitHAEndpointOwner(resolved.Cluster.ResourceID, resource.resource.ResourceID, resolved.Target.ResourceID, true); err != nil {
		return fmt.Errorf("commit VIP ownership metadata: %w", err)
	}
	committed, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil || committed.resource.OwnerID != resolved.Target.ResourceID || committed.endpoint.InstanceID != resolved.Target.ResourceID {
		return fmt.Errorf("physical and metadata VIP ownership did not converge")
	}
	return nil
}

func (provider *LinuxVIPProvider) Verify(ctx context.Context, resolved adapter.ResolvedOperation) model.Check {
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: err.Error()}
	}
	owners, complete := transitionObservationSummary(resolved, provider.observeTransition(ctx, resolved, resource))
	if !complete {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "VIP probe coverage is incomplete"}
	}
	if len(owners) != 1 || owners[0] != resolved.Target.ResourceID {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "VIP is not owned only by the operation target"}
	}
	if resource.resource.OwnerID != resolved.Target.ResourceID || resource.endpoint.InstanceID != resolved.Target.ResourceID {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "physical VIP ownership is correct but canonical ownership metadata is stale"}
	}
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass, Message: "VIP physical and metadata ownership match only the operation target"}
}
