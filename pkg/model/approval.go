package model

import "time"

type ApprovalGrantStatus string

const (
	ApprovalGrantActive   ApprovalGrantStatus = "active"
	ApprovalGrantConsumed ApprovalGrantStatus = "consumed"
	ApprovalGrantExpired  ApprovalGrantStatus = "expired"
	ApprovalGrantRevoked  ApprovalGrantStatus = "revoked"
)

type ApprovalGrant struct {
	ResourceMeta
	TokenHash             string              `json:"token_hash"`
	OperationID           ResourceID          `json:"operation_id"`
	ClusterID             ResourceID          `json:"cluster_id"`
	Engine                Engine              `json:"engine"`
	OperationKind         OperationKind       `json:"operation_kind"`
	TargetID              ResourceID          `json:"target_id"`
	PlanDigest            string              `json:"plan_digest"`
	ObservationDigest     string              `json:"observation_digest"`
	IssuedBy              string              `json:"issued_by"`
	IssuedAt              time.Time           `json:"issued_at"`
	ExpiresAt             time.Time           `json:"expires_at"`
	ConsumedAt            time.Time           `json:"consumed_at,omitempty"`
	ConsumedByOperationID ResourceID          `json:"consumed_by_operation_id,omitempty"`
	Status                ApprovalGrantStatus `json:"status"`
}
