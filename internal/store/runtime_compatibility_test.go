package store

import (
	"bytes"
	"clusterguard.io/ha/internal/runtimecompat"
	"clusterguard.io/ha/pkg/model"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeFloorPersistsAndCannotDecrease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, i := instancePolicyFixture(t, r)
	before, err := r.ReplicatedState()
	if err != nil {
		t.Fatal(err)
	}
	policy := model.InstanceFailoverPolicy{AutomaticEnabled: true, Instances: map[model.ResourceID]model.InstanceFailoverPreference{i.ResourceID: {AutomaticEligible: true, Weight: 913}}}
	if _, err = r.PutInstanceFailoverPolicy(c.ResourceID, 0, policy, "tester"); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(filepath.Dir(path), "config.json")
	cfg, _ := json.Marshal(map[string]string{"metadata_path": path})
	os.WriteFile(cfgPath, cfg, 0600)
	actual, e := runtimecompat.ReadFromConfig(cfgPath)
	if e != nil || actual != model.PolicyCompatibilityFloor() {
		t.Fatal("privileged metadata reader differs from committed state", actual, e)
	}
	state, err := r.ReplicatedState()
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(state, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw["runtime_compatibility_floor"]) == 0 || r.RuntimeCompatibilityRequirements() != model.PolicyCompatibilityFloor() {
		t.Fatal("floor not atomically stored")
	}
	follower := NewMemory()
	if err = follower.ApplyReplicatedState(state); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	policy.AutomaticEnabled = false
	if _, err = reopened.PutInstanceFailoverPolicy(c.ResourceID, 1, policy, "tester"); err != nil {
		t.Fatal(err)
	}
	if reopened.RuntimeCompatibilityRequirements() != model.PolicyCompatibilityFloor() {
		t.Fatal("disabling removed floor")
	}
	if _, err = reopened.RetireCluster(c.ResourceID, c.DisplayName, "tester"); err != nil {
		t.Fatal(err)
	}
	if reopened.RuntimeCompatibilityRequirements() != model.PolicyCompatibilityFloor() {
		t.Fatal("retirement removed floor")
	}
	if err = reopened.RestoreReplicatedState(before); err == nil {
		t.Fatal("old snapshot lowered floor")
	}
	if err = follower.RestoreReplicatedState(before); err == nil {
		t.Fatal("replica restore lowered floor")
	}
}
func TestLegacyPolicyRequiresCompatibilityAndFloorCannotBeStripped(t *testing.T) {
	r := NewMemory()
	c, i := instancePolicyFixture(t, r)
	p := model.InstanceFailoverPolicy{Instances: map[model.ResourceID]model.InstanceFailoverPreference{i.ResourceID: {Weight: 100}}}
	if _, e := r.PutInstanceFailoverPolicy(c.ResourceID, 0, p, "test"); e != nil {
		t.Fatal(e)
	}
	stripped := cloneDiscoverySnapshot(r.snapshot)
	stripped.RuntimeCompatibilityFloor = nil
	r.mu.Lock()
	err := r.commitSnapshotLocked(stripped)
	r.mu.Unlock()
	if err == nil {
		t.Fatal("explicit floor stripped even though policy still exists")
	}
	r.snapshot.RuntimeCompatibilityFloor = nil // model legacy pre-floor bytes, not an exposed mutation
	if r.RuntimeCompatibilityRequirements() != model.PolicyCompatibilityFloor() {
		t.Fatal("legacy disabled policy unprotected")
	}
	if _, e := r.RetireCluster(c.ResourceID, c.DisplayName, "test"); e != nil {
		t.Fatal(e)
	}
	if r.RuntimeCompatibilityRequirements() != model.PolicyCompatibilityFloor() {
		t.Fatal("legacy retirement lost floor")
	}
}
func TestRuntimeFloorUnknownAndMetadataCorruptionFailClosed(t *testing.T) {
	for _, floor := range []string{`{"protocol":2,"instance_failover_policy":1}`, `{"protocol":1,"instance_failover_policy":2}`, `{"protocol":1,"instance_failover_policy":1,"future":1}`} {
		r := NewMemory()
		b, _ := r.ReplicatedState()
		var raw map[string]json.RawMessage
		json.Unmarshal(b, &raw)
		raw["runtime_compatibility_floor"] = json.RawMessage(floor)
		delete(raw, snapshotStateDigestField)
		delete(raw, snapshotStateRevisionField)
		b, _ = json.Marshal(raw)
		if e := r.ApplyReplicatedState(b); e == nil {
			t.Fatal("unknown floor admitted", floor)
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"metadata_path":"/no/such/metadata"}`), 0600)
	if _, e := runtimecompat.ReadFromConfig(path); e == nil {
		t.Fatal("unknown metadata became empty floor")
	}
}

func TestProtectedRuntimeStateCannotHideBehindPreviousGeneration(t *testing.T) {
	for _, scenario := range []string{"corrupt-floor", "future-floor", "corrupt-legacy-policy"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			r, e := Open(path)
			if e != nil {
				t.Fatal(e)
			}
			c, i := instancePolicyFixture(t, r)
			if _, e = r.PutInstanceFailoverPolicy(c.ResourceID, 0, model.InstanceFailoverPolicy{Instances: map[model.ResourceID]model.InstanceFailoverPreference{i.ResourceID: {Weight: 100}}}, "test"); e != nil {
				t.Fatal(e)
			}
			data, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if scenario == "future-floor" {
				data = bytes.Replace(data, []byte(`"protocol":1`), []byte(`"protocol":2`), 1)
			} else if scenario == "corrupt-legacy-policy" {
				var raw map[string]json.RawMessage
				json.Unmarshal(data, &raw)
				delete(raw, "runtime_compatibility_floor")
				data, _ = json.Marshal(raw)
				data = append(data, '[')
			} else {
				data = append(data, '[')
			}
			if e = os.WriteFile(path, data, 0600); e != nil {
				t.Fatal(e)
			}
			if _, _, e = readSnapshotFile(previousSnapshotPath(path)); e != nil {
				t.Fatal("fallback fixture must be valid", e)
			}
			if _, e = Open(path); e == nil {
				t.Fatal("protected bad/unknown state recovered from an older generation")
			}
		})
	}
}
