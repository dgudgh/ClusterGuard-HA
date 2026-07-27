package endpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	oracleadapter "clusterguard.io/ha/adapters/oracle"
	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type OracleBrokerDiscoveryProvider struct {
	transport AgentTransport
	secret    string
	now       func() time.Time
}

func NewOracleBrokerDiscoveryProvider(transport AgentTransport, secret string, now func() time.Time) *OracleBrokerDiscoveryProvider {
	if now == nil {
		now = time.Now
	}
	return &OracleBrokerDiscoveryProvider{transport: transport, secret: strings.TrimSpace(secret), now: now}
}

func (provider *OracleBrokerDiscoveryProvider) Executable(context.Context) bool {
	return provider != nil && provider.transport != nil && provider.secret != ""
}

func (provider *OracleBrokerDiscoveryProvider) Discover(
	ctx context.Context,
	request adapter.DiscoverRequest,
) (adapter.DiscoveryResult, error) {
	if !provider.Executable(ctx) {
		return adapter.DiscoveryResult{}, adapter.ErrUnsupported
	}
	if !model.ValidResourceID(request.ClusterID) || request.Endpoint.Port < 1 || request.Endpoint.Port > 65535 ||
		(strings.TrimSpace(request.Endpoint.Hostname) == "" && strings.TrimSpace(request.Endpoint.IPAddress) == "") {
		return adapter.DiscoveryResult{}, fmt.Errorf("Oracle discovery endpoint scope is invalid")
	}
	operationID := model.NewResourceID()
	scope := strings.Join([]string{
		string(request.ClusterID), request.Endpoint.Hostname, request.Endpoint.IPAddress,
		fmt.Sprintf("%d", request.Endpoint.Port), string(operationID),
	}, "\x00")
	digest := sha256.Sum256([]byte(scope))
	signed := agent.Request{
		Command: agent.CommandOracleBrokerDiscover, Engine: model.EngineOracle,
		ClusterID: request.ClusterID, OperationID: operationID,
		PlanDigest: "sha256:" + hex.EncodeToString(digest[:]),
		ExpiresAt:  provider.now().UTC().Add(30 * time.Second),
	}
	signature, err := agent.SignRequest(signed, provider.secret)
	if err != nil {
		return adapter.DiscoveryResult{}, fmt.Errorf("sign Oracle discovery request: %w", err)
	}
	signed.Signature = signature
	target := model.DatabaseInstance{
		ClusterID: request.ClusterID, Engine: model.EngineOracle,
		Hostname: request.Endpoint.Hostname, IPAddress: request.Endpoint.IPAddress, Port: request.Endpoint.Port,
	}
	response, err := provider.transport.Send(ctx, target, signed)
	if err != nil {
		return adapter.DiscoveryResult{}, fmt.Errorf("Oracle discovery agent transport: %w", err)
	}
	if response.Status != agent.StatusOK {
		detail := strings.TrimSpace(response.Error)
		if detail == "" {
			detail = strings.TrimSpace(response.Message)
		}
		if detail == "" {
			detail = "agent returned a non-ok status"
		}
		return adapter.DiscoveryResult{}, fmt.Errorf("Oracle discovery agent rejected request: %s", detail)
	}
	if response.ClusterID != request.ClusterID || !model.ValidResourceID(response.InstanceID) ||
		strings.TrimSpace(response.OracleDBID) == "" || strings.TrimSpace(response.OracleDatabase) == "" ||
		strings.TrimSpace(response.OracleInstanceName) == "" || strings.TrimSpace(response.OracleRole) == "" ||
		strings.TrimSpace(response.OracleConfigurationStatus) == "" || strings.TrimSpace(response.OracleDatabaseStatus) == "" {
		return adapter.DiscoveryResult{}, fmt.Errorf("Oracle discovery agent returned incomplete or mismatched identity evidence")
	}

	role := oracleDiscoveryRole(response.OracleRole)
	brokerHealthy := response.OracleBrokerEnabled &&
		strings.HasPrefix(strings.ToUpper(strings.TrimSpace(response.OracleConfigurationStatus)), "SUCCESS") &&
		strings.HasPrefix(strings.ToUpper(strings.TrimSpace(response.OracleDatabaseStatus)), "SUCCESS") &&
		role != model.RoleUnknown
	healthState := model.HealthDegraded
	summary := "Oracle Data Guard Broker state requires attention"
	if brokerHealthy {
		healthState = model.HealthHealthy
		summary = "Oracle Data Guard Broker member is healthy"
	}
	lag := maximumOracleLag(response.OracleTransportLagSeconds, response.OracleApplyLagSeconds)
	threadState := model.ThreadUnknown
	if role == model.RoleStandby && brokerHealthy && lag != nil {
		threadState = model.ThreadRunning
	}
	brokerReady := response.OracleReadyForSwitchover != nil && *response.OracleReadyForSwitchover
	promotionEligible := role == model.RoleStandby && brokerHealthy && lag != nil && (*lag == 0 || brokerReady)
	instance := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: response.InstanceID},
		ClusterID:    request.ClusterID,
		Engine:       model.EngineOracle,
		EngineIdentity: model.EngineIdentity{
			"dbid": response.OracleDBID, "db_unique_name": response.OracleDatabase,
			"instance_name": response.OracleInstanceName,
		},
		DisplayName: response.OracleDatabase,
		Hostname:    request.Endpoint.Hostname,
		IPAddress:   request.Endpoint.IPAddress,
		Port:        request.Endpoint.Port,
		Role:        role,
		Health: model.Health{
			State: healthState, Summary: summary, ObservedAt: provider.now().UTC(),
			Replication: strings.ToLower(strings.TrimSpace(response.OracleDatabaseStatus)),
		},
		Replication: model.ReplicationStatus{
			IOThread: threadState, SQLThread: threadState, LagSeconds: lag,
		},
		PromotionEligible: promotionEligible,
		EngineMetadata: map[string]string{
			"data_guard_broker":     enabledValue(response.OracleBrokerEnabled),
			"dgbroker":              enabledValue(response.OracleBrokerEnabled),
			"db_unique_name":        response.OracleDatabase,
			"database_role":         response.OracleRole,
			"database_status":       response.OracleDatabaseStatus,
			"configuration_status":  response.OracleConfigurationStatus,
			"transport_lag_seconds": optionalOracleLag(response.OracleTransportLagSeconds),
			"apply_lag_seconds":     optionalOracleLag(response.OracleApplyLagSeconds),
		},
	}
	return adapter.DiscoveryResult{Instance: instance}, nil
}

func oracleDiscoveryRole(value string) model.InstanceRole {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "PRIMARY":
		return model.RolePrimary
	case "PHYSICAL STANDBY", "LOGICAL STANDBY", "SNAPSHOT STANDBY", "STANDBY":
		return model.RoleStandby
	default:
		return model.RoleUnknown
	}
}

func maximumOracleLag(left, right *int64) *int64 {
	if left == nil && right == nil {
		return nil
	}
	value := int64(0)
	if left != nil && *left > value {
		value = *left
	}
	if right != nil && *right > value {
		value = *right
	}
	return &value
}

func optionalOracleLag(value *int64) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *value)
}

func enabledValue(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

var _ oracleadapter.BrokerDiscoveryProvider = (*OracleBrokerDiscoveryProvider)(nil)
