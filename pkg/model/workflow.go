package model

import "time"

type WorkflowStage string

const (
	StageDiscover    WorkflowStage = "discover"
	StagePrecheck    WorkflowStage = "precheck"
	StagePlan        WorkflowStage = "plan"
	StageSafetyGuard WorkflowStage = "safety_guard"
	StageLock        WorkflowStage = "lock"
	StageApprove     WorkflowStage = "approve"
	StageExecute     WorkflowStage = "execute"
	StageVerify      WorkflowStage = "verify"
	StageAudit       WorkflowStage = "audit"
	StageReport      WorkflowStage = "report"
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
	OperationPlanned       OperationStatus = "planned"
	OperationBlocked       OperationStatus = "blocked"
	OperationRunning       OperationStatus = "running"
	OperationSucceeded     OperationStatus = "succeeded"
	OperationFailed        OperationStatus = "failed"
	OperationIndeterminate OperationStatus = "indeterminate"
	OperationUnsupported   OperationStatus = "unsupported"
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
	OperationID       ResourceID            `json:"operation_id"`
	ClusterID         ResourceID            `json:"cluster_id,omitempty"`
	SourceID          ResourceID            `json:"source_id,omitempty"`
	TargetID          ResourceID            `json:"target_id,omitempty"`
	Stage             WorkflowStage         `json:"stage"`
	ObservationToken  string                `json:"observation_token,omitempty"`
	ResourceRevisions map[ResourceID]uint64 `json:"resource_revisions,omitempty"`
	Checks            []Check               `json:"checks"`
	Steps             []PlanStep            `json:"steps,omitempty"`
	Digest            string                `json:"digest,omitempty"`
	Summary           string                `json:"summary"`
	Mutating          bool                  `json:"mutating"`
}

type PlanStep struct {
	Index         int        `json:"index"`
	Name          string     `json:"name"`
	Owner         string     `json:"owner"`
	TargetID      ResourceID `json:"target_id,omitempty"`
	Mutating      bool       `json:"mutating"`
	Postcondition string     `json:"postcondition,omitempty"`
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
	OperationID ResourceID      `json:"operation_id"`
	Title       string          `json:"title"`
	Status      OperationStatus `json:"status"`
	Summary     string          `json:"summary"`
}

type StepAttempt struct {
	Step         string          `json:"step"`
	Attempt      uint64          `json:"attempt"`
	Status       OperationStatus `json:"status"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   time.Time       `json:"finished_at,omitempty"`
	Message      string          `json:"message,omitempty"`
	FailureClass string          `json:"failure_class,omitempty"`
}

type OperationRecord struct {
	ResourceMeta
	Operation      Operation       `json:"operation"`
	TargetID       ResourceID      `json:"target_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	Stage          WorkflowStage   `json:"stage"`
	Status         OperationStatus `json:"status"`
	Observation    string          `json:"observation_token,omitempty"`
	Plan           OperationPlan   `json:"plan"`
	Attempts       []StepAttempt   `json:"attempts,omitempty"`
	Execution      Execution       `json:"execution"`
	Verification   Verification    `json:"verification"`
	FailureClass   string          `json:"failure_class,omitempty"`
	Message        string          `json:"message,omitempty"`
}

type OperationTransition struct {
	Stage        WorkflowStage
	Status       OperationStatus
	Observation  string
	Attempt      *StepAttempt
	Execution    *Execution
	Verification *Verification
	FailureClass string
	Message      string
}

type MetadataAnomaly struct {
	ResourceMeta
	ClusterID ResourceID `json:"cluster_id,omitempty"`
	Engine    Engine     `json:"engine"`
	Kind      string     `json:"kind"`
	Severity  string     `json:"severity"`
	Message   string     `json:"message"`
}
