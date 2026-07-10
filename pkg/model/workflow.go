package model

import "time"

type WorkflowStage string

const (
	StageDiscover WorkflowStage = "discover"
	StagePrecheck WorkflowStage = "precheck"
	StagePlan     WorkflowStage = "plan"
	StageLock     WorkflowStage = "lock"
	StageApprove  WorkflowStage = "approve"
	StageExecute  WorkflowStage = "execute"
	StageVerify   WorkflowStage = "verify"
	StageAudit    WorkflowStage = "audit"
	StageReport   WorkflowStage = "report"
)

type OperationKind string

const (
	OperationSwitchover             OperationKind = "switchover"
	OperationFailover               OperationKind = "failover"
	OperationNodeSync               OperationKind = "node_sync"
	OperationMetadataReconciliation OperationKind = "metadata_reconciliation"
)

type OperationStatus string

const (
	OperationPlanned     OperationStatus = "planned"
	OperationBlocked     OperationStatus = "blocked"
	OperationRunning     OperationStatus = "running"
	OperationSucceeded   OperationStatus = "succeeded"
	OperationFailed      OperationStatus = "failed"
	OperationUnsupported OperationStatus = "unsupported"
)

type CheckStatus string

const (
	CheckPass CheckStatus = "pass"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
)

type Check struct {
	Name    string      `json:"name"`
	Status  CheckStatus `json:"status"`
	Message string      `json:"message"`
}

type Operation struct {
	ResourceMeta
	ClusterID   ResourceID      `json:"cluster_id"`
	Engine      Engine          `json:"engine"`
	Kind        OperationKind   `json:"kind"`
	Status      OperationStatus `json:"status"`
	RequestedBy string          `json:"requested_by"`
}

type OperationPlan struct {
	ResourceMeta
	OperationID ResourceID    `json:"operation_id"`
	Stage       WorkflowStage `json:"stage"`
	Checks      []Check       `json:"checks"`
	Summary     string        `json:"summary"`
	Mutating    bool          `json:"mutating"`
}

type Execution struct {
	ResourceMeta
	OperationID ResourceID      `json:"operation_id"`
	Status      OperationStatus `json:"status"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  time.Time       `json:"finished_at,omitempty"`
	Message     string          `json:"message"`
}

type Verification struct {
	ResourceMeta
	OperationID ResourceID `json:"operation_id"`
	Passed      bool       `json:"passed"`
	Checks      []Check    `json:"checks"`
	ObservedAt  time.Time  `json:"observed_at"`
}

type AuditEvent struct {
	ResourceMeta
	OperationID ResourceID    `json:"operation_id"`
	Stage       WorkflowStage `json:"stage"`
	Actor       string        `json:"actor"`
	Message     string        `json:"message"`
}

type Report struct {
	ResourceMeta
	OperationID ResourceID `json:"operation_id"`
	Title       string     `json:"title"`
	Summary     string     `json:"summary"`
}

type MetadataAnomaly struct {
	ResourceMeta
	ClusterID ResourceID `json:"cluster_id,omitempty"`
	Engine    Engine     `json:"engine"`
	Kind      string     `json:"kind"`
	Severity  string     `json:"severity"`
	Message   string     `json:"message"`
}
