package configuration

import (
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"
)

// Initialize captures only the configuration that this process actually loaded.
func (m *Manager) Initialize() error {
	b, e := os.ReadFile(m.ConfigPath)
	if e != nil {
		return e
	}
	m.StartedBaseHash = config.ConfigurationFileDigest(b)
	if m.Config.LoadedConfigurationDigest != "" {
		m.StartedBaseHash = m.Config.LoadedConfigurationDigest
	}
	m.StartedOverride = m.Config.AppliedConfiguration
	return nil
}
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			step, cancel := context.WithTimeout(ctx, 8*time.Second)
			m.Tick(step)
			cancel()
		}
	}
}
func (m *Manager) Tick(ctx context.Context) {
	if m.Authority == nil || m.Peer == nil || m.Restart == nil {
		return
	}
	for _, t := range m.Repository.ConfigurationTasks() {
		if !t.Active || t.Status != "running" {
			continue
		}
		m.workerRevision = t.Revision
		m.applyLocal(ctx, t)
		m.advance(ctx, t.TaskID)
		return
	}
}
func marker(t store.ConfigurationTask) string {
	if t.Mode == "rollback" {
		return t.TaskID + ".rollback"
	}
	return t.TaskID
}
func matches(values, want map[string]int) bool {
	for k, v := range want {
		n, ok := values[k]
		if !ok || n != v {
			return false
		}
	}
	return true
}
func (m *Manager) localFailure(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.localError = err.Error()
	m.localRevision = m.workerRevision
}
func (m *Manager) applyLocal(ctx context.Context, t store.ConfigurationTask) {
	if t.CurrentNode != string(m.Config.Consensus.LocalID) || m.StartedOverride.TaskID == marker(t) {
		return
	}
	if m.Maintenance != nil {
		if e := m.Maintenance.Check(ctx); e != nil {
			return
		}
	}
	n, e := m.Local(ctx)
	if e != nil || !n.Ready {
		return
	}
	if n.Error != "" {
		m.localFailure(fmt.Errorf("configuration base changed"))
		return
	}
	var target store.ConfigurationTarget
	for _, v := range t.Targets {
		if v.NodeID == t.CurrentNode {
			target = v
		}
	}
	if target.Status != "applying" {
		return
	}
	leader := m.Authority.Status(ctx)
	permit := Permit{TaskID: t.TaskID, Revision: t.Revision, NodeID: t.CurrentNode}
	if m.Peer.Permit(ctx, consensus.ControllerMember{ResourceID: leader.LeaderID, APIAddress: leader.LeaderAPIAddress}, permit) != nil {
		return
	}
	path := config.ConfigurationOverridePath(m.Config.MetadataPath)
	current, e := config.ReadConfigurationOverrides(path)
	if e != nil {
		m.localFailure(e)
		return
	}
	previous := current
	validateSaved := func() error {
		loaded, err := config.Load(m.ConfigPath)
		if err != nil {
			return fmt.Errorf("saved configuration validation failed: %w", err)
		}
		if loaded.LoadedConfigurationDigest != m.StartedBaseHash || loaded.AppliedConfiguration.TaskID != marker(t) {
			return fmt.Errorf("saved configuration ownership or base changed")
		}
		expected := map[string]int{}
		for key, value := range target.Before {
			expected[key] = value
		}
		if t.Mode == "apply" {
			for key, value := range t.Changes {
				expected[key] = value
			}
		}
		if !matches(config.ConfigurationNumbers(loaded), expected) {
			return fmt.Errorf("saved configuration differs from approved complete candidate")
		}
		if m.Validate != nil {
			return m.Validate(loaded)
		}
		return nil
	}
	desired := marker(t)
	publishing := current.TaskID != desired
	if publishing {
		backup := filepath.Join(filepath.Dir(path), "configuration-"+t.TaskID+".before.json")
		if t.Mode == "apply" {
			if n.Fingerprint != target.Fingerprint {
				m.localFailure(fmt.Errorf("configuration preflight changed"))
				return
			}
			if e = m.Candidate(ctx, t.Changes); e != nil {
				m.localFailure(e)
				return
			}
			old := current
			old.SchemaVersion = 1
			old.TaskID = t.TaskID + ".before"
			if old.Values == nil {
				old.Values = map[string]int{}
			}
			old.Digest = config.ConfigurationDigest(old.Values)
			if e = saveBackup(backup, old); e != nil {
				m.localFailure(e)
				return
			}
			values := map[string]int{}
			for k, v := range current.Values {
				values[k] = v
			}
			for k, v := range t.Changes {
				values[k] = v
			}
			current.Values = values
		} else {
			// Validation failures may already have restored the untouched prestate.
			if n.Fingerprint == target.Fingerprint && matches(n.Values, target.Before) {
				return
			}
			old, err := config.ReadConfigurationOverrides(backup)
			if err != nil {
				m.localFailure(err)
				return
			}
			if old.TaskID == "" {
				// An untouched node needs no restart or file change to be rolled back.
				if n.Fingerprint == target.Fingerprint && matches(n.Values, target.Before) {
					return
				}
				m.localFailure(fmt.Errorf("configuration rollback backup unavailable"))
				return
			}
			if old.TaskID != t.TaskID+".before" || (current.TaskID != t.TaskID && current.TaskID != desired) {
				m.localFailure(fmt.Errorf("configuration rollback ownership changed"))
				return
			}
			current.Values = old.Values
		}
		current.SchemaVersion = 1
		current.TaskID = desired
		current.Digest = config.ConfigurationDigest(current.Values)
		// Check task ownership again immediately before publishing the file.
		latest, ok := m.Repository.ConfigurationTask(t.TaskID)
		if !ok || latest.Revision != t.Revision || !latest.Active || latest.Status != "running" {
			return
		}
		if e = publishValidatedOverrides(path, previous, current, validateSaved); e != nil {
			m.localFailure(e)
			return
		}
	}
	if !publishing {
		if e = validateSaved(); e != nil {
			m.localFailure(e)
			return
		}
	}
	if m.restartRequested == fmt.Sprintf("%s/%d", desired, t.Revision) {
		return
	}
	if m.Peer.Permit(ctx, consensus.ControllerMember{ResourceID: leader.LeaderID, APIAddress: leader.LeaderAPIAddress}, permit) != nil {
		return
	}
	if e = m.Restart.RestartController(ctx); e != nil {
		m.localFailure(fmt.Errorf("controller restart request failed"))
		return
	}
	m.mu.Lock()
	m.localError = ""
	m.mu.Unlock()
	m.restartRequested = fmt.Sprintf("%s/%d", desired, t.Revision)
}
func saveBackup(path string, v config.ConfigurationOverrides) error {
	if old, e := config.ReadConfigurationOverrides(path); e != nil {
		return e
	} else if old.TaskID != "" {
		if !reflect.DeepEqual(old, v) {
			return fmt.Errorf("immutable configuration backup conflicts")
		}
		return nil
	}
	b, _ := json.Marshal(v)
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(append(b, '\n'))
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func (m *Manager) advance(ctx context.Context, id string) {
	if m.Authority.RequireMutationAuthority(ctx) != nil {
		return
	}
	if m.Maintenance != nil && m.Maintenance.Check(ctx) != nil {
		return
	}
	t, ok := m.Repository.ConfigurationTask(id)
	if !ok || !t.Active || t.Status != "running" {
		return
	}
	members, e := m.Authority.ControllerMembers(ctx)
	if e != nil {
		return
	}
	ids := []string{}
	for _, v := range members {
		ids = append(ids, string(v.ResourceID))
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, t.Members) {
		m.fail(t, "controller membership changed")
		return
	}
	seconds := t.StepTimeoutSeconds
	if seconds == 0 {
		seconds = config.DefaultConfigurationStepTimeoutSeconds
	} // legacy persisted task only
	timeout := time.Duration(seconds) * time.Second
	if t.CurrentNode != "" && m.now().Sub(t.StepStartedAt) > timeout {
		m.fail(t, "controller restart or actual-value verification timed out")
		return
	}
	// No second controller may be restarted while another voter is unavailable.
	nodes := map[string]Node{}
	memberMap := map[string]consensus.ControllerMember{}
	for _, member := range members {
		key := string(member.ResourceID)
		memberMap[key] = member
		n, err := m.Peer.Node(ctx, member)
		if err != nil || !n.Ready || n.NodeID != key || n.SchemaVersion != SchemaVersion {
			if key != t.CurrentNode {
				return
			}
			continue
		}
		nodes[key] = n
	}
	if t.CurrentNode != "" {
		var index int
		for i, n := range t.Targets {
			if n.NodeID == t.CurrentNode {
				index = i
			}
		}
		n, present := nodes[t.CurrentNode]
		target := t.Targets[index]
		expected := map[string]int{}
		for key, value := range target.Before {
			expected[key] = value
		}
		for key, value := range t.Changes {
			expected[key] = value
		}
		if t.Mode == "rollback" {
			expected = target.Before
		}
		untouched := t.Mode == "rollback" && n.Fingerprint == target.Fingerprint && matches(n.Values, expected)
		if present && matches(n.Values, expected) && (n.TaskID == marker(t) || untouched) {
			t.Targets[index].Status = "applied"
			if t.Mode == "rollback" {
				t.Targets[index].Status = "rolled_back"
			}
			t.Targets[index].Error = ""
			t.CurrentNode = ""
			t.StepStartedAt = time.Time{}
		} else {
			if present && n.TaskError != "" && n.TaskErrorRevision == t.Revision {
				m.fail(t, n.TaskError)
				return
			}
			if m.now().Sub(t.StepStartedAt) > timeout {
				m.fail(t, "controller restart or actual-value verification timed out")
			}
			return
		}
	}
	leader := string(m.Authority.Status(ctx).LeaderID)
	selected := -1
	for i, n := range t.Targets {
		if n.Status == "pending" && n.NodeID != leader {
			selected = i
			break
		}
	}
	if selected < 0 {
		for i, n := range t.Targets {
			if n.Status == "pending" {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		t.Status = "succeeded"
		if t.Mode == "rollback" {
			t.Status = "rolled_back"
		}
		t.Active = false
		t.Error = ""
	} else {
		target := t.Targets[selected]
		n, present := nodes[target.NodeID]
		member := memberMap[target.NodeID]
		if !present || !n.RestartAvailable || member.APIAddress != target.APIAddress {
			return
		}
		if t.Mode == "apply" && n.Fingerprint != target.Fingerprint {
			m.fail(t, "configuration preflight changed")
			return
		}
		t.CurrentNode = target.NodeID
		t.Targets[selected].Status = "applying"
		t.StepStartedAt = m.now()
	}
	_, _ = m.Repository.UpdateConfigurationTask(t, t.Revision)
}
func (m *Manager) fail(t store.ConfigurationTask, message string) {
	t.Status = "failed"
	t.Error = message
	for i, n := range t.Targets {
		if n.NodeID == t.CurrentNode {
			t.Targets[i].Status = "failed"
			t.Targets[i].Error = message
		}
	}
	_, _ = m.Repository.UpdateConfigurationTask(t, t.Revision)
}
