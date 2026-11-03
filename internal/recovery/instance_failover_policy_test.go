package recovery

import (
	"clusterguard.io/ha/pkg/model"
	"context"
	"testing"
	"time"
)

func TestPerClusterAutomationCanEnableStartupOffAndNeverPreemptsHealthyPrimary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		startup bool
		policy  *model.InstanceFailoverPolicy
		healthy bool
		want    int
	}{
		{"legacy enabled", true, nil, false, 1}, {"legacy disabled", false, nil, false, 0}, {"explicit off", true, &model.InstanceFailoverPolicy{AutomaticEnabled: false}, false, 0}, {"hot on", false, &model.InstanceFailoverPolicy{AutomaticEnabled: true}, false, 1}, {"no preemption", true, &model.InstanceFailoverPolicy{AutomaticEnabled: true}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			cluster, snapshot, target := recoveryFixture(now)
			cluster.FailoverPolicy = tc.policy
			if tc.healthy {
				snapshot.Instances[0].Health.State = model.HealthHealthy
			}
			executor := &recoveryExecutorStub{}
			c := NewController(recoveryStateStub{clusters: []model.DatabaseCluster{cluster}, snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot}}, recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: now.Add(-30 * time.Second)}}, recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: target}}, executor, recoveryAuthorityStub{}, time.Second, func() time.Time { return now }, WithDefaultAutomationEnabled(tc.startup))
			if err := c.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			requests, _ := executor.calls()
			if len(requests) != tc.want {
				t.Fatalf("automatic executions=%d want=%d", len(requests), tc.want)
			}
		})
	}
}
