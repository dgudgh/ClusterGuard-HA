package mysql

import (
	"context"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type fakeRunner struct {
	queries []string
}

func (runner *fakeRunner) Query(_ context.Context, _ adapter.Endpoint, _ adapter.Credentials, query string) (string, error) {
	runner.queries = append(runner.queries, query)
	if strings.Contains(query, "server_uuid") {
		return "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\tmysql-renamed\t192.0.2.20\t3310\t22\t8.0.44\t1\t1\n", nil
	}
	return "1\t1\t1\n", nil
}

func TestDiscoverUsesMySQLServerUUIDNotHostnameAsIdentity(t *testing.T) {
	runner := &fakeRunner{}
	adapter := New(runner)
	result, err := adapter.Discover(context.Background(), adapterRequest())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if result.Instance.Engine != model.EngineMySQL || result.Instance.EngineIdentity["server_uuid"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("unexpected native identity: %+v", result.Instance)
	}
	if result.Instance.Hostname != "mysql-renamed" || result.Instance.IPAddress != "192.0.2.20" || result.Instance.Port != 3310 {
		t.Fatalf("unexpected discovered endpoint: %+v", result.Instance)
	}
	if result.Instance.Role != model.RoleReplica || result.Instance.Health.State != model.HealthHealthy {
		t.Fatalf("unexpected discovered role/health: %+v", result.Instance)
	}
}

func TestHealthIsReadOnlyAndDoesNotAdvertiseExecution(t *testing.T) {
	runner := &fakeRunner{}
	adapter := New(runner)
	health, err := adapter.Health(context.Background(), adapterRequest())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health.State != model.HealthHealthy || !strings.Contains(health.Summary, "read-only") {
		t.Fatalf("unexpected health: %+v", health)
	}
	if adapter.Capabilities(context.Background()).Supports(adapterpkgCapabilityExecute()) {
		t.Fatalf("MySQL phase one must not advertise mutation execution")
	}
}

func adapterRequest() adapter.DiscoverRequest {
	return adapter.DiscoverRequest{
		ClusterID:   model.NewResourceID(),
		Endpoint:    adapter.Endpoint{Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306},
		Credentials: adapter.Credentials{Username: "monitor", Password: "secret"},
	}
}

func adapterpkgCapabilityExecute() adapter.Capability { return adapter.CapabilityExecute }
