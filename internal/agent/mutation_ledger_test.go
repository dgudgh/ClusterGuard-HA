package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func mutationLedgerFixture() (Request, ClusterPolicy) {
	clusterID := model.NewResourceID()
	policy := ClusterPolicy{ClusterID: clusterID, InstanceID: model.NewResourceID()}
	request := Request{
		Command: CommandPostgreSQLPromote, Engine: model.EnginePostgreSQL,
		ClusterID: clusterID, OperationID: model.NewResourceID(), LeaseID: model.NewResourceID(),
		PlanDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	return request, policy
}

func TestFileMutationLedgerReplaysCompletedResultAcrossProcessRestart(t *testing.T) {
	directory := t.TempDir()
	request, policy := mutationLedgerFixture()
	ledger, err := NewFileMutationLedger(directory)
	if err != nil {
		t.Fatalf("new mutation ledger: %v", err)
	}
	if response, replayed, err := ledger.Begin(request, policy); err != nil || replayed || response.Status != "" {
		t.Fatalf("first begin response=%+v replayed=%t err=%v", response, replayed, err)
	}
	want := Response{Status: StatusOK, Message: "PostgreSQL standby promoted", ClusterID: policy.ClusterID, InstanceID: policy.InstanceID}
	if err := ledger.Complete(request, policy, want); err != nil {
		t.Fatalf("complete mutation: %v", err)
	}

	restarted, err := NewFileMutationLedger(directory)
	if err != nil {
		t.Fatalf("restart mutation ledger: %v", err)
	}
	got, replayed, err := restarted.Begin(request, policy)
	if err != nil || !replayed || got != want {
		t.Fatalf("replayed response=%+v replayed=%t err=%v", got, replayed, err)
	}
}

func TestFileMutationLedgerFailsClosedForUnknownOutcomeAndIntentMismatch(t *testing.T) {
	request, policy := mutationLedgerFixture()
	ledger, err := NewFileMutationLedger(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Begin(request, policy); err != nil {
		t.Fatalf("first begin: %v", err)
	}
	if _, _, err := ledger.Begin(request, policy); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("in-flight replay was not blocked: %v", err)
	}

	changed := request
	changed.LeaseID = model.NewResourceID()
	if _, _, err := ledger.Begin(changed, policy); err == nil || !strings.Contains(err.Error(), "intent") {
		t.Fatalf("changed mutation intent was not blocked: %v", err)
	}
}

func TestFileMutationLedgerUsesPrivateRegularFiles(t *testing.T) {
	directory := t.TempDir()
	request, policy := mutationLedgerFixture()
	ledger, err := NewFileMutationLedger(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Begin(request, policy); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ledger entries=%v err=%v", entries, err)
	}
	info, err := os.Lstat(filepath.Join(directory, entries[0].Name()))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode=%v err=%v", info.Mode(), err)
	}
}
