package model

import "time"

// RecoveryEvidence contains only facts collected by an allowlisted Agent after
// writes have been fenced. API callers cannot provide or override these facts.
type RecoveryEvidence struct {
	InstanceID           ResourceID         `json:"instance_id"`
	NativeID             string             `json:"native_id"`
	Engine               Engine             `json:"engine"`
	ObservedAt           time.Time          `json:"observed_at"`
	Fenced               bool               `json:"fenced"`
	Complete             bool               `json:"complete"`
	PreparedTransactions bool               `json:"prepared_transactions"`
	GTIDExecuted         string             `json:"gtid_executed,omitempty"`
	GTIDPurged           string             `json:"gtid_purged,omitempty"`
	SystemIdentifier     string             `json:"system_identifier,omitempty"`
	Timeline             uint32             `json:"timeline,omitempty"`
	Position             string             `json:"position,omitempty"`
	Checkpoint           string             `json:"checkpoint,omitempty"`
	Redo                 string             `json:"redo,omitempty"`
	WALSegmentBytes      uint64             `json:"wal_segment_bytes,omitempty"`
	ControlState         string             `json:"control_state,omitempty"`
	ControlDigest        string             `json:"control_digest,omitempty"`
	WALTailDigest        string             `json:"wal_tail_digest,omitempty"`
	History              []RecoveryTimeline `json:"history,omitempty"`
	Fingerprint          string             `json:"fingerprint"`
}

type RecoveryWALRequest struct {
	Fingerprint string `json:"fingerprint"`
	Timeline    uint32 `json:"timeline"`
	Start       string `json:"start"`
	End         string `json:"end"`
}

type RecoveryWALResult struct {
	Digest       string `json:"digest"`
	Transactions bool   `json:"transactions"`
}

// A history entry names an ancestor and the LSN at which its child forked.
type RecoveryTimeline struct {
	Timeline  uint32 `json:"timeline"`
	SwitchLSN string `json:"switch_lsn"`
}

type RecoveryProof struct {
	CandidateID           ResourceID `json:"candidate_id"`
	OtherID               ResourceID `json:"other_id"`
	CandidateFingerprint  string     `json:"candidate_fingerprint"`
	OtherFingerprint      string     `json:"other_fingerprint"`
	CommonWALVerified     bool       `json:"common_wal_verified"`
	DiscardedWALVerified  bool       `json:"discarded_wal_verified"`
	DiscardedTransactions bool       `json:"discarded_transactions"`
}

type RecoveryStage string

const (
	RecoveryPlanned    RecoveryStage = "planned"
	RecoveryFencing    RecoveryStage = "fencing"
	RecoveryInspecting RecoveryStage = "inspecting"
	RecoverySelecting  RecoveryStage = "selecting"
	RecoveryStarting   RecoveryStage = "starting_primary"
	RecoveryRebuilding RecoveryStage = "rebuilding_replicas"
	RecoveryVerifying  RecoveryStage = "verifying"
	RecoveryCommitted  RecoveryStage = "committed"
	RecoverySucceeded  RecoveryStage = "succeeded"
	RecoveryBlocked    RecoveryStage = "blocked"
)

func (s RecoveryStage) Valid() bool {
	switch s {
	case RecoveryPlanned, RecoveryFencing, RecoveryInspecting, RecoverySelecting, RecoveryStarting,
		RecoveryRebuilding, RecoveryVerifying, RecoveryCommitted, RecoverySucceeded, RecoveryBlocked:
		return true
	}
	return false
}

type RecoveryEvent struct {
	At         time.Time     `json:"at"`
	Stage      RecoveryStage `json:"stage"`
	InstanceID ResourceID    `json:"instance_id,omitempty"`
	Message    string        `json:"message"`
}

type RecoveryTask struct {
	ResourceMeta
	ClusterID           ResourceID         `json:"cluster_id"`
	Engine              Engine             `json:"engine"`
	RequestedBy         string             `json:"requested_by"`
	Stage               RecoveryStage      `json:"stage"`
	PlanDigest          string             `json:"plan_digest"`
	InventoryGeneration uint64             `json:"inventory_generation"`
	Members             []DatabaseInstance `json:"members"`
	Evidence            []RecoveryEvidence `json:"evidence,omitempty"`
	Proofs              []RecoveryProof    `json:"proofs,omitempty"`
	PrimaryID           ResourceID         `json:"primary_id,omitempty"`
	LeaseID             ResourceID         `json:"lease_id,omitempty"`
	Events              []RecoveryEvent    `json:"events"`
	Message             string             `json:"message"`
	CommittedAt         time.Time          `json:"committed_at,omitempty"`
	AttemptStartedAt    time.Time          `json:"attempt_started_at,omitempty"`
	VerifiedAt          time.Time          `json:"verified_at,omitempty"`
	CompletedAt         time.Time          `json:"completed_at,omitempty"`
}

// RecoveryState separates a historical outage classification from live health.
// Commit and protection release are distinct durable boundaries.
type RecoveryState struct {
	RecoveredAt                time.Time  `json:"recovered_at,omitempty"`
	TaskID                     ResourceID `json:"task_id"`
	CurrentPrimaryID           ResourceID `json:"current_primary_id,omitempty"`
	ExpectedState              string     `json:"expected_state"`
	ActualState                string     `json:"actual_state"`
	IncidentActive             bool       `json:"incident_active"`
	IncidentRecovered          bool       `json:"incident_recovered"`
	LastRecoveryStatus         string     `json:"last_recovery_status"`
	LastShutdownClassification string     `json:"last_shutdown_classification"`
}

func (cluster DatabaseCluster) DisasterRecoveryActive() bool {
	return cluster.RecoveryFreeze && cluster.Recovery != nil && cluster.Recovery.LastRecoveryStatus != "succeeded"
}
