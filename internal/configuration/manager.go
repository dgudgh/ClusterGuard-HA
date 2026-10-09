package configuration

import (
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"time"
)

func digest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}
func (m *Manager) Active() bool { return m.Repository.ConfigurationMaintenanceActive() }
func (m *Manager) Local(ctx context.Context) (Node, error) {
	m.mu.RLock()
	taskError := m.localError
	errorRevision := m.localRevision
	m.mu.RUnlock()
	n := Node{TaskError: taskError, TaskErrorRevision: errorRevision, SchemaVersion: SchemaVersion, NodeID: string(m.Config.Consensus.LocalID), Values: config.ConfigurationNumbers(m.Config), Fields: config.EditableConfigurationFields(), TaskID: m.StartedOverride.TaskID}
	if m.Repository != nil {
		policy := m.Repository.ClusterPolicy()
		fields := []config.ConfigurationField{}
		for _, f := range n.Fields {
			if !store.ConfigurationPolicyOverrides(policy, f.Path) {
				fields = append(fields, f)
			}
		}
		n.Fields = fields
	}
	if m.Authority == nil {
		return n, fmt.Errorf("controller consensus required")
	}
	s := m.Authority.Status(ctx)
	n.Ready = s.Enabled && s.LeaderKnown && s.AppliedIndex >= s.CommitIndex && (s.Role == "follower" || (s.Role == "leader" && s.QuorumConfirmed))
	b, e := os.ReadFile(m.ConfigPath)
	if e != nil {
		return n, e
	}
	base := config.ConfigurationFileDigest(b)
	if base != m.StartedBaseHash {
		n.Error = "configuration file changed outside this process"
		return n, nil
	}
	v, e := config.ReadConfigurationOverrides(config.ConfigurationOverridePath(m.Config.MetadataPath))
	if e != nil {
		return n, e
	}
	if !m.Active() && !reflect.DeepEqual(v, m.StartedOverride) {
		n.Error = "configuration override changed outside this process"
		return n, nil
	}
	// A saved file is not the configuration of the still-running old process.
	n.Fingerprint = digest(struct {
		Base     string
		Override config.ConfigurationOverrides
	}{base, v})
	if m.Restart != nil {
		n.RestartAvailable = m.Restart.Ready(ctx) == nil
	}
	return n, nil
}
func (m *Manager) Candidate(ctx context.Context, changes map[string]int) error {
	n, e := m.Local(ctx)
	if e != nil {
		return e
	}
	if n.Error != "" {
		return fmt.Errorf("configuration file changed outside this process")
	}
	if m.Repository != nil {
		for key := range changes {
			if store.ConfigurationPolicyOverrides(m.Repository.ClusterPolicy(), key) {
				return fmt.Errorf("edit the dynamic cluster policy instead of its startup default")
			}
		}
	}
	c, e := config.ValidateConfigurationCandidate(m.ConfigPath, changes)
	if e != nil {
		return e
	}
	for _, timing := range []struct {
		enabled                        bool
		interval, observations, window int
	}{{c.MySQL.AutomaticFailoverEnabled, c.MySQL.DiscoveryIntervalSeconds, c.MySQL.AutomaticFailoverMinimumObservations, c.MySQL.AutomaticFailoverFailureWindowSeconds}, {c.PostgreSQL.AutomaticFailoverEnabled, c.PostgreSQL.DiscoveryIntervalSeconds, c.PostgreSQL.AutomaticFailoverMinimumObservations, c.PostgreSQL.AutomaticFailoverFailureWindowSeconds}} {
		if timing.enabled && timing.window < timing.interval*(timing.observations-1) {
			return fmt.Errorf("failure evidence window cannot contain the required observations")
		}
	}
	if c.Agent.Enabled && c.Agent.CommandTimeoutSeconds > c.Agent.MutationTimeoutSeconds {
		return fmt.Errorf("command timeout exceeds mutation timeout")
	}
	if m.Validate != nil {
		return m.Validate(c)
	}
	return nil
}
func (m *Manager) Status(ctx context.Context) (Status, error) {
	n, e := m.Local(ctx)
	if e != nil {
		return Status{}, e
	}
	s := Status{SchemaVersion: SchemaVersion, Local: n, Tasks: m.Repository.ConfigurationTasks()}
	sort.Slice(s.Tasks, func(i, j int) bool { return s.Tasks[i].CreatedAt.After(s.Tasks[j].CreatedAt) })
	if m.Authority != nil {
		members, e := m.Authority.ControllerMembers(ctx)
		if e == nil {
			s.Members = members
		} else {
			for _, t := range s.Tasks {
				for _, x := range t.Targets {
					s.Members = append(s.Members, consensus.ControllerMember{ResourceID: model.ResourceID(x.NodeID), APIAddress: x.APIAddress})
				}
				break
			}
		}
	}
	return s, nil
}
func (m *Manager) Plan(ctx context.Context, r Request) (Plan, error) {
	if e := config.ValidateConfigurationChanges(r.Changes); e != nil {
		return Plan{}, e
	}
	if m.Authority == nil || m.Peer == nil {
		return Plan{}, fmt.Errorf("controller configuration distribution unavailable")
	}
	if e := m.Authority.RequireMutationAuthority(ctx); e != nil {
		return Plan{}, e
	}
	if m.Maintenance != nil {
		if e := m.Maintenance.Check(ctx); e != nil {
			return Plan{}, e
		}
	}
	members, e := m.Authority.ControllerMembers(ctx)
	if e != nil {
		return Plan{}, e
	}
	if len(members) < 3 {
		return Plan{}, fmt.Errorf("rolling configuration requires at least three voters")
	}
	want := map[string]bool{}
	for _, id := range r.NodeIDs {
		if want[id] || !model.ValidResourceID(model.ResourceID(id)) {
			return Plan{}, fmt.Errorf("invalid or duplicate controller target")
		}
		want[id] = true
	}
	if len(want) == 0 {
		return Plan{}, fmt.Errorf("select controller targets")
	}
	if config.ConfigurationRequiresAllVoters(r.Changes) && len(want) != len(members) {
		return Plan{}, fmt.Errorf("cluster-scoped parameters require all current voters")
	}
	timeout := m.Config.ConfigurationDistribution.EffectiveStepTimeoutSeconds()
	if timeout < 30 || timeout > 3600 {
		return Plan{}, fmt.Errorf("invalid configuration step timeout")
	}
	plan := Plan{StepTimeoutSeconds: timeout, PolicyDigest: store.ConfigurationPolicyDigest(m.Repository.ClusterPolicy()), Changes: r.Changes, RestartRequired: true, Targets: []store.ConfigurationTarget{}}
	sort.Slice(members, func(i, j int) bool { return members[i].ResourceID < members[j].ResourceID })
	for _, member := range members {
		id := string(member.ResourceID)
		plan.Members = append(plan.Members, id)
		n, e := m.Peer.Node(ctx, member)
		if e != nil || n.NodeID != id || n.SchemaVersion != SchemaVersion || !n.Ready || !n.RestartAvailable || n.Error != "" {
			return Plan{}, fmt.Errorf("controller %s is not ready for configuration distribution", id)
		}
		if want[id] {
			if e = m.Peer.Candidate(ctx, member, r.Changes); e != nil {
				return Plan{}, fmt.Errorf("controller %s candidate validation failed: %w", id, e)
			}
			plan.Targets = append(plan.Targets, store.ConfigurationTarget{NodeID: id, APIAddress: member.APIAddress, Fingerprint: n.Fingerprint, Before: n.Values, Status: "pending"})
			delete(want, id)
		}
	}
	if len(want) > 0 {
		return Plan{}, fmt.Errorf("configuration target is not a current voter")
	}
	plan.Hash = digest(plan)
	return plan, nil
}
func (m *Manager) Dispatch(ctx context.Context, r Request, actor string) (store.ConfigurationTask, error) {
	if !model.ValidResourceID(model.ResourceID(r.RequestID)) {
		return store.ConfigurationTask{}, fmt.Errorf("request_id must be a UUID")
	}
	if old, ok := m.Repository.ConfigurationTask(r.RequestID); ok {
		ids := []string{}
		for _, n := range old.Targets {
			ids = append(ids, n.NodeID)
		}
		given := append([]string{}, r.NodeIDs...)
		sort.Strings(given)
		sort.Strings(ids)
		if r.PlanHash != old.PlanHash || !reflect.DeepEqual(old.Changes, r.Changes) || !reflect.DeepEqual(ids, given) {
			return old, fmt.Errorf("request identity already binds different changes")
		}
		return old, nil
	}
	if m.Active() {
		return store.ConfigurationTask{}, fmt.Errorf("configuration maintenance is active")
	}
	plan, e := m.Plan(ctx, r)
	if e != nil {
		return store.ConfigurationTask{}, e
	}
	if r.PlanHash == "" || r.PlanHash != plan.Hash {
		return store.ConfigurationTask{}, fmt.Errorf("configuration preflight changed; review again")
	}
	return m.Repository.CreateConfigurationTask(store.ConfigurationTask{TaskID: r.RequestID, StepTimeoutSeconds: plan.StepTimeoutSeconds, Changes: plan.Changes, Targets: plan.Targets, Members: plan.Members, PlanHash: plan.Hash, PolicyDigest: plan.PolicyDigest, Actor: actor})
}
func (m *Manager) Recover(ctx context.Context, id, mode string, revision uint64) (store.ConfigurationTask, error) {
	if e := m.Authority.RequireMutationAuthority(ctx); e != nil {
		return store.ConfigurationTask{}, e
	}
	if m.Maintenance != nil {
		if e := m.Maintenance.Check(ctx); e != nil {
			return store.ConfigurationTask{}, e
		}
	}
	t, ok := m.Repository.ConfigurationTask(id)
	if !ok || !t.Active || t.Status != "failed" || t.Revision != revision {
		return t, fmt.Errorf("configuration recovery subject/revision changed")
	}
	if mode != "retry" && mode != "rollback" {
		return t, fmt.Errorf("invalid configuration recovery mode")
	}
	m.mu.Lock()
	m.localError = ""
	m.mu.Unlock()
	t.Status = "running"
	t.Error = ""
	t.StepStartedAt = m.now()
	if mode == "rollback" {
		t.Mode = "rollback"
		t.CurrentNode = ""
		for i := range t.Targets {
			t.Targets[i].Status = "pending"
			t.Targets[i].Error = ""
		}
	} else {
		for i := range t.Targets {
			if t.Targets[i].NodeID == t.CurrentNode {
				t.Targets[i].Status = "applying"
				t.Targets[i].Error = ""
			}
		}
	}
	return m.Repository.UpdateConfigurationTask(t, revision)
}
