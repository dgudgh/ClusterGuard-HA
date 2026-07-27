package main

import (
	"context"
	"path/filepath"
	"testing"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/model"
)

type inertCommandRunner struct{}

func (inertCommandRunner) Run(context.Context, string, ...string) ([]byte, error) { return nil, nil }

func TestRuntimeControllersConfigurePostgreSQLCommandAndReconcilePaths(t *testing.T) {
	clusterID := model.NewResourceID()
	configuration := agent.Config{
		SharedSecret: "agent-secret", MutationStateDirectory: filepath.Join(t.TempDir(), "mutations"),
		Clusters: map[model.ResourceID]agent.ClusterPolicy{
			clusterID: {ClusterID: clusterID, InstanceID: model.NewResourceID(), Engine: model.EnginePostgreSQL},
		},
	}
	controllers, err := newRuntimeControllers(configuration, inertCommandRunner{})
	if err != nil || controllers.postgresql == nil || controllers.oracle == nil || controllers.vip == nil || controllers.roles == nil {
		t.Fatalf("runtime controllers=%+v err=%v", controllers, err)
	}
	ledger, err := newRuntimeMutationLedger(configuration)
	if err != nil || ledger == nil {
		t.Fatalf("runtime mutation ledger=%+v err=%v", ledger, err)
	}
	if _, err := agent.NewService(
		configuration, controllers.vip, controllers.roles, nil,
		agent.WithPostgreSQLController(controllers.postgresql),
		agent.WithOracleController(controllers.oracle),
		agent.WithMutationLedger(ledger),
	); err != nil {
		t.Fatalf("PostgreSQL command service is not wired: %v", err)
	}
}
