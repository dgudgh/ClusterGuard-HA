package endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const defaultExternalCommandTimeout = 30 * time.Second
const defaultExternalCommandMaxOutput = 8 * 1024

// externalCommandStage is the single argument a hook receives. Every other
// value travels on stdin as JSON, so no cluster field can ever reach a command
// line. A ResolvedOperation is deliberately not passed through: it carries live
// engine credentials, and handing those to whatever executable an endpoint
// names would turn endpoint configuration into credential disclosure.
type externalCommandStage string

const (
	stagePrecheck              externalCommandStage = "precheck"
	stageAuthorize             externalCommandStage = "authorize"
	stageTransfer              externalCommandStage = "transfer"
	stageVerify                externalCommandStage = "verify"
	stageFormerPrimaryPrecheck externalCommandStage = "former_primary_precheck"
	stageObserve               externalCommandStage = "observe"
	stageFinalize              externalCommandStage = "finalize"
	stageAbort                 externalCommandStage = "abort"
)

// externalCommandRequest is the complete payload written to the hook's stdin.
// It is an explicit allowlist, so adding a field to a model type later cannot
// silently widen what the hook can see.
type externalCommandRequest struct {
	OperationID        model.ResourceID     `json:"operation_id"`
	Stage              externalCommandStage `json:"stage"`
	ClusterID          model.ResourceID     `json:"cluster_id"`
	HAEndpointID       model.ResourceID     `json:"ha_endpoint_id"`
	HAEndpointRevision uint64               `json:"ha_endpoint_revision"`
	EndpointID         model.ResourceID     `json:"endpoint_id"`
	EndpointRevision   uint64               `json:"endpoint_revision"`
	Address            string               `json:"address,omitempty"`
	Port               int                  `json:"port,omitempty"`
	CurrentPrimaryID   model.ResourceID     `json:"current_primary_instance_id,omitempty"`
	CurrentPrimaryHost string               `json:"current_primary_host,omitempty"`
	TargetInstanceID   model.ResourceID     `json:"target_instance_id,omitempty"`
	TargetHost         string               `json:"target_host,omitempty"`
}

// externalCommandObservation is what a hook reports about the data plane.
//
// Complete must be true for the answer to be usable. A hook that can only see
// part of the topology has proven nothing about who else is accepting writes,
// and the provider refuses rather than inferring the rest.
type externalCommandObservation struct {
	OwnerIDs []model.ResourceID `json:"owner_ids"`
	Complete bool               `json:"complete"`
	Message  string             `json:"message,omitempty"`
}

// ExternalCommandProvider moves the writer endpoint by invoking an operator
// supplied executable instead of announcing an address on a local interface.
//
// The authoritative decision stays where it always was: the consensus and lease
// layers decide who the writable primary is, and the hook is only told to move
// traffic to that already-decided target. The hook never chooses the target, so
// a wrong or malicious hook can fail a transition but cannot promote anyone.
type ExternalCommandProvider struct {
	inventory Inventory
	runner    ProcessRunner
	timeout   time.Duration
	maxOutput int
}

func NewExternalCommandProvider(inventory Inventory, runner ProcessRunner, timeout time.Duration, maxOutputBytes int) *ExternalCommandProvider {
	if timeout <= 0 {
		timeout = defaultExternalCommandTimeout
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultExternalCommandMaxOutput
	}
	return &ExternalCommandProvider{inventory: inventory, runner: runner, timeout: timeout, maxOutput: maxOutputBytes}
}

func (provider *ExternalCommandProvider) Executable(context.Context) bool {
	return provider != nil && provider.inventory != nil && provider.runner != nil
}

// resource selects the single active endpoint owned by this provider. The
// Provider field is matched exactly, so resources owned by the VIP or
// Kubernetes providers are never considered even when they look similar.
func (provider *ExternalCommandProvider) resource(clusterID model.ResourceID) (endpointResource, error) {
	if provider == nil || provider.inventory == nil {
		return endpointResource{}, errors.New("external command writer endpoint provider is not configured")
	}
	if !model.ValidResourceID(clusterID) {
		return endpointResource{}, errors.New("external command writer endpoint requires a valid cluster id")
	}
	var selected endpointResource
	matched := 0
	for _, resource := range provider.inventory.HAEndpoints(clusterID) {
		if resource.Provider != model.EndpointProviderExternalCommand {
			continue
		}
		endpoint, exists := provider.inventory.Endpoint(resource.EndpointID)
		if !exists || !endpoint.Active {
			continue
		}
		if endpoint.Kind != model.EndpointService {
			continue
		}
		matched++
		selected = endpointResource{resource: resource, endpoint: endpoint}
	}
	if matched != 1 {
		return endpointResource{}, fmt.Errorf("exactly one active external-command service endpoint resource is required, found %d", matched)
	}
	return selected, nil
}

// hook resolves the executable this endpoint names and refuses anything a
// lower-privileged user could influence. It runs with control-plane authority,
// so a path inside a group- or world-writable directory, or owned by an
// unrelated account, is arbitrary code execution for whoever can write there.
func hookExecutable(reference string) (string, error) {
	return validateHookFile(reference, os.Getuid())
}

// validateHookFile takes the current uid explicitly so that the ownership rule
// can be exercised without a second account to chown files to.
func validateHookFile(reference string, currentUID int) (string, error) {
	trimmed := strings.TrimSpace(reference)
	if trimmed == "" {
		return "", errors.New("external command endpoint does not name a hook executable")
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("hook executable must be an absolute path, got %q", trimmed)
	}
	cleaned := filepath.Clean(trimmed)
	// Lstat rather than Stat: a symlink would let the configured path point
	// anywhere, including a file whose permissions were never reviewed.
	info, err := os.Lstat(cleaned)
	if err != nil {
		return "", fmt.Errorf("hook executable %s is not readable: %w", cleaned, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("hook executable %s must not be a symbolic link", cleaned)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("hook executable %s must be a regular file", cleaned)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("hook executable %s must not be group- or world-writable", cleaned)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("hook executable %s ownership could not be determined", cleaned)
	}
	if int(stat.Uid) != 0 && int(stat.Uid) != currentUID {
		return "", fmt.Errorf("hook executable %s must be owned by root or by uid %d", cleaned, currentUID)
	}
	return cleaned, nil
}

func (provider *ExternalCommandProvider) request(stage externalCommandStage, resolved adapter.ResolvedOperation, resource endpointResource) externalCommandRequest {
	request := externalCommandRequest{
		OperationID:        resolved.OperationID,
		Stage:              stage,
		ClusterID:          resolved.Cluster.ResourceID,
		HAEndpointID:       resource.resource.ResourceID,
		HAEndpointRevision: resource.resource.MetadataRevision,
		EndpointID:         resource.endpoint.ResourceID,
		EndpointRevision:   resource.endpoint.MetadataRevision,
		Address:            resource.endpoint.Hostname,
		Port:               resource.endpoint.Port,
	}
	if strings.TrimSpace(request.Address) == "" {
		request.Address = resource.endpoint.IPAddress
	}
	request.CurrentPrimaryID = resolved.Primary.ResourceID
	request.CurrentPrimaryHost = resolved.Primary.Hostname
	request.TargetInstanceID = resolved.Target.ResourceID
	request.TargetHost = resolved.Target.Hostname
	return request
}

// runHook executes one stage. Every failure is fatal to the caller: there is no
// stage whose failure may be ignored, because an unanswered data plane question
// is indistinguishable from a data plane that is still pointing at the old
// primary.
func (provider *ExternalCommandProvider) runHook(ctx context.Context, hook string, stage externalCommandStage, request externalCommandRequest) ([]byte, error) {
	if provider == nil || provider.runner == nil {
		return nil, errors.New("external command writer endpoint runner is not configured")
	}
	request.Stage = stage
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode external endpoint %s request: %w", stage, err)
	}
	hookCtx, cancel := context.WithTimeout(ctx, provider.timeout)
	defer cancel()
	output, err := provider.runner.Run(hookCtx, payload, hook, string(stage))
	if err != nil {
		return nil, fmt.Errorf("external endpoint hook %s failed: %w", stage, err)
	}
	if len(output) > provider.maxOutput {
		return nil, fmt.Errorf("external endpoint hook %s produced %d bytes of output, limit is %d", stage, len(output), provider.maxOutput)
	}
	return output, nil
}

func (provider *ExternalCommandProvider) observe(ctx context.Context, hook string, stage externalCommandStage, request externalCommandRequest) (externalCommandObservation, error) {
	output, err := provider.runHook(ctx, hook, stage, request)
	if err != nil {
		return externalCommandObservation{}, err
	}
	var observation externalCommandObservation
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return externalCommandObservation{}, fmt.Errorf("external endpoint hook %s produced no observation", stage)
	}
	if err := json.Unmarshal([]byte(trimmed), &observation); err != nil {
		return externalCommandObservation{}, fmt.Errorf("external endpoint hook %s returned unreadable JSON: %w", stage, err)
	}
	return observation, nil
}

func (provider *ExternalCommandProvider) Precheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: err.Error()}}
	}
	hook, err := hookExecutable(resource.resource.ProviderRef)
	if err != nil {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: err.Error()}}
	}
	if _, err := provider.runHook(ctx, hook, stagePrecheck, provider.request(stagePrecheck, resolved, resource)); err != nil {
		return []model.Check{{Name: "writer_endpoint_precheck", Status: model.CheckFail, Message: err.Error()}}
	}
	return []model.Check{{Name: "writer_endpoint_precheck", Status: model.CheckPass, Message: "external endpoint hook accepted the precheck"}}
}

func (provider *ExternalCommandProvider) AuthorizeTransition(ctx context.Context, resolved adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return adapter.TransitionAuthorization{}, err
	}
	hook, err := hookExecutable(resource.resource.ProviderRef)
	if err != nil {
		return adapter.TransitionAuthorization{}, err
	}
	request := provider.request(stageAuthorize, resolved, resource)
	if _, err := provider.runHook(ctx, hook, stageAuthorize, request); err != nil {
		return adapter.TransitionAuthorization{}, err
	}
	transitionCtx, cancel := context.WithCancel(ctx)
	leaseID := model.ResourceID(fmt.Sprintf("%s-external-%s", resource.resource.ResourceID, resolved.OperationID))
	return adapter.TransitionAuthorization{
		Context: transitionCtx,
		Cancel:  cancel,
		LeaseID: leaseID,
		Abort: func(abortCtx context.Context) error {
			_, err := provider.runHook(abortCtx, hook, stageAbort, request)
			return err
		},
		Finalize: func(finalizeCtx context.Context) error {
			if _, err := provider.observe(finalizeCtx, hook, stageVerify, request); err != nil {
				return fmt.Errorf("external endpoint did not verify after finalize: %w", err)
			}
			_, err := provider.runHook(finalizeCtx, hook, stageFinalize, request)
			return err
		},
	}, nil
}

// Transfer moves the endpoint under the same protocol the VIP provider uses:
// prove nobody but the target owns it, move it, prove the target owns it, then
// converge metadata and re-read it. The difference is only how each fact is
// measured - here the hook answers instead of a gratuitous ARP probe.
func (provider *ExternalCommandProvider) Transfer(ctx context.Context, resolved adapter.ResolvedOperation) error {
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return err
	}
	hook, err := hookExecutable(resource.resource.ProviderRef)
	if err != nil {
		return err
	}
	request := provider.request(stageTransfer, resolved, resource)

	before, err := provider.observe(ctx, hook, stageObserve, request)
	if err != nil {
		return err
	}
	if !before.Complete {
		return fmt.Errorf("external endpoint ownership coverage is incomplete before transfer: %s", strings.TrimSpace(before.Message))
	}
	if len(before.OwnerIDs) > 1 {
		return fmt.Errorf("external endpoint is claimed by %d instances before transfer", len(before.OwnerIDs))
	}
	targetAlreadyOwns := len(before.OwnerIDs) == 1 && before.OwnerIDs[0] == resolved.Target.ResourceID
	if len(before.OwnerIDs) == 1 && !targetAlreadyOwns {
		return fmt.Errorf("external endpoint is owned by %s, which is not the operation target", before.OwnerIDs[0])
	}

	if !targetAlreadyOwns {
		if _, err := provider.runHook(ctx, hook, stageTransfer, request); err != nil {
			return fmt.Errorf("move external endpoint to target: %w", err)
		}
	}

	after, err := provider.observe(ctx, hook, stageObserve, request)
	if err != nil {
		return err
	}
	if !after.Complete {
		return fmt.Errorf("external endpoint ownership coverage is incomplete after transfer: %s", strings.TrimSpace(after.Message))
	}
	if len(after.OwnerIDs) != 1 || after.OwnerIDs[0] != resolved.Target.ResourceID {
		return fmt.Errorf("external endpoint did not converge on the operation target")
	}

	if err := provider.inventory.CommitHAEndpointOwner(resolved.Cluster.ResourceID, resource.resource.ResourceID, resolved.Target.ResourceID, true); err != nil {
		return fmt.Errorf("commit external endpoint ownership metadata: %w", err)
	}
	committed, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return err
	}
	if committed.resource.OwnerID != resolved.Target.ResourceID || committed.endpoint.InstanceID != resolved.Target.ResourceID {
		return fmt.Errorf("external endpoint ownership metadata did not converge")
	}
	return nil
}

func (provider *ExternalCommandProvider) Verify(ctx context.Context, resolved adapter.ResolvedOperation) model.Check {
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: err.Error()}
	}
	hook, err := hookExecutable(resource.resource.ProviderRef)
	if err != nil {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: err.Error()}
	}
	request := provider.request(stageVerify, resolved, resource)
	observation, err := provider.observe(ctx, hook, stageVerify, request)
	if err != nil {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: err.Error()}
	}
	if !observation.Complete {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "external endpoint ownership coverage is incomplete"}
	}
	if len(observation.OwnerIDs) != 1 || observation.OwnerIDs[0] != resolved.Target.ResourceID {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "external endpoint is not owned only by the operation target"}
	}
	if resource.resource.OwnerID != resolved.Target.ResourceID || resource.endpoint.InstanceID != resolved.Target.ResourceID {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "external endpoint ownership is correct but canonical ownership metadata is stale"}
	}
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass, Message: "external endpoint ownership and metadata match only the operation target"}
}

// FormerPrimaryPrecheck asks the hook whether the instance being promoted away
// from still attracts writes, which is the split-brain question the VIP
// provider answers by probing every node.
func (provider *ExternalCommandProvider) FormerPrimaryPrecheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckFail, Message: err.Error()}}
	}
	hook, err := hookExecutable(resource.resource.ProviderRef)
	if err != nil {
		return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckFail, Message: err.Error()}}
	}
	request := provider.request(stageFormerPrimaryPrecheck, resolved, resource)
	observation, err := provider.observe(ctx, hook, stageFormerPrimaryPrecheck, request)
	if err != nil {
		return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckFail, Message: err.Error()}}
	}
	if !observation.Complete {
		return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckFail, Message: "external endpoint ownership coverage is incomplete"}}
	}
	for _, ownerID := range observation.OwnerIDs {
		if ownerID == resolved.Primary.ResourceID {
			return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckFail, Message: "external endpoint is still owned by the former primary"}}
		}
	}
	return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckPass, Message: "former primary no longer owns the external endpoint"}}
}

func (provider *ExternalCommandProvider) ObserveOwnership(ctx context.Context, cluster model.DatabaseCluster, snapshot model.TopologySnapshot) (OwnershipObservation, error) {
	resource, err := provider.resource(cluster.ResourceID)
	if err != nil {
		return OwnershipObservation{}, err
	}
	hook, err := hookExecutable(resource.resource.ProviderRef)
	if err != nil {
		return OwnershipObservation{}, err
	}
	request := externalCommandRequest{
		Stage:              stageObserve,
		ClusterID:          cluster.ResourceID,
		HAEndpointID:       resource.resource.ResourceID,
		HAEndpointRevision: resource.resource.MetadataRevision,
		EndpointID:         resource.endpoint.ResourceID,
		EndpointRevision:   resource.endpoint.MetadataRevision,
		Address:            resource.endpoint.Hostname,
		Port:               resource.endpoint.Port,
	}
	if strings.TrimSpace(request.Address) == "" {
		request.Address = resource.endpoint.IPAddress
	}
	observation, err := provider.observe(ctx, hook, stageObserve, request)
	if err != nil {
		return OwnershipObservation{}, err
	}
	observed := make([]model.ResourceID, 0, len(snapshot.Instances))
	for _, instance := range snapshot.Instances {
		observed = append(observed, instance.ResourceID)
	}
	return OwnershipObservation{
		HAEndpointID:        resource.resource.ResourceID,
		HAEndpointRevision:  resource.resource.MetadataRevision,
		EndpointRevision:    resource.endpoint.MetadataRevision,
		CanonicalOwnerID:    resource.resource.OwnerID,
		EndpointOwnerID:     resource.endpoint.InstanceID,
		OwnerIDs:            observation.OwnerIDs,
		ObservedInstanceIDs: observed,
		Complete:            observation.Complete,
	}, nil
}
