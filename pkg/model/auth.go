package model

import "time"

type PlatformRole string

const (
	PlatformRoleAdmin    PlatformRole = "admin"
	PlatformRoleOperator PlatformRole = "operator"
	PlatformRoleViewer   PlatformRole = "viewer"
)

func (role PlatformRole) Valid() bool {
	return role == PlatformRoleAdmin || role == PlatformRoleOperator || role == PlatformRoleViewer
}

type PlatformUser struct {
	ResourceMeta
	Username           string       `json:"username"`
	DisplayName        string       `json:"display_name"`
	Role               PlatformRole `json:"role"`
	PasswordHash       string       `json:"password_hash"`
	MustChangePassword bool         `json:"must_change_password"`
	Disabled           bool         `json:"disabled"`
	AuthRevision       uint64       `json:"auth_revision"`
	PasswordChangedAt  time.Time    `json:"password_changed_at,omitempty"`
	LastRecoveryID     ResourceID   `json:"last_recovery_id,omitempty"`
}

type PlatformSession struct {
	ResourceMeta
	UserID           ResourceID `json:"user_id"`
	TokenHash        string     `json:"token_hash"`
	CSRFHash         string     `json:"csrf_hash"`
	UserAuthRevision uint64     `json:"user_auth_revision"`
	IssuedAt         time.Time  `json:"issued_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	RevokedAt        time.Time  `json:"revoked_at,omitempty"`
}

type SecurityEvent struct {
	ResourceMeta
	UserID   ResourceID `json:"user_id,omitempty"`
	Username string     `json:"username,omitempty"`
	Kind     string     `json:"kind"`
	Outcome  string     `json:"outcome"`
	Message  string     `json:"message"`
}
