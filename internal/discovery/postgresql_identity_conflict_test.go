package discovery

import (
	"context"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestPostgreSQLTopologyDuplicateWritersCannotCollapseHealthy(t *testing.T) {
	fixture := newPostgreSQLTopologyFixture(t)
	fixture.runner.setRow("pg01", pgReplayPrimaryRow(pgReplayPrimaryNativeID))
	snapshot, err := fixture.service.Refresh(context.Background(), fixture.cluster.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Health.State != model.HealthDegraded || len(snapshot.Links) != 0 {
		t.Fatalf("duplicate writers collapsed into a healthy primary: %+v", snapshot)
	}
	found := false
	for _, anomaly := range snapshot.Anomalies {
		if anomaly.Kind == "duplicate_native_identity" && anomaly.Severity == "critical" {
			found = true
		}
	}
	if !found {
		t.Fatal("duplicate native identities were not published as a critical anomaly")
	}
}
