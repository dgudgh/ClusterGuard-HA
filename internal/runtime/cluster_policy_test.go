package runtime

import (
	"testing"
	"time"

	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

// These tests exist because "the console can change it" is only true if the
// running process actually reads the change. A policy that required a restart
// would be indistinguishable from the configuration file it is meant to
// replace.

func clusterPolicyMySQLConfiguration() config.File {
	return config.File{
		MySQL: config.MySQL{
			Enabled: true, AutomaticFailoverEnabled: true,
			DiscoveryIntervalSeconds: 1, DiscoveryTimeoutSeconds: 1,
			// Deliberately strict: six observations spanning thirty seconds.
			AutomaticFailoverMinimumObservations: 6, AutomaticFailoverFailureWindowSeconds: 30,
		},
	}
}

func TestClusterPolicyRetunesTheEvidenceWindowWithoutARestart(t *testing.T) {
	cluster := model.NewResourceID()
	configuration := clusterPolicyMySQLConfiguration()
	repository := store.NewMemory()
	evidence := newConfiguredFailureEvidence(configuration, staticEngineResolver{cluster: model.EngineMySQL},
		clusterPolicyProvider(repository))

	start := time.Date(2026, time.July, 13, 20, 0, 0, 0, time.UTC)
	record := func(at time.Duration) { evidence.Record(cluster, true, start.Add(at)) }

	// Under the configured window (6 observations / 30 seconds) three probes are
	// nowhere near enough.
	record(0)
	record(time.Second)
	record(2 * time.Second)
	if evidence.Stable(cluster, start.Add(2*time.Second)) {
		t.Fatal("three observations satisfied a six-observation window")
	}

	// The console relaxes the policy to two observations across two seconds.
	if _, err := repository.PutClusterPolicy(store.ClusterPolicy{
		Engines: map[string]store.ClusterEnginePolicy{
			string(model.EngineMySQL): {
				AutomaticFailoverMinimumObservations:  2,
				AutomaticFailoverFailureWindowSeconds: 2,
			},
		},
	}, "ops"); err != nil {
		t.Fatal(err)
	}
	// The relaxation must not retroactively authorize the series that was
	// collected under the stricter window: evidence gathered under six
	// observations is not evidence under two.
	if evidence.Stable(cluster, start.Add(2*time.Second)) {
		t.Fatal("a relaxed policy authorized evidence it did not collect")
	}
	record(3 * time.Second)
	record(5 * time.Second)
	if !evidence.Stable(cluster, start.Add(5*time.Second)) {
		t.Fatal("the relaxed policy never took effect on the running process")
	}
	if _, ok := evidence.Incident(cluster, start.Add(5*time.Second)); !ok {
		t.Fatal("the relaxed policy produced no incident")
	}

	// Tightening again must re-arm the window instead of leaving the old series
	// standing.
	if _, err := repository.PutClusterPolicy(store.ClusterPolicy{
		Engines: map[string]store.ClusterEnginePolicy{
			string(model.EngineMySQL): {
				AutomaticFailoverMinimumObservations:  8,
				AutomaticFailoverFailureWindowSeconds: 60,
			},
		},
	}, "ops"); err != nil {
		t.Fatal(err)
	}
	if evidence.Stable(cluster, start.Add(5*time.Second)) {
		t.Fatal("a tightened policy left the previous series authorized")
	}
}

func TestClusterPolicyClearingRestoresTheConfiguredWindow(t *testing.T) {
	cluster := model.NewResourceID()
	configuration := clusterPolicyMySQLConfiguration()
	repository := store.NewMemory()
	if _, err := repository.PutClusterPolicy(store.ClusterPolicy{
		Engines: map[string]store.ClusterEnginePolicy{
			string(model.EngineMySQL): {AutomaticFailoverMinimumObservations: 2, AutomaticFailoverFailureWindowSeconds: 2},
		},
	}, "ops"); err != nil {
		t.Fatal(err)
	}
	evidence := newConfiguredFailureEvidence(configuration, staticEngineResolver{cluster: model.EngineMySQL},
		clusterPolicyProvider(repository))
	start := time.Date(2026, time.July, 13, 20, 0, 0, 0, time.UTC)
	evidence.Record(cluster, true, start)
	evidence.Record(cluster, true, start.Add(2*time.Second))
	if !evidence.Stable(cluster, start.Add(2*time.Second)) {
		t.Fatal("the override did not apply")
	}
	if _, err := repository.PutClusterPolicy(store.ClusterPolicy{}, "ops"); err != nil {
		t.Fatal(err)
	}
	evidence.Record(cluster, true, start.Add(4*time.Second))
	if evidence.Stable(cluster, start.Add(4*time.Second)) {
		t.Fatal("clearing the policy must return the cluster to its configured window")
	}
}

func TestAutomaticFailoverProfileOverridesOneFieldAtATime(t *testing.T) {
	configuration := clusterPolicyMySQLConfiguration()
	// Only the observation count is overridden; the window must keep the
	// configured thirty seconds.
	profile := automaticFailoverProfileWithPolicy(configuration, model.EngineMySQL,
		store.ClusterEnginePolicy{AutomaticFailoverMinimumObservations: 3})
	if profile.observations != 3 {
		t.Fatalf("observations=%d, want 3", profile.observations)
	}
	if profile.duration != 30*time.Second {
		t.Fatalf("an unset policy field must not clear the configured window: %s", profile.duration)
	}
	if profile.checks != 2 {
		t.Fatalf("checks=%d, want 2", profile.checks)
	}
	// An empty policy reproduces the configured profile exactly.
	if empty := automaticFailoverProfileWithPolicy(configuration, model.EngineMySQL, store.ClusterEnginePolicy{}); empty != automaticFailoverMySQLProfile(configuration) {
		t.Fatalf("an empty policy changed behaviour: %+v", empty)
	}
}

func TestAutomaticFailoverPolicyOperationBudgetFallsBackToConfiguration(t *testing.T) {
	configuration := clusterPolicyMySQLConfiguration()
	configuration.MySQL.AutomaticFailoverOperationTimeoutSeconds = 120
	if got := automaticFailoverPolicyOperationTimeout(configuration, model.EngineMySQL, store.ClusterEnginePolicy{}); got != 120*time.Second {
		t.Fatalf("empty policy budget=%s, want 120s", got)
	}
	settings := store.ClusterEnginePolicy{AutomaticFailoverOperationTimeoutSeconds: 600}
	if got := automaticFailoverPolicyOperationTimeout(configuration, model.EngineMySQL, settings); got != 600*time.Second {
		t.Fatalf("policy budget=%s, want 600s", got)
	}
}

func TestClusterPolicyProviderIsAbsentWithoutAStore(t *testing.T) {
	if clusterPolicyProvider(nil) != nil {
		t.Fatal("a runtime without a store must not invent a policy provider")
	}
}
