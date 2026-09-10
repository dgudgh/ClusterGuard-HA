package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/disaster"
	"clusterguard.io/ha/internal/discovery"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"clusterguard.io/ha/pkg/redact"
)

type disasterDriver struct {
	repository  *store.Repository
	transport   endpoint.AgentTransport
	secret      string
	authority   coordination.MutationAuthority
	leases      *coordination.LeaseStore
	refresher   *discovery.Service
	mysql       mysql.DisasterExecutor
	credentials func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error)
}

func newDisasterManager(repository *store.Repository, authority coordination.MutationAuthority, locks runtimeLocks, maintenance disaster.Maintenance, driver disaster.Driver) *disaster.Manager {
	// Recovery must publish fresh physical observations before and after Commit.
	// The lifecycle lock retains quorum exclusion without holding publication.
	return &disaster.Manager{Store: repository, Authority: authority, Locks: locks.lifecycle, Maintenance: maintenance, Driver: driver}
}

func (d *disasterDriver) authorize(ctx context.Context, task model.RecoveryTask) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.authority.RequireMutationAuthority(ctx); err != nil {
		return err
	}
	current, found := d.repository.RecoveryTask(task.ResourceID)
	if !found || current.LeaseID != task.LeaseID || current.Stage == model.RecoveryBlocked || current.Stage == model.RecoverySucceeded {
		return fmt.Errorf("recovery task no longer owns the operation")
	}
	for _, lease := range d.repository.CoordinationOperationLocks() {
		if lease.ResourceID == task.LeaseID && lease.OperationID == task.ResourceID && lease.ClusterID == task.ClusterID && lease.ExpiresAt.After(time.Now().UTC()) {
			return nil
		}
	}
	return fmt.Errorf("recovery operation lease expired")
}

func (d *disasterDriver) send(ctx context.Context, task model.RecoveryTask, member model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	if task.LeaseID != "" {
		if err := d.authorize(ctx, task); err != nil {
			return agent.Response{}, err
		}
	}
	data, err := json.Marshal(struct {
		ID, Lease  model.ResourceID
		Generation uint64
		Members    []model.DatabaseInstance
	}{task.ResourceID, task.LeaseID, task.InventoryGeneration, task.Members})
	if err != nil {
		return agent.Response{}, err
	}
	digest := sha256.Sum256(data)
	request.Engine = task.Engine
	request.ClusterID = task.ClusterID
	request.OperationID = model.NewResourceID()
	request.LeaseID = task.LeaseID
	request.PlanDigest = "sha256:" + hex.EncodeToString(digest[:])
	request.ExpiresAt = time.Now().UTC().Add(2 * time.Minute)
	request.Signature, err = agent.SignRequest(request, d.secret)
	if err != nil {
		return agent.Response{}, err
	}
	response, err := d.transport.Send(ctx, member, request)
	if err != nil {
		return response, err
	}
	if response.Status != agent.StatusOK {
		return response, fmt.Errorf("member %s: %s", member.ResourceID, redact.Bounded(response.Message+" "+response.Error, 2048))
	}
	if response.ClusterID != task.ClusterID || response.InstanceID != member.ResourceID {
		return response, fmt.Errorf("recovery Agent response identity does not match the frozen member")
	}
	return response, nil
}

func (d *disasterDriver) credential(ctx context.Context, task model.RecoveryTask) (adapter.OperationCredentials, error) {
	cluster, found := d.repository.Cluster(task.ClusterID)
	if !found {
		return adapter.OperationCredentials{}, fmt.Errorf("recovery cluster disappeared")
	}
	return d.credentials(ctx, cluster)
}

func (d *disasterDriver) vip(task model.RecoveryTask) (model.HAEndpoint, error) {
	var selected model.HAEndpoint
	for _, resource := range d.repository.HAEndpoints(task.ClusterID) {
		ep, found := d.repository.Endpoint(resource.EndpointID)
		if !found || !ep.Active {
			continue
		}
		if resource.Kind != model.EndpointVIP || ep.Kind != model.EndpointVIP || (resource.Provider != "" && resource.Provider != model.EndpointProviderLinuxVIP) {
			return selected, fmt.Errorf("one-click recovery currently requires a fixed Agent-managed VIP")
		}
		if selected.ResourceID != "" {
			return selected, fmt.Errorf("recovery business entry is ambiguous")
		}
		selected = resource
	}
	if selected.ResourceID == "" {
		return selected, fmt.Errorf("recovery requires a registered business VIP")
	}
	return selected, nil
}

func (d *disasterDriver) Preflight(ctx context.Context, task model.RecoveryTask) error {
	if d.transport == nil || d.secret == "" || d.authority == nil || d.leases == nil || d.refresher == nil {
		return fmt.Errorf("recovery runtime is not configured")
	}
	if _, err := d.vip(task); err != nil {
		return err
	}
	credentials, err := d.credential(ctx, task)
	if err != nil {
		return err
	}
	for _, member := range task.Members {
		// Preflight is read-only and may inspect a task before it owns a lease.
		preflight := task
		preflight.LeaseID = ""
		response, err := d.send(ctx, preflight, member, agent.Request{Command: agent.CommandRecoveryCapabilities})
		if err != nil {
			return err
		}
		if response.RecoveryVersion < 1 {
			return fmt.Errorf("member %s needs the guarded recovery Agent upgrade", member.ResourceID)
		}
		if task.Engine == model.EngineMySQL {
			status, err := d.send(ctx, preflight, member, agent.Request{Command: agent.CommandMySQLPowerStatus})
			if err != nil {
				return err
			}
			if status.ServiceRunning == nil || status.DatabaseReachable == nil {
				return fmt.Errorf("MySQL recovery cannot establish member power state")
			}
			if *status.DatabaseReachable {
				if err = d.mysql.Preflight(ctx, member, credentials.Administrative); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (d *disasterDriver) Fence(ctx context.Context, task model.RecoveryTask, member model.DatabaseInstance) error {
	_, isolateErr := d.send(ctx, task, member, agent.Request{Command: agent.CommandSelfIsolate})
	if task.Engine == model.EnginePostgreSQL {
		if isolateErr != nil {
			return isolateErr
		}
		if _, err := d.send(ctx, task, member, agent.Request{Command: agent.CommandPostgreSQLStop}); err != nil {
			return err
		}
		state, err := d.send(ctx, task, member, agent.Request{Command: agent.CommandPostgreSQLStatus})
		if err != nil || state.ServiceRunning == nil || *state.ServiceRunning {
			return errors.Join(err, fmt.Errorf("PostgreSQL member is not proven stopped"))
		}
		return nil
	}
	state, err := d.send(ctx, task, member, agent.Request{Command: agent.CommandMySQLPowerStatus})
	if err != nil {
		return errors.Join(isolateErr, err)
	}
	if state.DatabaseReachable == nil || !*state.DatabaseReachable {
		// Do not start a stopped service on the strength of a host role file.
		// The Agent must also verify persisted-variable and service overrides.
		if _, err := d.send(ctx, task, member, agent.Request{Command: agent.CommandRecoveryFencedStart, RecoveryTaskID: task.ResourceID}); err != nil {
			return err
		}
	}
	if _, err = d.send(ctx, task, member, agent.Request{Command: agent.CommandSelfIsolate}); err != nil {
		return err
	}
	return nil
}

func (d *disasterDriver) prepareMySQL(ctx context.Context, task model.RecoveryTask, member model.DatabaseInstance) error {
	var err error
	if _, err = d.send(ctx, task, member, agent.Request{Command: agent.CommandRecoveryReplicaGuard, RecoveryTaskID: task.ResourceID}); err != nil {
		return err
	}
	credentials, err := d.credential(ctx, task)
	if err != nil {
		return err
	}
	if err = d.mysql.Prepare(ctx, member, credentials.Administrative, func() error { return d.authorize(ctx, task) }, func() error { return d.verifyMySQLOffline(ctx, task, member, credentials.Administrative) }); err != nil {
		return err
	}
	_, err = d.send(ctx, task, member, agent.Request{Command: agent.CommandRecoveryReplicaRelease, RecoveryTaskID: task.ResourceID})
	return err
}

func (d *disasterDriver) Inspect(ctx context.Context, task model.RecoveryTask, member model.DatabaseInstance) (model.RecoveryEvidence, error) {
	if task.Engine == model.EngineMySQL {
		// Plugin preparation is allowed only after every member was fenced and
		// the previous writer permits have drained.
		if err := d.prepareMySQL(ctx, task, member); err != nil {
			return model.RecoveryEvidence{}, err
		}
		if _, err := d.send(ctx, task, member, agent.Request{Command: agent.CommandRecoveryQuiesce}); err != nil {
			return model.RecoveryEvidence{}, err
		}
	}
	response, err := d.send(ctx, task, member, agent.Request{Command: agent.CommandRecoveryInspect})
	if err != nil {
		return model.RecoveryEvidence{}, err
	}
	if response.RecoveryEvidence == nil {
		return model.RecoveryEvidence{}, fmt.Errorf("Agent omitted recovery evidence")
	}
	return *response.RecoveryEvidence, nil
}

type recoveryWALReader struct {
	driver *disasterDriver
	task   model.RecoveryTask
}

func (r recoveryWALReader) VerifyWAL(ctx context.Context, e model.RecoveryEvidence, request model.RecoveryWALRequest) (model.RecoveryWALResult, error) {
	for _, member := range r.task.Members {
		if member.ResourceID == e.InstanceID {
			response, err := r.driver.send(ctx, r.task, member, agent.Request{Command: agent.CommandRecoveryWAL, RecoveryWAL: &request})
			if err != nil {
				return model.RecoveryWALResult{}, err
			}
			if response.RecoveryWAL == nil {
				return model.RecoveryWALResult{}, fmt.Errorf("Agent omitted WAL proof")
			}
			return *response.RecoveryWAL, nil
		}
	}
	return model.RecoveryWALResult{}, fmt.Errorf("WAL proof member is outside the frozen inventory")
}
func (d *disasterDriver) Compare(ctx context.Context, task model.RecoveryTask, evidence []model.RecoveryEvidence) ([]model.RecoveryProof, error) {
	if task.Engine == model.EngineMySQL {
		return nil, nil
	}
	return disaster.CompareWAL(ctx, evidence, recoveryWALReader{d, task})
}

func recoveryMember(task model.RecoveryTask, id model.ResourceID) (model.DatabaseInstance, model.RecoveryEvidence, error) {
	for _, member := range task.Members {
		if member.ResourceID == id {
			for _, e := range task.Evidence {
				if e.InstanceID == id {
					return member, e, nil
				}
			}
		}
	}
	return model.DatabaseInstance{}, model.RecoveryEvidence{}, fmt.Errorf("recovery member lacks frozen evidence")
}

func (d *disasterDriver) StartPrimary(ctx context.Context, task model.RecoveryTask) error {
	member, evidence, err := recoveryMember(task, task.PrimaryID)
	if err != nil {
		return err
	}
	if task.Engine == model.EngineMySQL {
		credentials, err := d.credential(ctx, task)
		if err != nil {
			return err
		}
		return d.mysql.StartPrimary(ctx, member, credentials.Administrative, evidence.GTIDExecuted, func() error { return d.authorize(ctx, task) })
	}
	for _, command := range []string{agent.CommandRecoveryGuard, agent.CommandRecoveryStart} {
		if _, err = d.send(ctx, task, member, agent.Request{Command: command, RecoveryTaskID: task.ResourceID, RecoveryFingerprint: evidence.Fingerprint}); err != nil {
			return err
		}
	}
	return nil
}

func (d *disasterDriver) verifyMySQLOffline(ctx context.Context, task model.RecoveryTask, member model.DatabaseInstance, credentials adapter.Credentials) error {
	if err := d.authorize(ctx, task); err != nil {
		return err
	}
	rows, err := d.mysql.Runner.Query(ctx, adapter.Endpoint{IPAddress: member.IPAddress, Hostname: member.Hostname, Port: member.Port}, credentials, "SELECT @@global.offline_mode AS guarded")
	if err != nil || len(rows) != 1 || rows[0]["guarded"] != "1" {
		return errors.Join(err, fmt.Errorf("MySQL business-access guard is not active"))
	}
	return nil
}

func (d *disasterDriver) Rebuild(ctx context.Context, task model.RecoveryTask, member model.DatabaseInstance) error {
	primary, _, err := recoveryMember(task, task.PrimaryID)
	if err != nil {
		return err
	}
	_, evidence, err := recoveryMember(task, member.ResourceID)
	if err != nil {
		return err
	}
	if task.Engine == model.EnginePostgreSQL {
		_, err = d.send(ctx, task, member, agent.Request{Command: agent.CommandRecoveryRebuild, RecoveryTaskID: task.ResourceID, RecoveryFingerprint: evidence.Fingerprint, SourceInstanceID: primary.ResourceID, SourceNodeID: model.ResourceID(primary.EngineIdentity["resource_id"]), SourceIPAddress: primary.IPAddress, SourceHostname: primary.Hostname, SourcePort: primary.Port})
		return err
	}
	if _, err = d.send(ctx, task, member, agent.Request{Command: agent.CommandRecoveryReplicaGuard, RecoveryTaskID: task.ResourceID}); err != nil {
		return err
	}
	credentials, err := d.credential(ctx, task)
	if err != nil {
		return err
	}
	err = d.mysql.Rebuild(ctx, member, primary, credentials, func() error { return d.authorize(ctx, task) }, func() error { return d.verifyMySQLOffline(ctx, task, member, credentials.Administrative) }, func() error {
		_, err := d.send(ctx, task, member, agent.Request{Command: agent.CommandRecoveryFencedStart, RecoveryTaskID: task.ResourceID})
		return err
	})
	if err != nil {
		return err
	}
	_, err = d.send(ctx, task, member, agent.Request{Command: agent.CommandRecoveryReplicaRelease, RecoveryTaskID: task.ResourceID})
	return err
}

func (d *disasterDriver) Verify(ctx context.Context, task model.RecoveryTask) (model.TopologySnapshot, error) {
	if err := d.authorize(ctx, task); err != nil {
		return model.TopologySnapshot{}, err
	}
	snapshot, err := d.refresher.Refresh(ctx, task.ClusterID)
	if err != nil {
		return snapshot, err
	}
	if task.Engine == model.EngineMySQL {
		primary, evidence, err := recoveryMember(task, task.PrimaryID)
		if err != nil {
			return snapshot, err
		}
		credentials, err := d.credential(ctx, task)
		if err != nil {
			return snapshot, err
		}
		guarded, err := d.mysql.VerifyGuardedPrimary(ctx, primary, credentials.Administrative, evidence.GTIDExecuted)
		if err != nil {
			return snapshot, err
		}
		for i, instance := range snapshot.Instances {
			if instance.ResourceID == task.PrimaryID {
				snapshot.Instances[i] = guarded
			}
		}
		for i, probe := range snapshot.Probes {
			if probe.InstanceID == task.PrimaryID && probe.Outcome == model.ProbeOutcomeReachable {
				snapshot.Probes[i].Health = guarded.Health
			}
		}
		snapshot.Links = nil
		for _, instance := range snapshot.Instances {
			if instance.ResourceID == task.PrimaryID {
				continue
			}
			if instance.Health.State != model.HealthHealthy || instance.Replication.SourceIdentity["server_uuid"] != primary.EngineIdentity["server_uuid"] || instance.Replication.IOThread != model.ThreadRunning || instance.Replication.SQLThread != model.ThreadRunning || instance.Replication.LagSeconds == nil || *instance.Replication.LagSeconds != 0 {
				return snapshot, fmt.Errorf("MySQL recovery replica is not fully caught up to the selected primary")
			}
			snapshot.Links = append(snapshot.Links, model.ReplicationLink{ClusterID: task.ClusterID, SourceInstanceID: task.PrimaryID, TargetInstanceID: instance.ResourceID, Healthy: true, LagSeconds: instance.Replication.LagSeconds})
		}
		snapshot.Health = model.Health{State: model.HealthHealthy, ObservedAt: snapshot.ObservedAt, Summary: "recovery topology verified behind the business write fence"}
	}
	return snapshot, nil
}

func (d *disasterDriver) Activate(ctx context.Context, task model.RecoveryTask) error {
	if err := d.authorize(ctx, task); err != nil {
		return err
	}
	current, found := d.repository.RecoveryTask(task.ResourceID)
	if !found || current.Stage != model.RecoveryCommitted || current.CommittedAt.IsZero() {
		return fmt.Errorf("business activation requires a durable Recovery Commit")
	}
	vip, err := d.vip(task)
	if err != nil {
		return err
	}
	_, err = d.leases.Acquire(ctx, endpoint.LeaseRequest{ClusterID: task.ClusterID, HAEndpointID: vip.ResourceID, OperationID: vip.ResourceID, OwnerID: task.PrimaryID, TTL: 30 * time.Second})
	return err
}

func (d *disasterDriver) Complete(ctx context.Context, task model.RecoveryTask) (model.RecoveryTask, error) {
	if err := d.verifyActivated(ctx, task); err != nil {
		return task, err
	}
	completed := task
	// VIP probes involve remote calls. Refresh again after them, and keep that
	// observation stable until strict validation and the Raft commit finish.
	_, err := d.refresher.RefreshAndCommit(ctx, task.ClusterID, func(model.TopologySnapshot) error {
		var commitErr error
		completed, commitErr = d.repository.CompleteRecovery(ctx, task)
		return commitErr
	})
	if err != nil {
		return task, err
	}
	return completed, nil
}

func (d *disasterDriver) verifyActivated(ctx context.Context, task model.RecoveryTask) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if err := d.Activate(ctx, task); err != nil {
			return err
		}
		snapshot, err := d.refresher.Refresh(ctx, task.ClusterID)
		ready := err == nil && snapshot.Health.State == model.HealthHealthy && len(snapshot.Instances) == len(task.Members) && len(snapshot.Links) == len(task.Members)-1
		owners := 0
		for _, member := range task.Members {
			state, stateErr := d.send(ctx, task, member, agent.Request{Command: agent.CommandVIPStatus})
			if stateErr != nil || state.OwnsVIP == nil {
				ready = false
				continue
			}
			if *state.OwnsVIP {
				owners++
				if member.ResourceID != task.PrimaryID {
					ready = false
				}
			}
		}
		if ready && owners == 1 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Recovery Commit persisted, but unique VIP ownership and normal healthy discovery did not converge")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

var _ disaster.Driver = (*disasterDriver)(nil)
