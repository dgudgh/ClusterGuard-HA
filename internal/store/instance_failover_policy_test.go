package store

import (
	"clusterguard.io/ha/pkg/model"
	"errors"
	"path/filepath"
	"testing"
)

func instancePolicyFixture(t *testing.T, r *Repository) (model.DatabaseCluster, model.DatabaseInstance) {
	t.Helper()
	c, e := r.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "preferences"})
	if e != nil {
		t.Fatal(e)
	}
	x, e := r.ReconcileInstance(model.DatabaseInstance{ClusterID: c.ResourceID, Engine: c.Engine, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}, IPAddress: "127.0.0.1", Port: 3306})
	if e != nil {
		t.Fatal(e)
	}
	return c, x.Instance
}
func TestInstanceFailoverPolicyPersistsClonesAndSurvivesClusterUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	r, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	c, i := instancePolicyFixture(t, r)
	p := model.InstanceFailoverPolicy{AutomaticEnabled: true, Instances: map[model.ResourceID]model.InstanceFailoverPreference{i.ResourceID: {AutomaticEligible: false, Weight: 900}}}
	got, e := r.PutInstanceFailoverPolicy(c.ResourceID, 0, p, "admin")
	if e != nil {
		t.Fatal(e)
	}
	p.Instances[i.ResourceID] = model.InstanceFailoverPreference{}
	got.Instances[i.ResourceID] = model.InstanceFailoverPreference{}
	current, _ := r.Cluster(c.ResourceID)
	if current.FailoverPolicy.Preference(i.ResourceID).Weight != 900 {
		t.Fatal("caller aliased persisted policy")
	}
	current.FailoverPolicy.Instances[i.ResourceID] = model.InstanceFailoverPreference{}
	c.DisplayName = "renamed"
	if _, e = r.UpsertCluster(c); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	current, _ = reopened.Cluster(c.ResourceID)
	if current.FailoverPolicy == nil || current.FailoverPolicy.Revision != 1 || !current.FailoverPolicy.AutomaticEnabled || current.FailoverPolicy.Preference(i.ResourceID).AutomaticEligible || current.FailoverPolicy.Preference(i.ResourceID).Weight != 900 {
		t.Fatalf("lost policy: %+v", current.FailoverPolicy)
	}
	if current.FailoverPolicy.Preference(model.NewResourceID()).AutomaticEligible {
		t.Fatal("new unconfigured instance was automatically enrolled")
	}
	if len(reopened.Audits()) != 1 {
		t.Fatal("policy audit missing after restart")
	}
	if _, e = reopened.PutInstanceFailoverPolicy(c.ResourceID, 0, *current.FailoverPolicy, "admin"); !errors.Is(e, ErrConflict) {
		t.Fatal("stale policy revision accepted", e)
	}
}
func TestInstanceFailoverPolicyRejectsForeignInstancesAndInvalidWeights(t *testing.T) {
	r := NewMemory()
	c, i := instancePolicyFixture(t, r)
	other, e := r.UpsertCluster(model.DatabaseCluster{Engine: model.EnginePostgreSQL, DisplayName: "other"})
	if e != nil {
		t.Fatal(e)
	}
	for _, test := range []struct {
		id     model.ResourceID
		weight int
	}{{i.ResourceID, -1}, {i.ResourceID, 1001}, {model.NewResourceID(), 100}} {
		_, e = r.PutInstanceFailoverPolicy(c.ResourceID, 0, model.InstanceFailoverPolicy{Instances: map[model.ResourceID]model.InstanceFailoverPreference{test.id: {AutomaticEligible: true, Weight: test.weight}}}, "admin")
		if !errors.Is(e, ErrValidation) {
			t.Fatal("invalid preference accepted", e)
		}
	}
	if _, e = r.PutInstanceFailoverPolicy(other.ResourceID, 0, model.InstanceFailoverPolicy{Instances: map[model.ResourceID]model.InstanceFailoverPreference{i.ResourceID: {Weight: 100}}}, "admin"); !errors.Is(e, ErrValidation) {
		t.Fatal("cross-cluster/engine instance accepted", e)
	}
	if len(r.Audits()) != 0 {
		t.Fatal("rejected input was audited as saved")
	}
}
func TestInstanceFailoverPolicyConsensusFailureIsAtomicAndReplicates(t *testing.T) {
	r := NewMemory()
	c, i := instancePolicyFixture(t, r)
	follower := NewMemory()
	initial, e := r.ReplicatedState()
	if e != nil {
		t.Fatal(e)
	}
	if e = follower.ApplyReplicatedState(initial); e != nil {
		t.Fatal(e)
	}
	consensus := &snapshotConsensusStub{apply: r.ApplyReplicatedState}
	if e = r.SetSnapshotConsensus(consensus); e != nil {
		t.Fatal(e)
	}
	p := model.InstanceFailoverPolicy{AutomaticEnabled: false, Instances: map[model.ResourceID]model.InstanceFailoverPreference{i.ResourceID: {Weight: 800}}}
	if _, e = r.PutInstanceFailoverPolicy(c.ResourceID, 0, p, "admin"); e != nil {
		t.Fatal(e)
	}
	if len(consensus.commits) != 1 {
		t.Fatal("policy and audit are not one commit")
	}
	if e = follower.ApplyReplicatedState(consensus.commits[0]); e != nil {
		t.Fatal(e)
	}
	fc, _ := follower.Cluster(c.ResourceID)
	if fc.FailoverPolicy.Preference(i.ResourceID).Weight != 800 || len(follower.Audits()) != 1 {
		t.Fatal("replica lost policy/audit")
	}
	consensus.err = errors.New("quorum lost")
	p.AutomaticEnabled = true
	if _, e = r.PutInstanceFailoverPolicy(c.ResourceID, 1, p, "admin"); e == nil {
		t.Fatal("failed consensus accepted")
	}
	current, _ := r.Cluster(c.ResourceID)
	if current.FailoverPolicy.AutomaticEnabled || current.FailoverPolicy.Revision != 1 || len(r.Audits()) != 1 {
		t.Fatal("failed commit published partial state")
	}
}

func TestInstanceFailoverPolicyRejectsActiveOperationAndCorruptPersistedPreferences(t *testing.T) {
	r := NewMemory()
	c, i := instancePolicyFixture(t, r)
	p := model.InstanceFailoverPolicy{Instances: map[model.ResourceID]model.InstanceFailoverPreference{i.ResourceID: {Weight: 100}}}
	op, _, e := r.CreateOperation(model.OperationRecord{Operation: model.Operation{ClusterID: c.ResourceID, Engine: c.Engine, Kind: model.OperationSwitchover, RequestedBy: "admin"}, TargetID: i.ResourceID, IdempotencyKey: "policy-active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.TransitionOperation(op.ResourceID, op.MetadataRevision, model.OperationTransition{Stage: model.StageDiscover, Status: model.OperationRunning}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.PutInstanceFailoverPolicy(c.ResourceID, 0, p, "admin"); !errors.Is(e, ErrConflict) {
		t.Fatal("changed automatic authority while operation running", e)
	}
	clean := NewMemory()
	cc, ii := instancePolicyFixture(t, clean)
	p.Instances = map[model.ResourceID]model.InstanceFailoverPreference{ii.ResourceID: {Weight: 100}}
	if _, e = clean.PutInstanceFailoverPolicy(cc.ResourceID, 0, p, "admin"); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*model.InstanceFailoverPolicy){func(p *model.InstanceFailoverPolicy) { p.Revision = 0 }, func(p *model.InstanceFailoverPolicy) {
		p.Instances[ii.ResourceID] = model.InstanceFailoverPreference{Weight: 1001}
	}, func(p *model.InstanceFailoverPolicy) { p.UpdatedBy = "" }} {
		state := cloneDiscoverySnapshot(clean.snapshot)
		cluster := state.Clusters[cc.ResourceID]
		cluster = cloneCluster(cluster)
		mutate(cluster.FailoverPolicy)
		state.Clusters[cc.ResourceID] = cluster
		if _, e = normalizeSnapshot(state); e == nil {
			t.Fatal("corrupt stored policy accepted")
		}
	}
}
