package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

// validateClusterPolicy is called twice on purpose: once when the console
// submits a policy and again while normalizing any snapshot, including one
// replicated from a peer. This test pins the shared bounds directly, because
// removing either call site still leaves the other one rejecting the same
// policy.
func TestValidateClusterPolicySharesTheConfigurationBounds(t *testing.T) {
	if err := validateClusterPolicy(ClusterPolicy{
		Engines: map[string]ClusterEnginePolicy{"mysql": {AutomaticFailoverMinimumObservations: 2, AutomaticFailoverFailureWindowSeconds: 1, AutomaticFailoverOperationTimeoutSeconds: 30}},
	}); err != nil {
		t.Fatalf("the lower bound of every range must be accepted: %v", err)
	}
	for _, invalid := range []ClusterPolicy{
		{Engines: map[string]ClusterEnginePolicy{"mysql": {AutomaticFailoverMinimumObservations: 1}}},
		{Engines: map[string]ClusterEnginePolicy{"mysql": {AutomaticFailoverMinimumObservations: 101}}},
		{Engines: map[string]ClusterEnginePolicy{"mysql": {AutomaticFailoverFailureWindowSeconds: 3601}}},
		{Engines: map[string]ClusterEnginePolicy{"mysql": {AutomaticFailoverOperationTimeoutSeconds: 29}}},
		{Engines: map[string]ClusterEnginePolicy{"oracle": {AutomaticFailoverMinimumObservations: 6}}},
	} {
		if err := validateClusterPolicy(invalid); !errors.Is(err, ErrValidation) {
			t.Fatalf("invalid policy accepted: %+v", invalid)
		}
	}
}

func TestClusterPolicyIsEmptyUntilOneIsStored(t *testing.T) {
	repository := NewMemory()
	if policy := repository.ClusterPolicy(); len(policy.Engines) != 0 {
		t.Fatalf("a fresh cluster must not invent a policy: %+v", policy)
	}
	if settings := repository.ClusterEnginePolicy(model.EngineMySQL); settings != (ClusterEnginePolicy{}) {
		t.Fatalf("an unset engine must read as unset: %+v", settings)
	}
	if repository.AutomaticFailoverSuppressed(model.EngineMySQL) {
		t.Fatal("maintenance suppression must default to off")
	}
}

func TestPutClusterPolicyPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PutClusterPolicy(ClusterPolicy{
		Engines: map[string]ClusterEnginePolicy{
			string(model.EngineMySQL): {
				AutomaticFailoverMinimumObservations:     6,
				AutomaticFailoverFailureWindowSeconds:    30,
				AutomaticFailoverOperationTimeoutSeconds: 600,
			},
		},
	}, "ops@example"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings := reopened.ClusterEnginePolicy(model.EngineMySQL)
	if settings.AutomaticFailoverMinimumObservations != 6 ||
		settings.AutomaticFailoverFailureWindowSeconds != 30 ||
		settings.AutomaticFailoverOperationTimeoutSeconds != 600 {
		t.Fatalf("restart lost the replicated policy: %+v", settings)
	}
	if reopened.ClusterPolicy().UpdatedBy != "ops@example" {
		t.Fatalf("the actor must be stored with the policy: %+v", reopened.ClusterPolicy())
	}
	// A policy for one engine must not silently leak into another.
	if reopened.ClusterEnginePolicy(model.EnginePostgreSQL) != (ClusterEnginePolicy{}) {
		t.Fatal("policy leaked across engines")
	}
}

func TestPutClusterPolicyRejectsOutOfRangeAndUnsupportedEngines(t *testing.T) {
	repository := NewMemory()
	if _, err := repository.PutClusterPolicy(ClusterPolicy{
		Engines: map[string]ClusterEnginePolicy{"mysql": {AutomaticFailoverMinimumObservations: 1}},
	}, "ops"); !errors.Is(err, ErrValidation) {
		t.Fatalf("one observation cannot reject a single missed probe: %v", err)
	}
	if _, err := repository.PutClusterPolicy(ClusterPolicy{
		Engines: map[string]ClusterEnginePolicy{"mysql": {AutomaticFailoverOperationTimeoutSeconds: 5}},
	}, "ops"); !errors.Is(err, ErrValidation) {
		t.Fatalf("an operation budget shorter than the verification stage: %v", err)
	}
	// Automatic failover does not exist for these engines, so a policy for them
	// would be inert and is refused rather than stored.
	if _, err := repository.PutClusterPolicy(ClusterPolicy{
		Engines: map[string]ClusterEnginePolicy{"oracle": {AutomaticFailoverMinimumObservations: 6}},
	}, "ops"); !errors.Is(err, ErrValidation) {
		t.Fatalf("unsupported engine policy error = %v", err)
	}
	if policy := repository.ClusterPolicy(); len(policy.Engines) != 0 {
		t.Fatalf("a rejected policy must not be stored: %+v", policy)
	}
}

func TestPutClusterPolicyWithNoEnginesClearsEveryOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PutClusterPolicy(ClusterPolicy{
		Engines: map[string]ClusterEnginePolicy{"mysql": {AutomaticFailoverSuppressed: true}},
	}, "ops"); err != nil {
		t.Fatal(err)
	}
	if !repository.AutomaticFailoverSuppressed(model.EngineMySQL) {
		t.Fatal("suppression was not stored")
	}
	if _, err := repository.PutClusterPolicy(ClusterPolicy{}, "ops"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.AutomaticFailoverSuppressed(model.EngineMySQL) {
		t.Fatal("clearing the policy must restore the configured behaviour")
	}
	if len(reopened.ClusterPolicy().Engines) != 0 {
		t.Fatalf("cleared policy still has engines: %+v", reopened.ClusterPolicy())
	}
	if audits := reopened.Audits(); len(audits) != 2 || !strings.Contains(audits[1].Message, "清除集群策略") {
		t.Fatalf("policy clear and its audit must survive the same restart: %+v", audits)
	}
}

func TestPutClusterPolicyCommitsPolicyAndAuditAtomically(t *testing.T) {
	repository := NewMemory()
	consensus := &snapshotConsensusStub{apply: repository.ApplyReplicatedState}
	if err := repository.SetSnapshotConsensus(consensus); err != nil {
		t.Fatal(err)
	}
	policy := ClusterPolicy{Engines: map[string]ClusterEnginePolicy{"mysql": {AutomaticFailoverSuppressed: true}}}
	if _, err := repository.PutClusterPolicy(policy, "ops"); err != nil {
		t.Fatal(err)
	}
	if len(consensus.commits) != 1 || !repository.AutomaticFailoverSuppressed(model.EngineMySQL) {
		t.Fatalf("policy and audit need one replicated commit, commits=%d", len(consensus.commits))
	}
	if audits := repository.Audits(); len(audits) != 1 || !strings.Contains(audits[0].Message, "维护抑制") {
		t.Fatalf("committed policy lacks audit: %+v", audits)
	}

	consensus.err = errors.New("quorum unavailable")
	if _, err := repository.PutClusterPolicy(ClusterPolicy{}, "ops"); err == nil {
		t.Fatal("failed consensus commit was reported as a successful clear")
	}
	if !repository.AutomaticFailoverSuppressed(model.EngineMySQL) || len(repository.Audits()) != 1 {
		t.Fatalf("failed consensus commit published half a change: policy=%+v audits=%+v", repository.ClusterPolicy(), repository.Audits())
	}
}

func TestPutClusterPolicyRejectsUnauditableActor(t *testing.T) {
	repository := NewMemory()
	if _, err := repository.PutClusterPolicy(ClusterPolicy{Engines: map[string]ClusterEnginePolicy{"mysql": {AutomaticFailoverSuppressed: true}}}, strings.Repeat("x", maximumAuditActorLength+1)); !errors.Is(err, ErrValidation) {
		t.Fatalf("policy changed without an auditable actor: %v", err)
	}
	if len(repository.ClusterPolicy().Engines) != 0 || len(repository.Audits()) != 0 {
		t.Fatal("rejected actor must not publish a policy or audit")
	}
}

func TestClusterPolicyReplicatesToFollowers(t *testing.T) {
	leader, follower := NewMemory(), NewMemory()
	consensus := &snapshotConsensusStub{apply: func(state []byte) error {
		if err := leader.ApplyReplicatedState(state); err != nil {
			return err
		}
		return follower.ApplyReplicatedState(state)
	}}
	if err := leader.SetSnapshotConsensus(consensus); err != nil {
		t.Fatal(err)
	}
	if _, err := leader.PutClusterPolicy(ClusterPolicy{
		Engines: map[string]ClusterEnginePolicy{"postgresql": {AutomaticFailoverSuppressed: true}},
	}, "ops"); err != nil {
		t.Fatal(err)
	}
	if !follower.AutomaticFailoverSuppressed(model.EnginePostgreSQL) {
		t.Fatal("the follower did not apply the replicated policy")
	}
	if audits := follower.Audits(); len(audits) != 1 || audits[0].Actor != "ops" {
		t.Fatalf("the follower did not apply the same audit: %+v", audits)
	}
}

func TestClusterPolicyAccessorReturnsACopy(t *testing.T) {
	repository := NewMemory()
	if _, err := repository.PutClusterPolicy(ClusterPolicy{
		Engines: map[string]ClusterEnginePolicy{"mysql": {AutomaticFailoverMinimumObservations: 6}},
	}, "ops"); err != nil {
		t.Fatal(err)
	}
	policy := repository.ClusterPolicy()
	policy.Engines["mysql"] = ClusterEnginePolicy{AutomaticFailoverMinimumObservations: 2}
	policy.Engines["postgresql"] = ClusterEnginePolicy{}
	if repository.ClusterEnginePolicy(model.EngineMySQL).AutomaticFailoverMinimumObservations != 6 {
		t.Fatal("the accessor handed out the live map")
	}
	if len(repository.ClusterPolicy().Engines) != 1 {
		t.Fatal("a caller mutated the stored policy")
	}
}
