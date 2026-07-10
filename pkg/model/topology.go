package model

import "time"

type ThreadState string

const (
	ThreadUnknown ThreadState = "unknown"
	ThreadRunning ThreadState = "running"
	ThreadStopped ThreadState = "stopped"
)

type ReplicationStatus struct {
	SourceIdentity    EngineIdentity `json:"source_identity,omitempty"`
	IOThread          ThreadState    `json:"io_thread"`
	SQLThread         ThreadState    `json:"sql_thread"`
	LagSeconds        *int64         `json:"lag_seconds,omitempty"`
	RetrievedPosition string         `json:"retrieved_position,omitempty"`
	ExecutedPosition  string         `json:"executed_position,omitempty"`
	LastError         string         `json:"last_error,omitempty"`
}

type MetricSample struct {
	InstanceID ResourceID         `json:"instance_id"`
	ObservedAt time.Time          `json:"observed_at"`
	Values     map[string]float64 `json:"values"`
}

type ProbeStatus struct {
	EndpointID ResourceID `json:"endpoint_id"`
	InstanceID ResourceID `json:"instance_id,omitempty"`
	Health     Health     `json:"health"`
}

type TopologySnapshot struct {
	ClusterID  ResourceID         `json:"cluster_id"`
	Instances  []DatabaseInstance `json:"instances"`
	Links      []ReplicationLink  `json:"links"`
	Probes     []ProbeStatus      `json:"probes"`
	Health     Health             `json:"health"`
	Anomalies  []MetadataAnomaly  `json:"anomalies,omitempty"`
	ObservedAt time.Time          `json:"observed_at"`
}

type CandidatePolicy struct {
	MaximumLagSeconds int64 `json:"maximum_lag_seconds"`
	RequireGTID       bool  `json:"require_gtid"`
}

type CandidateAssessment struct {
	InstanceID   ResourceID `json:"instance_id"`
	Eligible     bool       `json:"eligible"`
	Rank         int        `json:"rank"`
	RiskLevel    string     `json:"risk_level"`
	DataLossRisk string     `json:"data_loss_risk"`
	Checks       []Check    `json:"checks"`
}
