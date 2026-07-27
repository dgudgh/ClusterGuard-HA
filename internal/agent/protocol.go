package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

const (
	CommandVIPStatus              = "vip_status"
	CommandVIPAcquire             = "vip_acquire"
	CommandVIPRelease             = "vip_release"
	CommandSelfIsolate            = "self_isolate"
	CommandPersistRole            = "persist_role"
	CommandRoleStatus             = "role_status"
	CommandPostgreSQLStatus       = "postgresql_status"
	CommandPostgreSQLStop         = "postgresql_stop"
	CommandPostgreSQLStart        = "postgresql_start"
	CommandPostgreSQLPromote      = "postgresql_promote"
	CommandPostgreSQLRepoint      = "postgresql_repoint"
	CommandPostgreSQLRewind       = "postgresql_rewind"
	CommandPostgreSQLBaseBackup   = "postgresql_basebackup"
	CommandOracleBrokerDiscover   = "oracle_broker_discover"
	CommandOracleBrokerStatus     = "oracle_broker_status"
	CommandOracleBrokerSwitchover = "oracle_broker_switchover"

	StatusOK      = "ok"
	StatusBlocked = "blocked"
	StatusError   = "error"
)

type Request struct {
	Command          string           `json:"command"`
	Engine           model.Engine     `json:"engine,omitempty"`
	ClusterID        model.ResourceID `json:"cluster_id"`
	OperationID      model.ResourceID `json:"operation_id"`
	LeaseID          model.ResourceID `json:"lease_id,omitempty"`
	PlanDigest       string           `json:"plan_digest"`
	ExpiresAt        time.Time        `json:"expires_at"`
	VIP              string           `json:"vip,omitempty"`
	Interface        string           `json:"interface,omitempty"`
	Prefix           int              `json:"prefix,omitempty"`
	ReadOnly         bool             `json:"read_only,omitempty"`
	SourceInstanceID model.ResourceID `json:"source_instance_id,omitempty"`
	SourceNodeID     model.ResourceID `json:"source_node_id,omitempty"`
	SourceHostname   string           `json:"source_hostname,omitempty"`
	SourceIPAddress  string           `json:"source_ip_address,omitempty"`
	SourcePort       int              `json:"source_port,omitempty"`
	OracleTarget     string           `json:"oracle_target,omitempty"`
	Signature        string           `json:"signature"`
}

type Response struct {
	Status                    string           `json:"status"`
	Message                   string           `json:"message"`
	Error                     string           `json:"error,omitempty"`
	OwnsVIP                   *bool            `json:"owns_vip,omitempty"`
	ReadOnly                  *bool            `json:"read_only,omitempty"`
	SuperReadOnly             *bool            `json:"super_read_only,omitempty"`
	ServiceRunning            *bool            `json:"service_running,omitempty"`
	InRecovery                *bool            `json:"in_recovery,omitempty"`
	ClusterID                 model.ResourceID `json:"cluster_id,omitempty"`
	InstanceID                model.ResourceID `json:"instance_id,omitempty"`
	OracleDBID                string           `json:"oracle_dbid,omitempty"`
	OracleDatabase            string           `json:"oracle_database,omitempty"`
	OracleInstanceName        string           `json:"oracle_instance_name,omitempty"`
	OracleRole                string           `json:"oracle_role,omitempty"`
	OracleDatabaseStatus      string           `json:"oracle_database_status,omitempty"`
	OracleConfigurationStatus string           `json:"oracle_configuration_status,omitempty"`
	OracleBrokerEnabled       bool             `json:"oracle_broker_enabled,omitempty"`
	OracleReadyForSwitchover  *bool            `json:"oracle_ready_for_switchover,omitempty"`
	OracleTransportLagSeconds *int64           `json:"oracle_transport_lag_seconds,omitempty"`
	OracleApplyLagSeconds     *int64           `json:"oracle_apply_lag_seconds,omitempty"`
}

type unsignedRequest struct {
	Command          string           `json:"command"`
	Engine           model.Engine     `json:"engine,omitempty"`
	ClusterID        model.ResourceID `json:"cluster_id"`
	OperationID      model.ResourceID `json:"operation_id"`
	LeaseID          model.ResourceID `json:"lease_id,omitempty"`
	PlanDigest       string           `json:"plan_digest"`
	ExpiresAt        time.Time        `json:"expires_at"`
	VIP              string           `json:"vip,omitempty"`
	Interface        string           `json:"interface,omitempty"`
	Prefix           int              `json:"prefix,omitempty"`
	ReadOnly         bool             `json:"read_only,omitempty"`
	SourceInstanceID model.ResourceID `json:"source_instance_id,omitempty"`
	SourceNodeID     model.ResourceID `json:"source_node_id,omitempty"`
	SourceHostname   string           `json:"source_hostname,omitempty"`
	SourceIPAddress  string           `json:"source_ip_address,omitempty"`
	SourcePort       int              `json:"source_port,omitempty"`
	OracleTarget     string           `json:"oracle_target,omitempty"`
}

func canonicalRequest(request Request) ([]byte, error) {
	return json.Marshal(unsignedRequest{
		Command: request.Command, ClusterID: request.ClusterID, OperationID: request.OperationID,
		LeaseID: request.LeaseID, PlanDigest: request.PlanDigest, ExpiresAt: request.ExpiresAt.UTC(),
		Engine: request.Engine, VIP: request.VIP, Interface: request.Interface, Prefix: request.Prefix, ReadOnly: request.ReadOnly,
		SourceInstanceID: request.SourceInstanceID, SourceNodeID: request.SourceNodeID,
		SourceHostname: request.SourceHostname, SourceIPAddress: request.SourceIPAddress, SourcePort: request.SourcePort,
		OracleTarget: request.OracleTarget,
	})
}

func SignRequest(request Request, secret string) (string, error) {
	contents, err := canonicalRequest(request)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(contents)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

type VIPController interface {
	Status(context.Context, ClusterPolicy) (bool, error)
	Acquire(context.Context, ClusterPolicy) error
	Release(context.Context, ClusterPolicy) error
}

type RoleController interface {
	PersistReadOnly(context.Context, ClusterPolicy, bool) error
	Status(context.Context, ClusterPolicy) (bool, bool, error)
}

type PostgreSQLController interface {
	Status(context.Context, ClusterPolicy) (bool, bool, error)
	Stop(context.Context, ClusterPolicy) error
	Start(context.Context, ClusterPolicy) error
	Promote(context.Context, ClusterPolicy) error
	Repoint(context.Context, ClusterPolicy, PostgreSQLPeer) error
	Rewind(context.Context, ClusterPolicy, PostgreSQLPeer) error
	BaseBackup(context.Context, ClusterPolicy, PostgreSQLPeer) error
}

type OracleController interface {
	Discover(context.Context, ClusterPolicy) (OracleBrokerStatus, error)
	Status(context.Context, ClusterPolicy, string) (OracleBrokerStatus, error)
	Switchover(context.Context, ClusterPolicy, string) error
}

type ServiceOption func(*Service)

func WithPostgreSQLController(controller PostgreSQLController) ServiceOption {
	return func(service *Service) { service.postgresql = controller }
}

func WithOracleController(controller OracleController) ServiceOption {
	return func(service *Service) { service.oracle = controller }
}

func WithMutationLedger(ledger MutationLedger) ServiceOption {
	return func(service *Service) { service.mutations = ledger }
}

type Service struct {
	configuration Config
	vip           VIPController
	roles         RoleController
	postgresql    PostgreSQLController
	oracle        OracleController
	mutations     MutationLedger
	now           func() time.Time
}

func NewService(configuration Config, vip VIPController, roles RoleController, now func() time.Time, options ...ServiceOption) (*Service, error) {
	if strings.TrimSpace(configuration.SharedSecret) == "" || len(configuration.Clusters) == 0 || vip == nil || roles == nil {
		return nil, fmt.Errorf("agent service configuration is incomplete")
	}
	if now == nil {
		now = time.Now
	}
	service := &Service{configuration: configuration, vip: vip, roles: roles, now: now}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	for _, policy := range configuration.Clusters {
		if policy.Engine == model.EnginePostgreSQL && service.postgresql == nil {
			return nil, fmt.Errorf("PostgreSQL agent controller is required by cluster policy")
		}
		if policy.Engine == model.EngineOracle && service.oracle == nil {
			return nil, fmt.Errorf("Oracle agent controller is required by cluster policy")
		}
	}
	return service, nil
}

func blocked(message string) Response { return Response{Status: StatusBlocked, Message: message} }

func (service *Service) validate(request Request) (ClusterPolicy, Response, bool) {
	policy, found := service.configuration.Clusters[request.ClusterID]
	if !found {
		return ClusterPolicy{}, blocked("cluster is outside the agent allowlist"), false
	}
	if !model.ValidResourceID(request.OperationID) || !strings.HasPrefix(request.PlanDigest, "sha256:") {
		return ClusterPolicy{}, blocked("signed operation UUID and plan digest are required"), false
	}
	if agentMutationCommand(request.Command) && !cryptographicPlanDigest(request.PlanDigest) {
		return ClusterPolicy{}, blocked("a complete cryptographic plan digest is required for agent mutations"), false
	}
	policyEngine := policy.Engine
	if policyEngine == "" {
		policyEngine = model.EngineMySQL
	}
	requestEngine := request.Engine
	if requestEngine == "" {
		requestEngine = model.EngineMySQL
	}
	if requestEngine != policyEngine {
		return ClusterPolicy{}, blocked("database engine is outside the agent allowlist"), false
	}
	now := service.now().UTC()
	if request.ExpiresAt.IsZero() || !request.ExpiresAt.After(now) || request.ExpiresAt.After(now.Add(5*time.Minute)) {
		return ClusterPolicy{}, blocked("agent request is expired or outside the allowed time window"), false
	}
	expected, err := SignRequest(request, service.configuration.SharedSecret)
	if err != nil || subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(request.Signature))) != 1 {
		return ClusterPolicy{}, blocked("agent request signature is invalid"), false
	}
	if request.VIP != "" || request.Interface != "" || request.Prefix != 0 {
		if request.VIP != policy.VIP || request.Interface != policy.Interface || request.Prefix != policy.Prefix {
			return ClusterPolicy{}, blocked("VIP request is outside the agent allowlist"), false
		}
	}
	if request.Command == CommandVIPAcquire || request.Command == CommandVIPRelease || (request.Command == CommandPersistRole && !request.ReadOnly) || postgresqlMutationCommand(request.Command) {
		if !model.ValidResourceID(request.LeaseID) {
			return ClusterPolicy{}, blocked("an active lease UUID is required for this mutation"), false
		}
	}
	if request.Command == CommandOracleBrokerSwitchover {
		if !model.ValidResourceID(request.LeaseID) {
			return ClusterPolicy{}, blocked("an active lease UUID is required for this mutation"), false
		}
	}
	if postgresqlSourceCommand(request.Command) {
		if _, found := postgresqlSource(policy, request); !found {
			return ClusterPolicy{}, blocked("PostgreSQL source is outside the agent allowlist"), false
		}
	}
	if request.Command == CommandOracleBrokerDiscover && strings.TrimSpace(request.OracleTarget) != "" {
		return ClusterPolicy{}, blocked("Oracle discovery does not accept a target database"), false
	}
	if request.Command == CommandOracleBrokerStatus || request.Command == CommandOracleBrokerSwitchover {
		if !oracleTargetAllowed(policy, request.OracleTarget) {
			return ClusterPolicy{}, blocked("Oracle target is outside the agent allowlist"), false
		}
	}
	return policy, Response{}, true
}

func cryptographicPlanDigest(value string) bool {
	const prefix = "sha256:"
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(digest) == sha256.Size
}

func postgresqlMutationCommand(command string) bool {
	switch command {
	case CommandPostgreSQLStop, CommandPostgreSQLStart, CommandPostgreSQLPromote, CommandPostgreSQLRepoint, CommandPostgreSQLRewind, CommandPostgreSQLBaseBackup:
		return true
	default:
		return false
	}
}

func agentMutationCommand(command string) bool {
	switch command {
	case CommandVIPAcquire, CommandVIPRelease, CommandSelfIsolate, CommandPersistRole, CommandOracleBrokerSwitchover:
		return true
	default:
		return postgresqlMutationCommand(command)
	}
}

func oracleTargetAllowed(policy ClusterPolicy, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" || strings.EqualFold(target, policy.OracleDatabaseUniqueName) {
		return false
	}
	for _, member := range policy.OracleMembers {
		if strings.EqualFold(strings.TrimSpace(member), target) {
			return true
		}
	}
	return false
}

func postgresqlSourceCommand(command string) bool {
	return command == CommandPostgreSQLRepoint || command == CommandPostgreSQLRewind || command == CommandPostgreSQLBaseBackup
}

func postgresqlSource(policy ClusterPolicy, request Request) (PostgreSQLPeer, bool) {
	for _, peer := range policy.PostgreSQLPeers {
		if peer.InstanceID == request.SourceInstanceID && peer.NodeID == request.SourceNodeID && strings.EqualFold(strings.TrimSpace(peer.Hostname), strings.TrimSpace(request.SourceHostname)) &&
			strings.TrimSpace(peer.IPAddress) == strings.TrimSpace(request.SourceIPAddress) && peer.Port == request.SourcePort {
			return peer, true
		}
	}
	peer := PostgreSQLPeer{
		InstanceID: request.SourceInstanceID,
		NodeID:     request.SourceNodeID,
		Hostname:   strings.TrimSpace(request.SourceHostname),
		IPAddress:  strings.TrimSpace(request.SourceIPAddress),
		Port:       request.SourcePort,
	}
	if !model.ValidResourceID(peer.InstanceID) || peer.InstanceID == policy.InstanceID ||
		!model.ValidResourceID(peer.NodeID) || peer.NodeID == policy.PostgreSQLNodeID ||
		peer.Port < 1 || peer.Port > 65535 || (peer.Hostname == "" && peer.IPAddress == "") {
		return PostgreSQLPeer{}, false
	}
	if peer.Hostname != "" && !hostnamePattern.MatchString(peer.Hostname) {
		return PostgreSQLPeer{}, false
	}
	if peer.IPAddress != "" && net.ParseIP(peer.IPAddress) == nil {
		return PostgreSQLPeer{}, false
	}
	return peer, true
}

func (service *Service) Handle(ctx context.Context, request Request) Response {
	policy, failure, valid := service.validate(request)
	if !valid {
		return failure
	}
	mutating := agentMutationCommand(request.Command)
	if mutating {
		if service.mutations == nil {
			return blocked("durable agent mutation ledger is not configured")
		}
		replayed, found, err := service.mutations.Begin(request, policy)
		if err != nil {
			return blocked("agent mutation blocked: " + publicAgentError(err))
		}
		if found {
			return replayed
		}
	}
	response := Response{Status: StatusOK, ClusterID: policy.ClusterID, InstanceID: policy.InstanceID}
	var err error
	switch request.Command {
	case CommandVIPStatus:
		var owns bool
		owns, err = service.vip.Status(ctx, policy)
		response.OwnsVIP = &owns
		response.Message = "VIP status collected"
	case CommandVIPAcquire:
		err = service.vip.Acquire(ctx, policy)
		response.Message = "VIP acquired"
	case CommandVIPRelease:
		err = service.vip.Release(ctx, policy)
		response.Message = "VIP released"
	case CommandSelfIsolate:
		releaseErr := service.vip.Release(ctx, policy)
		if policy.Engine == model.EnginePostgreSQL {
			err = errors.Join(releaseErr, service.postgresql.Stop(ctx, policy))
		} else {
			err = errors.Join(releaseErr, service.roles.PersistReadOnly(ctx, policy, true))
		}
		response.Message = "instance self-isolated"
	case CommandPersistRole:
		err = service.roles.PersistReadOnly(ctx, policy, request.ReadOnly)
		response.Message = "MySQL role persisted"
	case CommandRoleStatus:
		var readOnly, superReadOnly bool
		readOnly, superReadOnly, err = service.roles.Status(ctx, policy)
		response.ReadOnly = &readOnly
		response.SuperReadOnly = &superReadOnly
		response.Message = "MySQL role status collected"
	case CommandPostgreSQLStatus:
		var running, inRecovery bool
		running, inRecovery, err = service.postgresql.Status(ctx, policy)
		response.ServiceRunning = &running
		response.InRecovery = &inRecovery
		response.Message = "PostgreSQL service status collected"
	case CommandPostgreSQLStop:
		err = service.postgresql.Stop(ctx, policy)
		response.Message = "PostgreSQL service stopped"
	case CommandPostgreSQLStart:
		err = service.postgresql.Start(ctx, policy)
		response.Message = "PostgreSQL service started"
	case CommandPostgreSQLPromote:
		err = service.postgresql.Promote(ctx, policy)
		response.Message = "PostgreSQL standby promoted"
	case CommandPostgreSQLRepoint:
		source, _ := postgresqlSource(policy, request)
		err = service.postgresql.Repoint(ctx, policy, source)
		response.Message = "PostgreSQL standby source updated"
	case CommandPostgreSQLRewind:
		source, _ := postgresqlSource(policy, request)
		err = service.postgresql.Rewind(ctx, policy, source)
		response.Message = "PostgreSQL former primary rewound"
	case CommandPostgreSQLBaseBackup:
		source, _ := postgresqlSource(policy, request)
		err = service.postgresql.BaseBackup(ctx, policy, source)
		response.Message = "PostgreSQL base backup completed"
	case CommandOracleBrokerDiscover:
		var status OracleBrokerStatus
		status, err = service.oracle.Discover(ctx, policy)
		setOracleResponse(&response, status)
		response.Message = "Oracle Data Guard Broker discovery collected"
	case CommandOracleBrokerStatus:
		var status OracleBrokerStatus
		status, err = service.oracle.Status(ctx, policy, request.OracleTarget)
		setOracleResponse(&response, status)
		response.Message = "Oracle Data Guard Broker status collected"
	case CommandOracleBrokerSwitchover:
		err = service.oracle.Switchover(ctx, policy, request.OracleTarget)
		response.Message = "Oracle Data Guard Broker switchover completed"
	default:
		return blocked("agent command is unsupported")
	}
	if err != nil {
		response.Status = StatusError
		response.Message = "agent command failed"
		response.Error = publicAgentError(err)
	}
	if mutating {
		if err := service.mutations.Complete(request, policy, response); err != nil {
			return Response{
				Status: StatusError, Message: "agent mutation completed but its durable receipt is unavailable",
				Error: publicAgentError(err), ClusterID: policy.ClusterID, InstanceID: policy.InstanceID,
			}
		}
	}
	return response
}

func setOracleResponse(response *Response, status OracleBrokerStatus) {
	response.OracleDBID = status.DBID
	response.OracleDatabase = status.Database
	response.OracleInstanceName = status.InstanceName
	response.OracleRole = status.Role
	response.OracleDatabaseStatus = status.DatabaseStatus
	response.OracleConfigurationStatus = status.ConfigurationStatus
	response.OracleBrokerEnabled = status.BrokerEnabled
	response.OracleReadyForSwitchover = &status.ReadyForSwitchover
	response.OracleTransportLagSeconds = status.TransportLagSeconds
	response.OracleApplyLagSeconds = status.ApplyLagSeconds
}

func publicAgentError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}
