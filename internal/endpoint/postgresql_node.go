package endpoint

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

// PostgreSQLNodeController translates platform operations into signed,
// allowlisted agent commands. Database credentials are never sent to agents.
type PostgreSQLNodeController struct {
	transport AgentTransport
	secret    string
	now       func() time.Time
}

func NewPostgreSQLNodeController(transport AgentTransport, secret string, now func() time.Time) *PostgreSQLNodeController {
	if now == nil {
		now = time.Now
	}
	return &PostgreSQLNodeController{transport: transport, secret: strings.TrimSpace(secret), now: now}
}

func (controller *PostgreSQLNodeController) Executable(context.Context) bool {
	return controller != nil && controller.transport != nil && controller.secret != ""
}

func (controller *PostgreSQLNodeController) request(
	resolved adapter.ResolvedOperation,
	command string,
	leaseID model.ResourceID,
	source *model.DatabaseInstance,
) (agent.Request, error) {
	if !model.ValidResourceID(resolved.OperationID) || resolved.Cluster.Engine != model.EnginePostgreSQL || !model.ValidResourceID(resolved.Cluster.ResourceID) {
		return agent.Request{}, fmt.Errorf("PostgreSQL operation scope is invalid")
	}
	digest := strings.TrimSpace(resolved.PlanDigest)
	if digest == "" {
		digest = "sha256:precheck:" + string(resolved.OperationID)
	}
	request := agent.Request{
		Command: command, Engine: model.EnginePostgreSQL,
		ClusterID: resolved.Cluster.ResourceID, OperationID: resolved.OperationID,
		LeaseID: leaseID, PlanDigest: digest, ExpiresAt: controller.now().UTC().Add(30 * time.Second),
	}
	if source != nil {
		sourceNodeID := postgresqlNativeNodeID(*source)
		if sourceNodeID == "" {
			return agent.Request{}, fmt.Errorf("PostgreSQL source native node identity is invalid")
		}
		request.SourceInstanceID = source.ResourceID
		request.SourceNodeID = sourceNodeID
		request.SourceHostname = source.Hostname
		request.SourceIPAddress = source.IPAddress
		request.SourcePort = source.Port
	}
	signature, err := agent.SignRequest(request, controller.secret)
	if err != nil {
		return agent.Request{}, fmt.Errorf("sign PostgreSQL agent request: %w", err)
	}
	request.Signature = signature
	return request, nil
}

func postgresqlNativeNodeID(instance model.DatabaseInstance) model.ResourceID {
	nodeID := model.ResourceID(strings.TrimSpace(instance.EngineIdentity["resource_id"]))
	if !model.ValidResourceID(nodeID) {
		return ""
	}
	return nodeID
}

func postgresqlDurablePlanDigest(value string) bool {
	const prefix = "sha256:"
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256HexLength {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == sha256HexLength/2
}

const sha256HexLength = 64

func postgresqlFrozenInstance(resolved adapter.ResolvedOperation, instance model.DatabaseInstance) bool {
	if instance.ClusterID != resolved.Cluster.ResourceID || instance.Engine != model.EnginePostgreSQL || !model.ValidResourceID(instance.ResourceID) {
		return false
	}
	for _, frozen := range resolved.Snapshot.Instances {
		if frozen.ResourceID != instance.ResourceID {
			continue
		}
		return frozen.ClusterID == instance.ClusterID && frozen.Engine == instance.Engine &&
			postgresqlNativeNodeID(frozen) != "" && postgresqlNativeNodeID(frozen) == postgresqlNativeNodeID(instance) &&
			strings.TrimSpace(frozen.EngineIdentity["system_identifier"]) == strings.TrimSpace(instance.EngineIdentity["system_identifier"]) &&
			strings.TrimSpace(frozen.Hostname) == strings.TrimSpace(instance.Hostname) &&
			strings.TrimSpace(frozen.IPAddress) == strings.TrimSpace(instance.IPAddress) &&
			frozen.Port == instance.Port && frozen.MetadataRevision == instance.MetadataRevision
	}
	return false
}

func (controller *PostgreSQLNodeController) send(
	ctx context.Context,
	resolved adapter.ResolvedOperation,
	instance model.DatabaseInstance,
	command string,
	leaseID model.ResourceID,
	source *model.DatabaseInstance,
) (agent.Response, error) {
	if !controller.Executable(ctx) {
		return agent.Response{}, fmt.Errorf("PostgreSQL node controller is not configured")
	}
	if !postgresqlFrozenInstance(resolved, instance) {
		return agent.Response{}, fmt.Errorf("PostgreSQL node is outside the operation scope")
	}
	if command != agent.CommandPostgreSQLStatus {
		if !model.ValidResourceID(leaseID) {
			return agent.Response{}, fmt.Errorf("active PostgreSQL mutation lease is required")
		}
		if !postgresqlDurablePlanDigest(resolved.PlanDigest) {
			return agent.Response{}, fmt.Errorf("durable PostgreSQL operation plan digest is required")
		}
	}
	if source != nil {
		if source.ResourceID == instance.ResourceID || !postgresqlFrozenInstance(resolved, *source) {
			return agent.Response{}, fmt.Errorf("PostgreSQL source is outside the frozen operation topology")
		}
	}
	request, err := controller.request(resolved, command, leaseID, source)
	if err != nil {
		return agent.Response{}, err
	}
	response, err := controller.transport.Send(ctx, instance, request)
	if err != nil {
		return agent.Response{}, fmt.Errorf("PostgreSQL agent transport: %w", err)
	}
	if response.Status != agent.StatusOK {
		detail := strings.TrimSpace(response.Error)
		if detail == "" {
			detail = strings.TrimSpace(response.Message)
		}
		if detail == "" {
			detail = "agent returned a non-ok status"
		}
		return agent.Response{}, fmt.Errorf("PostgreSQL agent rejected %s: %s", command, detail)
	}
	if response.ClusterID != resolved.Cluster.ResourceID || response.InstanceID != instance.ResourceID {
		return agent.Response{}, fmt.Errorf("PostgreSQL agent identity response does not match the requested node")
	}
	return response, nil
}

func (controller *PostgreSQLNodeController) status(ctx context.Context, resolved adapter.ResolvedOperation, instance model.DatabaseInstance) (bool, bool, error) {
	response, err := controller.send(ctx, resolved, instance, agent.CommandPostgreSQLStatus, "", nil)
	if err != nil {
		return false, false, err
	}
	if response.ServiceRunning == nil || response.InRecovery == nil {
		return false, false, fmt.Errorf("PostgreSQL agent returned incomplete service status")
	}
	return *response.ServiceRunning, *response.InRecovery, nil
}

func (controller *PostgreSQLNodeController) Status(ctx context.Context, resolved adapter.ResolvedOperation, instance model.DatabaseInstance) (bool, bool, error) {
	return controller.status(ctx, resolved, instance)
}

func (controller *PostgreSQLNodeController) Precheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	checks := make([]model.Check, 0, 2)
	for _, expected := range []struct {
		name       string
		instance   model.DatabaseInstance
		recovery   bool
		pass, fail string
	}{
		{name: "postgresql_primary_agent", instance: resolved.Primary, recovery: false, pass: "primary service is running outside recovery", fail: "primary agent cannot prove a running writable-role service"},
		{name: "postgresql_target_agent", instance: resolved.Target, recovery: true, pass: "target service is running in recovery", fail: "target agent cannot prove a running standby-role service"},
	} {
		running, inRecovery, err := controller.status(ctx, resolved, expected.instance)
		if err == nil && running && inRecovery == expected.recovery {
			checks = append(checks, model.Check{Name: expected.name, Status: model.CheckPass, Message: expected.pass})
		} else {
			checks = append(checks, model.Check{Name: expected.name, Status: model.CheckFail, Message: expected.fail})
		}
	}
	return checks
}

func (controller *PostgreSQLNodeController) Stop(ctx context.Context, resolved adapter.ResolvedOperation, instance model.DatabaseInstance, leaseID model.ResourceID) error {
	_, err := controller.send(ctx, resolved, instance, agent.CommandPostgreSQLStop, leaseID, nil)
	return err
}

func (controller *PostgreSQLNodeController) IsStopped(ctx context.Context, resolved adapter.ResolvedOperation, instance model.DatabaseInstance) (bool, error) {
	running, _, err := controller.status(ctx, resolved, instance)
	if err != nil {
		return false, err
	}
	return !running, nil
}

func (controller *PostgreSQLNodeController) Promote(ctx context.Context, resolved adapter.ResolvedOperation, instance model.DatabaseInstance, leaseID model.ResourceID) error {
	_, err := controller.send(ctx, resolved, instance, agent.CommandPostgreSQLPromote, leaseID, nil)
	return err
}

func (controller *PostgreSQLNodeController) Repoint(ctx context.Context, resolved adapter.ResolvedOperation, instance, source model.DatabaseInstance, leaseID model.ResourceID) error {
	_, err := controller.send(ctx, resolved, instance, agent.CommandPostgreSQLRepoint, leaseID, &source)
	return err
}

func (controller *PostgreSQLNodeController) Rewind(ctx context.Context, resolved adapter.ResolvedOperation, instance, source model.DatabaseInstance, leaseID model.ResourceID) error {
	_, err := controller.send(ctx, resolved, instance, agent.CommandPostgreSQLRewind, leaseID, &source)
	return err
}

func (controller *PostgreSQLNodeController) BaseBackup(ctx context.Context, resolved adapter.ResolvedOperation, instance, source model.DatabaseInstance, leaseID model.ResourceID) error {
	_, err := controller.send(ctx, resolved, instance, agent.CommandPostgreSQLBaseBackup, leaseID, &source)
	return err
}

func (controller *PostgreSQLNodeController) Start(ctx context.Context, resolved adapter.ResolvedOperation, instance model.DatabaseInstance, leaseID model.ResourceID) error {
	_, err := controller.send(ctx, resolved, instance, agent.CommandPostgreSQLStart, leaseID, nil)
	return err
}
