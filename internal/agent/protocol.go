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
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

const (
	CommandVIPStatus   = "vip_status"
	CommandVIPAcquire  = "vip_acquire"
	CommandVIPRelease  = "vip_release"
	CommandSelfIsolate = "self_isolate"
	CommandPersistRole = "persist_role"
	CommandRoleStatus  = "role_status"

	StatusOK      = "ok"
	StatusBlocked = "blocked"
	StatusError   = "error"
)

type Request struct {
	Command     string           `json:"command"`
	ClusterID   model.ResourceID `json:"cluster_id"`
	OperationID model.ResourceID `json:"operation_id"`
	LeaseID     model.ResourceID `json:"lease_id,omitempty"`
	PlanDigest  string           `json:"plan_digest"`
	ExpiresAt   time.Time        `json:"expires_at"`
	VIP         string           `json:"vip,omitempty"`
	Interface   string           `json:"interface,omitempty"`
	Prefix      int              `json:"prefix,omitempty"`
	ReadOnly    bool             `json:"read_only,omitempty"`
	Signature   string           `json:"signature"`
}

type Response struct {
	Status        string           `json:"status"`
	Message       string           `json:"message"`
	Error         string           `json:"error,omitempty"`
	OwnsVIP       *bool            `json:"owns_vip,omitempty"`
	ReadOnly      *bool            `json:"read_only,omitempty"`
	SuperReadOnly *bool            `json:"super_read_only,omitempty"`
	ClusterID     model.ResourceID `json:"cluster_id,omitempty"`
	InstanceID    model.ResourceID `json:"instance_id,omitempty"`
}

type unsignedRequest struct {
	Command     string           `json:"command"`
	ClusterID   model.ResourceID `json:"cluster_id"`
	OperationID model.ResourceID `json:"operation_id"`
	LeaseID     model.ResourceID `json:"lease_id,omitempty"`
	PlanDigest  string           `json:"plan_digest"`
	ExpiresAt   time.Time        `json:"expires_at"`
	VIP         string           `json:"vip,omitempty"`
	Interface   string           `json:"interface,omitempty"`
	Prefix      int              `json:"prefix,omitempty"`
	ReadOnly    bool             `json:"read_only,omitempty"`
}

func canonicalRequest(request Request) ([]byte, error) {
	return json.Marshal(unsignedRequest{
		Command: request.Command, ClusterID: request.ClusterID, OperationID: request.OperationID,
		LeaseID: request.LeaseID, PlanDigest: request.PlanDigest, ExpiresAt: request.ExpiresAt.UTC(),
		VIP: request.VIP, Interface: request.Interface, Prefix: request.Prefix, ReadOnly: request.ReadOnly,
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

type Service struct {
	configuration Config
	vip           VIPController
	roles         RoleController
	now           func() time.Time
}

func NewService(configuration Config, vip VIPController, roles RoleController, now func() time.Time) (*Service, error) {
	if strings.TrimSpace(configuration.SharedSecret) == "" || len(configuration.Clusters) == 0 || vip == nil || roles == nil {
		return nil, fmt.Errorf("agent service configuration is incomplete")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{configuration: configuration, vip: vip, roles: roles, now: now}, nil
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
	if request.Command == CommandVIPAcquire || request.Command == CommandVIPRelease || (request.Command == CommandPersistRole && !request.ReadOnly) {
		if !model.ValidResourceID(request.LeaseID) {
			return ClusterPolicy{}, blocked("an active lease UUID is required for this mutation"), false
		}
	}
	return policy, Response{}, true
}

func (service *Service) Handle(ctx context.Context, request Request) Response {
	policy, failure, valid := service.validate(request)
	if !valid {
		return failure
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
		roleErr := service.roles.PersistReadOnly(ctx, policy, true)
		err = errors.Join(releaseErr, roleErr)
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
	default:
		return blocked("agent command is unsupported")
	}
	if err != nil {
		response.Status = StatusError
		response.Message = "agent command failed"
		response.Error = publicAgentError(err)
	}
	return response
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
