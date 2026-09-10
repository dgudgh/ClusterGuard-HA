package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

// Explicitly opted-in field diagnostics never stop/start a service or change
// storage. The real controller must independently prove that it is offline.
func TestRecoveryPostgreSQLReadOnlyStoppedDockerEvidence(t *testing.T) {
	path := os.Getenv("CG_PG_RECOVERY_READONLY_CONFIG")
	id := model.ResourceID(os.Getenv("CG_PG_RECOVERY_READONLY_CLUSTER"))
	if path == "" || id == "" {
		t.Skip("explicit read-only Agent config and cluster required")
	}
	if !model.ValidResourceID(id) {
		t.Fatal("read-only inspection requires a valid cluster ID")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config fileConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal("invalid read-only Agent configuration")
	}
	var policies []ClusterPolicy
	for _, policy := range config.Clusters {
		if policy.ClusterID == id {
			policies = append(policies, policy)
		}
	}
	if len(policies) != 1 || policies[0].Engine != model.EnginePostgreSQL || policies[0].RuntimeKind != model.RuntimeDocker {
		t.Fatal("read-only target must be one allowlisted Docker PostgreSQL member")
	}
	policy := policies[0]
	if err := validatePostgreSQLPolicy(&policy); err != nil {
		t.Fatal(err)
	}
	controller, err := NewDockerPostgreSQLController(OSCommandRunner{}, config.DockerBinary, config.DockerConfigDirectory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	evidence, err := controller.RecoveryInspect(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := controller.RecoveryInspect(ctx, policy)
	if err != nil || second.Fingerprint != evidence.Fingerprint || !evidence.Complete || !evidence.Fenced {
		t.Fatalf("offline evidence is not complete and stable: %v", err)
	}
	// Current checkpoint WAL must be decodable without starting PostgreSQL.
	request := model.RecoveryWALRequest{Fingerprint: evidence.Fingerprint, Timeline: evidence.Timeline, Start: evidence.Redo, End: evidence.Position}
	if _, err := controller.RecoveryWAL(ctx, policy, request); err != nil {
		t.Fatal(err)
	}
	request.Fingerprint = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := controller.RecoveryWAL(ctx, policy, request); err == nil {
		t.Fatal("old evidence fingerprint must not authorize a WAL comparison")
	}
	t.Logf("read-only offline evidence verified: instance=%s native=%s system=%s timeline=%d redo=%s end=%s", evidence.InstanceID, evidence.NativeID, evidence.SystemIdentifier, evidence.Timeline, evidence.Redo, evidence.Position)
}
