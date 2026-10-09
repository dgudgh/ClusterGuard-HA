// Package configuration coordinates a node-scoped rolling configuration change.
package configuration

import (
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/internal/store"
	"context"
	"net/http"
	"sync"
	"time"
)

const SchemaVersion = 1

type Node struct {
	TaskErrorRevision uint64                      `json:"task_error_revision,omitempty"`
	TaskError         string                      `json:"task_error,omitempty"`
	SchemaVersion     int                         `json:"schema_version"`
	NodeID            string                      `json:"node_id"`
	Ready             bool                        `json:"ready"`
	RestartAvailable  bool                        `json:"restart_available"`
	Fingerprint       string                      `json:"fingerprint"`
	TaskID            string                      `json:"task_id"`
	Values            map[string]int              `json:"values"`
	Fields            []config.ConfigurationField `json:"fields"`
	Error             string                      `json:"error,omitempty"`
}
type Request struct {
	RequestID string         `json:"request_id"`
	PlanHash  string         `json:"plan_hash"`
	NodeIDs   []string       `json:"node_ids"`
	Changes   map[string]int `json:"changes"`
}
type Plan struct {
	PolicyDigest    string                      `json:"policy_digest"`
	Hash            string                      `json:"hash"`
	Members         []string                    `json:"members"`
	Targets         []store.ConfigurationTarget `json:"targets"`
	Changes         map[string]int              `json:"changes"`
	RestartRequired bool                        `json:"restart_required"`
}
type Status struct {
	SchemaVersion int                          `json:"schema_version"`
	Local         Node                         `json:"local"`
	Members       []consensus.ControllerMember `json:"members"`
	Tasks         []store.ConfigurationTask    `json:"tasks"`
}
type Authority interface {
	RequireMutationAuthority(context.Context) error
	Status(context.Context) consensus.Status
	ControllerMembers(context.Context) ([]consensus.ControllerMember, error)
}
type Maintenance interface{ Check(context.Context) error }
type Restart interface {
	Ready(context.Context) error
	RestartController(context.Context) error
}
type Peer interface {
	Node(context.Context, consensus.ControllerMember) (Node, error)
	Candidate(context.Context, consensus.ControllerMember, map[string]int) error
	Permit(context.Context, consensus.ControllerMember, Permit) error
}
type Manager struct {
	Repository      *store.Repository
	Authority       Authority
	Maintenance     Maintenance
	Restart         Restart
	Peer            Peer
	Config          config.File
	ConfigPath      string
	StartedAt       time.Time
	StartedOverride config.ConfigurationOverrides
	StartedBaseHash string
	Validate        func(config.File) error
	Now             func() time.Time
	// StepTimeout includes restart, election, catch-up and actual-value verification.
	StepTimeout      time.Duration
	restartRequested string
	mu               sync.RWMutex
	localError       string
	localRevision    uint64
	workerRevision   uint64
}
type HTTPPeer struct {
	Client *http.Client
	Token  string
}
