package store

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/pkg/model"
)

// ClusterPolicy is the Raft-replicated half of automatic failover tuning.
//
// The node configuration file is read once at start-up and differs per
// controller, so changing it means editing three machines and restarting them.
// This record is cluster-wide: it lives in replicated metadata, the console can
// change it, every change is audited, and the runtime re-reads it every round.
//
// Every field is independent and zero means "not set": an unset field falls
// back to the value this node was started with. A cluster therefore adopts
// policy one parameter at a time instead of all-or-nothing, and clearing the
// record restores exactly the behaviour the nodes were configured with.
type ClusterPolicy struct {
	Engines   map[string]ClusterEnginePolicy `json:"engines,omitempty"`
	UpdatedAt time.Time                      `json:"updated_at"`
	UpdatedBy string                         `json:"updated_by,omitempty"`
	Note      string                         `json:"note,omitempty"`
}

// ClusterEnginePolicy holds the tunable automatic failover parameters of one
// engine. AutomaticFailoverSuppressed is the maintenance switch: while it is
// true the controller records failure evidence but never starts an automatic
// failover for that engine, which is what an operator wants during planned
// maintenance on a cluster that is legitimately failing its probes.
type ClusterEnginePolicy struct {
	AutomaticFailoverMinimumObservations     int  `json:"automatic_failover_minimum_observations,omitempty"`
	AutomaticFailoverFailureWindowSeconds    int  `json:"automatic_failover_failure_window_seconds,omitempty"`
	AutomaticFailoverOperationTimeoutSeconds int  `json:"automatic_failover_operation_timeout_seconds,omitempty"`
	AutomaticFailoverSuppressed              bool `json:"automatic_failover_suppressed,omitempty"`
}

// clusterPolicyEngines are the engines that can run an automatic failover at
// all. A policy for Oracle or SQL Server would be inert, so it is rejected
// rather than silently stored.
var clusterPolicyEngines = map[string]struct{}{
	string(model.EngineMySQL):      {},
	string(model.EnginePostgreSQL): {},
}

func validateClusterPolicy(policy ClusterPolicy) error {
	for engine, settings := range policy.Engines {
		engine = strings.TrimSpace(engine)
		if _, allowed := clusterPolicyEngines[engine]; !allowed {
			return validationError(fmt.Sprintf("cluster policy engine %q does not support automatic failover", engine))
		}
		if err := config.ValidateAutomaticFailoverTiming(settings.AutomaticFailoverMinimumObservations,
			settings.AutomaticFailoverFailureWindowSeconds, settings.AutomaticFailoverOperationTimeoutSeconds,
			engine); err != nil {
			return validationError(err.Error())
		}
	}
	if len(strings.TrimSpace(policy.Note)) > 512 {
		return validationError("cluster policy note is too long")
	}
	return nil
}

func cloneClusterEnginePolicyMap(source map[string]ClusterEnginePolicy) map[string]ClusterEnginePolicy {
	if source == nil {
		return nil
	}
	cloned := make(map[string]ClusterEnginePolicy, len(source))
	for engine, settings := range source {
		cloned[engine] = settings
	}
	return cloned
}

func cloneClusterPolicy(policy ClusterPolicy) ClusterPolicy {
	cloned := policy
	cloned.Engines = cloneClusterEnginePolicyMap(policy.Engines)
	return cloned
}

// ClusterPolicy returns the replicated policy. An empty policy is the normal
// state and means "every node keeps using its own configuration file".
func (repository *Repository) ClusterPolicy() ClusterPolicy {
	if repository == nil {
		return ClusterPolicy{}
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if repository.snapshot.ClusterPolicy == nil {
		return ClusterPolicy{}
	}
	return cloneClusterPolicy(*repository.snapshot.ClusterPolicy)
}

// ClusterEnginePolicy returns the policy of one engine, or the zero value when
// no override exists for it.
func (repository *Repository) ClusterEnginePolicy(engine model.Engine) ClusterEnginePolicy {
	return repository.ClusterPolicy().Engines[string(engine)]
}

// AutomaticFailoverSuppressed reports whether planned maintenance has paused
// automatic failover for an engine. Evidence keeps being recorded, so lifting
// the pause cannot resurrect a stale series.
func (repository *Repository) AutomaticFailoverSuppressed(engine model.Engine) bool {
	return repository.ClusterEnginePolicy(engine).AutomaticFailoverSuppressed
}

// PutClusterPolicy replaces the replicated policy. Passing a policy with no
// engines clears every override and returns the cluster to its configuration
// files. The actor and a timestamp are stored with the record so an operator
// can see who changed failover behaviour and when. The audit is committed in
// the same snapshot, so a failed consensus commit cannot publish either half.
func (repository *Repository) PutClusterPolicy(policy ClusterPolicy, actor string) (ClusterPolicy, error) {
	stored := cloneClusterPolicy(policy)
	stored.Engines = cloneClusterEnginePolicyMap(policy.Engines)
	if len(stored.Engines) == 0 {
		stored.Engines = nil
	}
	stored.UpdatedBy = strings.TrimSpace(actor)
	stored.Note = strings.TrimSpace(policy.Note)
	if err := validateClusterPolicy(stored); err != nil {
		return ClusterPolicy{}, err
	}
	if stored.UpdatedAt.IsZero() {
		stored.UpdatedAt = time.Now().UTC()
	}
	stored.UpdatedAt = stored.UpdatedAt.UTC()
	audit := redactAudit(model.AuditEvent{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: stored.UpdatedAt, UpdatedAt: stored.UpdatedAt},
		OperationID:  model.NewResourceID(), Stage: model.StageAudit, Actor: stored.UpdatedBy,
		Message: describeClusterPolicyChange(stored),
	})
	if err := validateAuditText(audit); err != nil {
		return ClusterPolicy{}, err
	}

	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.snapshot.ClusterPolicy == nil && len(stored.Engines) == 0 {
		// Nothing to clear: do not spend a replicated commit on a no-op.
		return ClusterPolicy{}, nil
	}
	next := repository.snapshot
	next.ClusterPolicy = nil
	if len(stored.Engines) > 0 {
		committed := cloneClusterPolicy(stored)
		next.ClusterPolicy = &committed
	}
	next.Audits = append(append([]model.AuditEvent{}, repository.snapshot.Audits...), audit)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return ClusterPolicy{}, fmt.Errorf("persist cluster policy: %w", err)
	}
	if next.ClusterPolicy == nil {
		return ClusterPolicy{}, nil
	}
	return cloneClusterPolicy(*next.ClusterPolicy), nil
}

func describeClusterPolicyChange(policy ClusterPolicy) string {
	if len(policy.Engines) == 0 {
		return "清除集群策略：全部引擎恢复节点配置文件取值"
	}
	engines := make([]string, 0, len(policy.Engines))
	for engine := range policy.Engines {
		engines = append(engines, engine)
	}
	sort.Strings(engines)
	parts := make([]string, 0, len(engines))
	for _, engine := range engines {
		parts = append(parts, fmt.Sprintf("%s（%s）", engine, describeClusterEnginePolicy(policy.Engines[engine])))
	}
	message := "更新集群策略：" + strings.Join(parts, "；")
	if policy.Note != "" {
		message += "；备注：" + policy.Note
	}
	return message
}

// ClusterPolicySummary renders the policy for display. It is deliberately
// ordered so the console shows the same sequence on every node.
func (repository *Repository) ClusterPolicySummary() []string {
	policy := repository.ClusterPolicy()
	engines := make([]string, 0, len(policy.Engines))
	for engine := range policy.Engines {
		engines = append(engines, engine)
	}
	sort.Strings(engines)
	summary := make([]string, 0, len(engines))
	for _, engine := range engines {
		settings := policy.Engines[engine]
		summary = append(summary, fmt.Sprintf("%s: %s", engine, describeClusterEnginePolicy(settings)))
	}
	return summary
}

func describeClusterEnginePolicy(settings ClusterEnginePolicy) string {
	parts := make([]string, 0, 4)
	if settings.AutomaticFailoverMinimumObservations > 0 {
		parts = append(parts, fmt.Sprintf("观测次数 %d", settings.AutomaticFailoverMinimumObservations))
	}
	if settings.AutomaticFailoverFailureWindowSeconds > 0 {
		parts = append(parts, fmt.Sprintf("证据窗口 %d 秒", settings.AutomaticFailoverFailureWindowSeconds))
	}
	if settings.AutomaticFailoverOperationTimeoutSeconds > 0 {
		parts = append(parts, fmt.Sprintf("操作预算 %d 秒", settings.AutomaticFailoverOperationTimeoutSeconds))
	}
	if settings.AutomaticFailoverSuppressed {
		parts = append(parts, "维护抑制：暂停自动切换")
	}
	if len(parts) == 0 {
		return "未覆盖（沿用节点配置）"
	}
	return strings.Join(parts, "、")
}
