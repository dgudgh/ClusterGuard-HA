package main

import (
	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/model"
	"context"
	"fmt"
	"strings"
	"testing"
)

type runtimePGGatewayRunner struct {
	stopped  bool
	writable bool
	calls    []string
}

func (r *runtimePGGatewayRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	command := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, command)
	switch {
	case strings.Contains(command, "systemctl stop"):
		r.stopped = true
		return nil, nil
	case strings.Contains(command, "systemctl show"):
		if r.stopped {
			return []byte("inactive\n"), nil
		}
		return []byte("active\n"), nil
	case strings.Contains(command, "SELECT pg_is_in_recovery()"):
		return []byte("f\n"), nil
	case strings.Contains(command, "SELECT NOT pg_is_in_recovery()"):
		if r.writable {
			return []byte("t\n"), nil
		}
		return []byte("f\n"), nil
	default:
		return nil, fmt.Errorf("unexpected command %s", command)
	}
}

type runtimePGGatewayDecision struct{ response agent.ReconcileResponse }

func (d runtimePGGatewayDecision) Decision(context.Context, agent.ClusterPolicy) (agent.ReconcileResponse, error) {
	return d.response, nil
}
func TestRuntimeControllerWiresPostgreSQLGatewayWritability(t *testing.T) {
	for _, writable := range []bool{true, false} {
		t.Run(fmt.Sprint(writable), func(t *testing.T) {
			runner := &runtimePGGatewayRunner{writable: writable}
			controllers, err := newRuntimeControllers(agent.Config{MySQLBinary: "/usr/bin/mysql", DockerBinary: "/usr/bin/docker", RoleStateDirectory: t.TempDir()}, runner)
			if err != nil {
				t.Fatal(err)
			}
			policy := agent.ClusterPolicy{ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID(), Engine: model.EnginePostgreSQL, EndpointProvider: model.EndpointProviderGateway, RuntimeKind: model.RuntimeLinux, PostgreSQLUser: "postgres", PostgreSQLPassfile: "/data/postgresql/5432/pgpass", PostgreSQLBinaryDirectory: "/usr/pgsql-16/bin", PostgreSQLPort: 5432, PostgreSQLService: "clusterguard-postgresql-5432.service", PostgreSQLDatabase: "postgres"}
			d := runtimePGGatewayDecision{agent.ReconcileResponse{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: agent.ReconcileKeepVIP, EndpointProvider: model.EndpointProviderGateway, LeaseID: model.NewResourceID()}}
			// Use exactly the production constructor and runtime wrapper, not the native
			// controller directly. The runner rejects any unexpected VIP/MySQL call.
			r := agent.NewReconciler(controllers.vip, controllers.roles, d, agent.WithPostgreSQLReconcileController(controllers.postgresql))
			_, err = r.ReconcileAll(context.Background(), map[model.ResourceID]agent.ClusterPolicy{policy.ClusterID: policy})
			if writable && (err != nil || runner.stopped) {
				t.Fatalf("real runtime wiring rejected writer: stopped=%v err=%v", runner.stopped, err)
			}
			if !writable && (err == nil || !runner.stopped) {
				t.Fatalf("read-only primary not hard fenced: stopped=%v err=%v", runner.stopped, err)
			}
		})
	}
}
