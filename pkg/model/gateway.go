package model

import "time"

// GatewayGrant is the independent gateway's authenticated wire contract. The
// publisher must bind it to a real majority lease; the string alone grants no
// database promotion authority.
type GatewayGrant struct {
	ClusterID   ResourceID `json:"cluster_id"`
	Epoch       uint64     `json:"epoch"`
	Sequence    uint64     `json:"sequence"`
	Mode        string     `json:"mode"`
	TargetID    ResourceID `json:"target_instance_id"`
	LeaseID     ResourceID `json:"lease_id"`
	OperationID ResourceID `json:"operation_id"`
	IssuedAt    time.Time  `json:"issued_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
}
