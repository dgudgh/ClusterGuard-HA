package endpoint

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	oracleadapter "clusterguard.io/ha/adapters/oracle"
	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type OracleBrokerController struct {
	transport AgentTransport
	secret    string
	now       func() time.Time
}

func NewOracleBrokerController(transport AgentTransport, secret string, now func() time.Time) *OracleBrokerController {
	if now == nil {
		now = time.Now
	}
	return &OracleBrokerController{transport: transport, secret: strings.TrimSpace(secret), now: now}
}

func (controller *OracleBrokerController) Executable(context.Context) bool {
	return controller != nil && controller.transport != nil && controller.secret != ""
}

func oracleDurablePlanDigest(value string) bool {
	const prefix = "sha256:"
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == 32
}

func oracleFrozenInstance(resolved adapter.ResolvedOperation, instance model.DatabaseInstance) bool {
	if instance.ClusterID != resolved.Cluster.ResourceID || instance.Engine != model.EngineOracle ||
		!model.ValidResourceID(instance.ResourceID) {
		return false
	}
	for _, frozen := range resolved.Snapshot.Instances {
		if frozen.ResourceID != instance.ResourceID {
			continue
		}
		return frozen.ClusterID == instance.ClusterID && frozen.Engine == instance.Engine &&
			strings.TrimSpace(frozen.EngineIdentity["dbid"]) != "" &&
			strings.TrimSpace(frozen.EngineIdentity["dbid"]) == strings.TrimSpace(instance.EngineIdentity["dbid"]) &&
			strings.EqualFold(strings.TrimSpace(frozen.EngineIdentity["db_unique_name"]), strings.TrimSpace(instance.EngineIdentity["db_unique_name"])) &&
			strings.TrimSpace(frozen.Hostname) == strings.TrimSpace(instance.Hostname) &&
			strings.TrimSpace(frozen.IPAddress) == strings.TrimSpace(instance.IPAddress) &&
			frozen.Port == instance.Port && frozen.MetadataRevision == instance.MetadataRevision
	}
	return false
}

func oracleDatabaseName(instance model.DatabaseInstance) string {
	return strings.TrimSpace(instance.EngineIdentity["db_unique_name"])
}

func (controller *OracleBrokerController) request(
	resolved adapter.ResolvedOperation,
	command string,
	target string,
	leaseID model.ResourceID,
) (agent.Request, error) {
	if !model.ValidResourceID(resolved.OperationID) || resolved.Cluster.Engine != model.EngineOracle ||
		!model.ValidResourceID(resolved.Cluster.ResourceID) || strings.TrimSpace(target) == "" {
		return agent.Request{}, fmt.Errorf("Oracle operation scope is invalid")
	}
	digest := strings.TrimSpace(resolved.PlanDigest)
	if digest == "" {
		digest = "sha256:precheck:" + string(resolved.OperationID)
	}
	request := agent.Request{
		Command: command, Engine: model.EngineOracle,
		ClusterID: resolved.Cluster.ResourceID, OperationID: resolved.OperationID,
		LeaseID: leaseID, PlanDigest: digest, ExpiresAt: controller.now().UTC().Add(30 * time.Second),
		OracleTarget: target,
	}
	signature, err := agent.SignRequest(request, controller.secret)
	if err != nil {
		return agent.Request{}, fmt.Errorf("sign Oracle agent request: %w", err)
	}
	request.Signature = signature
	return request, nil
}

func (controller *OracleBrokerController) send(
	ctx context.Context,
	resolved adapter.ResolvedOperation,
	instance model.DatabaseInstance,
	command string,
	target string,
	leaseID model.ResourceID,
) (agent.Response, error) {
	if !controller.Executable(ctx) {
		return agent.Response{}, fmt.Errorf("Oracle Broker controller is not configured")
	}
	if !oracleFrozenInstance(resolved, instance) {
		return agent.Response{}, fmt.Errorf("Oracle node is outside the frozen operation scope")
	}
	if command == agent.CommandOracleBrokerSwitchover {
		if !model.ValidResourceID(leaseID) {
			return agent.Response{}, fmt.Errorf("active Oracle mutation lease is required")
		}
		if !oracleDurablePlanDigest(resolved.PlanDigest) {
			return agent.Response{}, fmt.Errorf("durable Oracle operation plan digest is required")
		}
	}
	request, err := controller.request(resolved, command, target, leaseID)
	if err != nil {
		return agent.Response{}, err
	}
	response, err := controller.transport.Send(ctx, instance, request)
	if err != nil {
		return agent.Response{}, fmt.Errorf("Oracle agent transport: %w", err)
	}
	if response.Status != agent.StatusOK {
		detail := strings.TrimSpace(response.Error)
		if detail == "" {
			detail = strings.TrimSpace(response.Message)
		}
		if detail == "" {
			detail = "agent returned a non-ok status"
		}
		return agent.Response{}, fmt.Errorf("Oracle agent rejected %s: %s", command, detail)
	}
	if response.ClusterID != resolved.Cluster.ResourceID || response.InstanceID != instance.ResourceID {
		return agent.Response{}, fmt.Errorf("Oracle agent identity response does not match the requested node")
	}
	return response, nil
}

func (controller *OracleBrokerController) Status(
	ctx context.Context,
	resolved adapter.ResolvedOperation,
	instance model.DatabaseInstance,
	target string,
) (oracleadapter.BrokerControlStatus, error) {
	response, err := controller.send(ctx, resolved, instance, agent.CommandOracleBrokerStatus, target, "")
	if err != nil {
		return oracleadapter.BrokerControlStatus{}, err
	}
	if !strings.EqualFold(response.OracleDatabase, oracleDatabaseName(instance)) ||
		strings.TrimSpace(response.OracleRole) == "" || strings.TrimSpace(response.OracleConfigurationStatus) == "" ||
		response.OracleReadyForSwitchover == nil {
		return oracleadapter.BrokerControlStatus{}, fmt.Errorf("Oracle agent returned incomplete or mismatched Broker evidence")
	}
	return oracleadapter.BrokerControlStatus{
		Database: response.OracleDatabase, Role: response.OracleRole,
		ConfigurationStatus: response.OracleConfigurationStatus,
		ReadyForSwitchover:  *response.OracleReadyForSwitchover,
		TransportLagSeconds: response.OracleTransportLagSeconds,
		ApplyLagSeconds:     response.OracleApplyLagSeconds,
	}, nil
}

func (controller *OracleBrokerController) Precheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	status, err := controller.Status(ctx, resolved, resolved.Primary, oracleDatabaseName(resolved.Target))
	checks := []model.Check{
		oracleControllerCheck("oracle_primary_agent", err == nil && strings.EqualFold(status.Role, "PRIMARY"),
			"current primary Broker role is verified", "current primary Broker role could not be verified"),
		oracleControllerCheck("oracle_broker_success", err == nil && strings.HasPrefix(strings.ToUpper(status.ConfigurationStatus), "SUCCESS"),
			"Data Guard Broker configuration is healthy", "Data Guard Broker configuration is not healthy"),
		oracleControllerCheck("oracle_target_ready", err == nil && status.ReadyForSwitchover,
			"target standby is ready for switchover", "target standby is not ready for switchover"),
		oracleBrokerLagCheck(status, err),
	}
	return checks
}

func (controller *OracleBrokerController) Switchover(ctx context.Context, resolved adapter.ResolvedOperation, leaseID model.ResourceID) error {
	_, err := controller.send(
		ctx, resolved, resolved.Primary, agent.CommandOracleBrokerSwitchover,
		oracleDatabaseName(resolved.Target), leaseID,
	)
	return err
}

func oracleControllerCheck(name string, passed bool, success, failure string) model.Check {
	if passed {
		return model.Check{Name: name, Status: model.CheckPass, Message: success}
	}
	return model.Check{Name: name, Status: model.CheckFail, Message: failure}
}

func oracleBrokerLagCheck(status oracleadapter.BrokerControlStatus, statusErr error) model.Check {
	check := model.Check{Name: "oracle_target_zero_lag"}
	if statusErr != nil || status.TransportLagSeconds == nil || status.ApplyLagSeconds == nil ||
		*status.TransportLagSeconds < 0 || *status.ApplyLagSeconds < 0 {
		check.Status = model.CheckFail
		check.Message = "target standby lag is unavailable or invalid"
		return check
	}
	if *status.TransportLagSeconds == 0 && *status.ApplyLagSeconds == 0 {
		check.Status = model.CheckPass
		check.Message = "target standby transport and apply lag are zero"
		return check
	}
	if status.ReadyForSwitchover {
		check.Status = model.CheckWarn
		check.Message = fmt.Sprintf(
			"target standby has transient lag (transport=%ds, apply=%ds); Data Guard Broker will synchronize before switchover",
			*status.TransportLagSeconds,
			*status.ApplyLagSeconds,
		)
		return check
	}
	check.Status = model.CheckFail
	check.Message = "target standby has non-zero lag and Data Guard Broker has not declared it ready for switchover"
	return check
}

var _ oracleadapter.BrokerController = (*OracleBrokerController)(nil)
