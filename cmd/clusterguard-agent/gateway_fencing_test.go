package main

import (
	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/model"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type gatewayFenceRunner struct {
	stopped bool
	calls   []string
	data    string
}

func (r *gatewayFenceRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	c := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, c)
	switch {
	case strings.Contains(c, "/ip ") || strings.Contains(c, "arping"):
		return nil, fmt.Errorf("database gateway policy must never operate a host VIP: %s", c)
	case strings.Contains(c, "systemctl stop"):
		r.stopped = true
		return nil, nil
	case strings.Contains(c, "systemctl show"):
		if r.stopped {
			return []byte("inactive\n"), nil
		}
		return []byte("active\n"), nil
	case strings.Contains(c, "systemctl is-active"):
		if r.stopped {
			return []byte("inactive\n"), fmt.Errorf("inactive")
		}
		return []byte("active\n"), nil
	case strings.Contains(c, "--verbose --help"):
		return []byte("read-only TRUE\nsuper-read-only TRUE\ndatadir " + r.data + "\n"), nil
	case strings.Contains(c, "SELECT SUBSTRING_INDEX"):
		if r.stopped {
			return nil, fmt.Errorf("database stopped")
		}
		return []byte("8\n"), nil
	case strings.Contains(c, "SET PERSIST_ONLY"), strings.Contains(c, "SET GLOBAL"):
		if r.stopped {
			return nil, fmt.Errorf("database stopped")
		}
		return nil, nil
	case strings.Contains(c, "SELECT @@"):
		if r.stopped {
			return nil, fmt.Errorf("database stopped")
		}
		return []byte("1\t1\n"), nil
	default:
		return nil, fmt.Errorf("unexpected native command: %s", c)
	}
}

func TestRuntimeGatewaySignedDatabaseIsolation(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		for _, stopped := range []bool{true, false} {
			t.Run(string(engine)+fmt.Sprint(stopped), func(t *testing.T) {
				root := t.TempDir()
				runner := &gatewayFenceRunner{stopped: stopped, data: root}
				id := model.NewResourceID()
				p := agent.ClusterPolicy{ClusterID: id, InstanceID: model.NewResourceID(), Engine: engine, EndpointProvider: model.EndpointProviderGateway, RuntimeKind: model.RuntimeLinux, MySQLService: "mysql.service", MySQLServerBinary: "/usr/local/mysql/bin/mysqld", MySQLServerDefaultsFile: "/data/my.cnf", MySQLDefaultsFile: "/data/client.cnf", MySQLPort: 3306, PostgreSQLService: "postgresql.service"}
				cfg := agent.Config{SharedSecret: "test-secret", MySQLBinary: "/usr/local/mysql/bin/mysql", IPBinary: "/sbin/ip", ARPingBinary: "/usr/sbin/arping", DockerBinary: "/usr/bin/docker", RoleStateDirectory: filepath.Join(root, "roles"), MutationStateDirectory: filepath.Join(root, "mutations"), Clusters: map[model.ResourceID]agent.ClusterPolicy{id: p}}
				ctrls, err := newRuntimeControllers(cfg, runner)
				if err != nil {
					t.Fatal(err)
				}
				ledger, err := newRuntimeMutationLedger(cfg)
				if err != nil {
					t.Fatal(err)
				}
				svc, err := agent.NewService(cfg, ctrls.vip, ctrls.roles, nil, agent.WithPostgreSQLController(ctrls.postgresql), agent.WithPowerController(ctrls.power), agent.WithMutationLedger(ledger))
				if err != nil {
					t.Fatal(err)
				}
				send := func(command string) agent.Response {
					req := agent.Request{Command: command, Engine: engine, ClusterID: id, OperationID: model.NewResourceID(), LeaseID: model.NewResourceID(), PlanDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
					req.Signature, err = agent.SignRequest(req, cfg.SharedSecret)
					if err != nil {
						t.Fatal(err)
					}
					return svc.Handle(context.Background(), req)
				}
				got := send(agent.CommandVIPStatus)
				if got.Status != agent.StatusOK || got.OwnsVIP == nil || *got.OwnsVIP {
					t.Fatalf("gateway database VIP status: %+v", got)
				}
				got = send(agent.CommandSelfIsolate)
				if got.Status != agent.StatusOK {
					t.Fatalf("native self-isolation: %+v; calls %v", got, runner.calls)
				}
				if engine == model.EngineMySQL {
					got = send(agent.CommandRoleStatus)
					if got.Status != agent.StatusOK || got.RestartReadOnly == nil || !*got.RestartReadOnly || got.PersistedReadOnly == nil || !*got.PersistedReadOnly {
						t.Fatalf("durable native role evidence: %+v", got)
					}
				} else {
					got = send(agent.CommandPostgreSQLStatus)
					if got.Status != agent.StatusOK || got.ServiceRunning == nil || *got.ServiceRunning {
						t.Fatalf("native PG stop evidence: %+v", got)
					}
				}
				got = send(agent.CommandVIPAcquire)
				if got.Status == agent.StatusOK {
					t.Fatal("gateway database acquired a VIP")
				}
				for _, c := range runner.calls {
					if strings.Contains(c, "/ip ") || strings.Contains(c, "arping") {
						t.Fatalf("host VIP command: %s", c)
					}
				}
			})
		}
	}
}
