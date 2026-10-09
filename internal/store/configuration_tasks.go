package store

import (
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type ConfigurationTarget struct {
	NodeID      string         `json:"node_id"`
	APIAddress  string         `json:"api_address"`
	Fingerprint string         `json:"fingerprint"`
	Before      map[string]int `json:"before"`
	Status      string         `json:"status"`
	Error       string         `json:"error,omitempty"`
}
type ConfigurationTask struct {
	StepTimeoutSeconds int                   `json:"step_timeout_seconds,omitempty"`
	Members            []string              `json:"members"`
	PolicyDigest       string                `json:"policy_digest"`
	PlanHash           string                `json:"plan_hash"`
	TaskID             string                `json:"task_id"`
	Revision           uint64                `json:"revision"`
	Changes            map[string]int        `json:"changes"`
	Targets            []ConfigurationTarget `json:"targets"`
	CurrentNode        string                `json:"current_node,omitempty"`
	Status             string                `json:"status"`
	Mode               string                `json:"mode"`
	Active             bool                  `json:"active"`
	Actor              string                `json:"actor"`
	CreatedAt          time.Time             `json:"created_at"`
	UpdatedAt          time.Time             `json:"updated_at"`
	StepStartedAt      time.Time             `json:"step_started_at"`
	Error              string                `json:"error,omitempty"`
}

func CloneConfigurationTask(t ConfigurationTask) ConfigurationTask {
	b, _ := json.Marshal(t)
	var c ConfigurationTask
	_ = json.Unmarshal(b, &c)
	return c
}
func ValidateConfigurationTask(t ConfigurationTask) error {
	// Zero is reserved for tasks persisted by 3.1.2.1 with the legacy 180s limit.
	if t.StepTimeoutSeconds != 0 && (t.StepTimeoutSeconds < 30 || t.StepTimeoutSeconds > 3600) {
		return fmt.Errorf("invalid configuration task timeout")
	}
	if !model.ValidResourceID(model.ResourceID(t.TaskID)) || t.Revision == 0 || len(t.Targets) == 0 || len(t.Targets) > 32 {
		return fmt.Errorf("invalid configuration task identity/targets")
	}
	_, hashError := hex.DecodeString(t.PlanHash)
	if len(t.Members) < 3 || len(t.PlanHash) != 64 || hashError != nil || t.Actor == "" || len(t.PolicyDigest) != 64 {
		return fmt.Errorf("invalid configuration task membership/plan")
	}
	members := map[string]bool{}
	for _, id := range t.Members {
		if !model.ValidResourceID(model.ResourceID(id)) || members[id] {
			return fmt.Errorf("invalid configuration membership")
		}
		members[id] = true
	}
	if t.StepTimeoutSeconds > 0 && config.ConfigurationRequiresAllVoters(t.Changes) && len(t.Targets) != len(t.Members) {
		return fmt.Errorf("cluster-scoped configuration requires all voters")
	}
	if e := config.ValidateConfigurationChanges(t.Changes); e != nil {
		return e
	}
	if t.Mode != "apply" && t.Mode != "rollback" {
		return fmt.Errorf("invalid configuration mode")
	}
	switch t.Status {
	case "running", "failed", "succeeded", "rolled_back":
	default:
		return fmt.Errorf("invalid configuration status")
	}
	if t.Active != (t.Status == "running" || t.Status == "failed") {
		return fmt.Errorf("invalid configuration maintenance state")
	}
	seen := map[string]bool{}
	currentFound := t.CurrentNode == ""
	for _, target := range t.Targets {
		if !model.ValidResourceID(model.ResourceID(target.NodeID)) || seen[target.NodeID] || !members[target.NodeID] || len(target.Fingerprint) != 64 {
			return fmt.Errorf("invalid configuration target")
		}
		if _, e := hex.DecodeString(target.Fingerprint); e != nil {
			return fmt.Errorf("invalid configuration target digest")
		}
		fields := config.EditableConfigurationFields()
		if len(target.Before) != len(fields) {
			return fmt.Errorf("incomplete configuration before-values")
		}
		for _, field := range fields {
			v, ok := target.Before[field.Path]
			if !ok || v < 0 {
				return fmt.Errorf("invalid configuration before-value")
			}
		}
		if !t.Active && (t.CurrentNode != "" || ((t.Status == "succeeded" && target.Status != "applied") || (t.Status == "rolled_back" && target.Status != "rolled_back"))) {
			return fmt.Errorf("configuration completion requires all target verification")
		}
		seen[target.NodeID] = true
		if target.NodeID == t.CurrentNode {
			currentFound = true
		}
		switch target.Status {
		case "pending", "applying", "applied", "failed", "rolled_back":
		default:
			return fmt.Errorf("invalid configuration target status")
		}
	}
	if !currentFound {
		return fmt.Errorf("configuration current node not in task")
	}
	return nil
}
func (r *Repository) ConfigurationTasks() []ConfigurationTask {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []ConfigurationTask{}
	for _, t := range r.snapshot.ConfigurationTasks {
		out = append(out, CloneConfigurationTask(t))
	}
	return out
}
func (r *Repository) ConfigurationTask(id string) (ConfigurationTask, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.snapshot.ConfigurationTasks[id]
	return CloneConfigurationTask(t), ok
}
func (r *Repository) ConfigurationMaintenanceActive() bool {
	for _, t := range r.ConfigurationTasks() {
		if t.Active {
			return true
		}
	}
	return false
}
func (r *Repository) CreateConfigurationTask(t ConfigurationTask) (ConfigurationTask, error) {
	t.Revision = 1
	t.Mode = "apply"
	t.Status = "running"
	t.Active = true
	t.CreatedAt = r.now().UTC()
	t.UpdatedAt = t.CreatedAt
	if e := ValidateConfigurationTask(t); e != nil {
		return t, validationError(e.Error())
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.snapshot.ConfigurationTasks[t.TaskID]; ok {
		return t, conflictError("configuration task already exists")
	}
	policy := ClusterPolicy{}
	if r.snapshot.ClusterPolicy != nil {
		policy = *r.snapshot.ClusterPolicy
	}
	if ConfigurationPolicyDigest(policy) != t.PolicyDigest {
		return t, conflictError("dynamic policy changed after configuration preflight")
	}
	for key := range t.Changes {
		if ConfigurationPolicyOverrides(policy, key) {
			return t, conflictError("edit the active dynamic policy instead of its startup default")
		}
	}
	if r.snapshot.SoftwareUpdateGate != nil {
		return t, conflictError("software update maintenance active")
	}
	for _, old := range r.snapshot.ConfigurationTasks {
		if old.Active {
			return t, conflictError("configuration task is already active")
		}
	}
	for _, o := range r.snapshot.Operations {
		if o.Status == model.OperationRunning || o.Status == model.OperationIndeterminate {
			return t, conflictError("database operation requires completion or review")
		}
	}
	for _, o := range r.snapshot.RecoveryTasks {
		if o.Stage != model.RecoveryPlanned && o.Stage != model.RecoverySucceeded {
			return t, conflictError("disaster recovery requires completion or review")
		}
	}
	for _, o := range r.snapshot.PowerOperations {
		if !model.TerminalPowerState(o.State) {
			return t, conflictError("power lifecycle active")
		}
	}
	for _, o := range r.snapshot.LifecycleTasks {
		switch o.Status {
		case lifecycle.TaskPlanned, lifecycle.TaskQueued, lifecycle.TaskRunning, lifecycle.TaskVerifying:
			return t, conflictError("node lifecycle task is active")
		}
	}
	return r.commitConfigurationTaskLocked(t, "configuration distribution created")
}
func (r *Repository) UpdateConfigurationTask(t ConfigurationTask, expected uint64) (ConfigurationTask, error) {
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.snapshot.ConfigurationTasks[t.TaskID]
	if !ok || old.Revision != expected {
		return t, conflictError("configuration task revision changed")
	}
	if !old.Active {
		return t, conflictError("completed configuration task is immutable")
	}
	// Neither retries nor an advancing Leader can change the selected subject.
	a, _ := json.Marshal(old.Changes)
	b, _ := json.Marshal(t.Changes)
	if old.StepTimeoutSeconds != t.StepTimeoutSeconds || old.Actor != t.Actor || !old.CreatedAt.Equal(t.CreatedAt) || old.PlanHash != t.PlanHash || old.PolicyDigest != t.PolicyDigest || !reflect.DeepEqual(old.Members, t.Members) || string(a) != string(b) || len(old.Targets) != len(t.Targets) {
		return t, validationError("configuration task subject changed")
	}
	for i, n := range old.Targets {
		if n.NodeID != t.Targets[i].NodeID || n.APIAddress != t.Targets[i].APIAddress || n.Fingerprint != t.Targets[i].Fingerprint || !reflect.DeepEqual(n.Before, t.Targets[i].Before) {
			return t, validationError("configuration target identity changed")
		}
	}
	t.Revision = expected + 1
	t.UpdatedAt = r.now().UTC()
	if e := ValidateConfigurationTask(t); e != nil {
		return t, validationError(e.Error())
	}
	return r.commitConfigurationTaskLocked(t, "configuration distribution "+t.Status+" / "+t.Mode)
}
func (r *Repository) commitConfigurationTaskLocked(t ConfigurationTask, message string) (ConfigurationTask, error) {
	next := r.snapshot
	next.ConfigurationTasks = map[string]ConfigurationTask{}
	for id, x := range r.snapshot.ConfigurationTasks {
		next.ConfigurationTasks[id] = CloneConfigurationTask(x)
	}
	next.ConfigurationTasks[t.TaskID] = CloneConfigurationTask(t)
	audit := model.AuditEvent{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: t.UpdatedAt, UpdatedAt: t.UpdatedAt}, OperationID: model.ResourceID(t.TaskID), Stage: model.StageAudit, Actor: t.Actor, Message: message}
	next.Audits = append(append([]model.AuditEvent{}, r.snapshot.Audits...), audit)
	if e := r.commitSnapshotLocked(next); e != nil {
		return ConfigurationTask{}, fmt.Errorf("persist configuration task: %w", e)
	}
	return CloneConfigurationTask(t), nil
}

// ConfigurationPolicyDigest binds startup tuning to the dynamic policy that may
// override it. Policy writes are frozen while a configuration task is active.
func ConfigurationPolicyDigest(policy ClusterPolicy) string {
	b, _ := json.Marshal(policy)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func ConfigurationPolicyOverrides(policy ClusterPolicy, path string) bool {
	engine, key, ok := strings.Cut(path, ".")
	if !ok {
		return false
	}
	settings := policy.Engines[engine]
	switch key {
	case "automatic_failover_minimum_observations":
		return settings.AutomaticFailoverMinimumObservations > 0
	case "automatic_failover_failure_window_seconds":
		return settings.AutomaticFailoverFailureWindowSeconds > 0
	case "automatic_failover_operation_timeout_seconds":
		return settings.AutomaticFailoverOperationTimeoutSeconds > 0
	}
	return false
}
