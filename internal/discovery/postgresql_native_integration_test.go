package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	pgadapter "clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type nativePostgreSQLObservation struct {
	snapshot model.TopologySnapshot
	err      error
}

type nativePostgreSQLObserver struct {
	service *Service
	results chan nativePostgreSQLObservation
}

func (observer nativePostgreSQLObserver) Refresh(ctx context.Context, id model.ResourceID) (model.TopologySnapshot, error) {
	snapshot, err := observer.service.Refresh(ctx, id)
	select {
	case observer.results <- nativePostgreSQLObservation{snapshot, err}:
	case <-ctx.Done():
	}
	return snapshot, err
}

func TestPostgreSQLNativeStreamingDiscoveryFiftyBackgroundCycles(t *testing.T) {
	bin := os.Getenv("CG_PG16_BIN")
	if bin == "" {
		t.Skip("set CG_PG16_BIN to an isolated PostgreSQL 16 bin directory")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("CG_PG16_BIN must be absolute")
	}
	run := func(binary string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, filepath.Join(bin, binary), args...)
		command.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "LC_ALL=C", "PGCONNECT_TIMEOUT=5"}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated %s failed: %v\n%s", binary, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	if version := run("postgres", "--version"); !strings.Contains(version, " 16.") {
		t.Fatalf("PostgreSQL 16 is required, got %s", version)
	}
	root := t.TempDir()
	ports := make(map[string]int)
	dirs := make(map[string]string)
	natives := map[string]string{"pg02": pgReplayPrimaryNativeID, "pg01": pgReplayStandbyOneNativeID, "pg03": pgReplayStandbyThreeNativeID}
	for _, name := range []string{"pg02", "pg01", "pg03"} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports[name] = listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		dirs[name] = filepath.Join(root, name)
	}
	query := func(name, sql string) string {
		return run("psql", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-h", "127.0.0.1", "-p", strconv.Itoa(ports[name]), "-U", "cg_fixture", "-d", "postgres", "-c", sql)
	}
	start := func(name, hint string) {
		t.Helper()
		configuration := fmt.Sprintf("\nlisten_addresses = '127.0.0.1'\nport = %d\nunix_socket_directories = ''\nmax_wal_senders = 10\nmax_replication_slots = 10\nwal_level = replica\nclusterguard.node_id = '%s'\nclusterguard.primary_node_id = '%s'\n", ports[name], natives[name], hint)
		file, err := os.OpenFile(filepath.Join(dirs[name], "postgresql.auto.conf"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.WriteString(configuration)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("write fixture config: %v %v", writeErr, closeErr)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, filepath.Join(bin, "pg_ctl"), "-D", dirs[name], "-m", "fast", "-w", "-t", "15", "stop").CombinedOutput()
			if err != nil {
				t.Errorf("stop isolated %s: %v %s", name, err, output)
			}
		})
		run("pg_ctl", "-D", dirs[name], "-l", filepath.Join(root, name+".log"), "-w", "-t", "20", "start")
	}
	run("initdb", "-D", dirs["pg02"], "-U", "cg_fixture", "-A", "trust", "--locale=C", "-E", "UTF8")
	start("pg02", "")
	for _, name := range []string{"pg01", "pg03"} {
		slot := "cg_slot_" + name
		query("pg02", "SELECT pg_create_physical_replication_slot('"+slot+"')")
		connection := fmt.Sprintf("host=127.0.0.1 port=%d user=cg_fixture application_name=%s", ports["pg02"], natives[name])
		run("pg_basebackup", "-d", connection, "-D", dirs[name], "-R", "-X", "stream", "-S", slot, "--checkpoint=fast")
		hint := ""
		if name == "pg03" {
			hint = pgReplayStandbyOneNativeID
		}
		start(name, hint)
	}
	deadline := time.Now().Add(20 * time.Second)
	for query("pg02", "SELECT count(*) FROM pg_stat_replication WHERE state = 'streaming'") != "2" {
		if time.Now().After(deadline) {
			t.Fatal("isolated standbys did not begin streaming")
		}
		time.Sleep(100 * time.Millisecond)
	}
	systemID := query("pg02", "SELECT (pg_control_system()).system_identifier::text")
	timeline := query("pg02", "SELECT (pg_control_checkpoint()).timeline_id::text")
	assertNative := func(snapshot model.TopologySnapshot) {
		t.Helper()
		if snapshot.Health.State != model.HealthHealthy || len(snapshot.Instances) != 3 || len(snapshot.Links) != 2 || len(snapshot.Probes) != 3 {
			t.Fatalf("native topology not complete and healthy: %+v", snapshot)
		}
		var primaryID model.ResourceID
		standbyIDs := make(map[model.ResourceID]bool)
		for _, instance := range snapshot.Instances {
			if instance.Health.State != model.HealthHealthy || instance.EngineIdentity["resource_id"] != natives[instance.Hostname] ||
				instance.EngineIdentity["system_identifier"] != systemID || instance.EngineMetadata["timeline_id"] != timeline {
				t.Fatalf("native identity or health mismatch: %+v", instance)
			}
			if instance.Hostname == "pg02" && instance.Role == model.RolePrimary {
				primaryID = instance.ResourceID
			} else if instance.Role == model.RoleStandby && instance.Replication.SourceIdentity["resource_id"] == pgReplayPrimaryNativeID && instance.PromotionEligible {
				standbyIDs[instance.ResourceID] = true
			} else {
				t.Fatalf("native role or upstream mismatch: %+v", instance)
			}
		}
		for _, link := range snapshot.Links {
			if !link.Healthy || link.SourceInstanceID != primaryID || !standbyIDs[link.TargetInstanceID] {
				t.Fatalf("unexpected native replication link: %+v", link)
			}
			delete(standbyIDs, link.TargetInstanceID)
		}
		if len(standbyIDs) != 0 {
			t.Fatal("native standby missing its replication link")
		}
	}
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EnginePostgreSQL, DisplayName: "isolated-native-pg16"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pg01", "pg02", "pg03"} {
		_, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Active: true, Hostname: name, IPAddress: "127.0.0.1", Port: ports[name]})
		if err != nil {
			t.Fatal(err)
		}
	}
	registry := adapter.NewRegistry()
	if err := registry.Register(pgadapter.New(pgadapter.CLIQueryRunner{Binary: filepath.Join(bin, "psql")})); err != nil {
		t.Fatal(err)
	}
	service := New(registry, repository, CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "cg_fixture", Database: "postgres"}, nil
	}), nil)
	observer := nativePostgreSQLObserver{service: service, results: make(chan nativePostgreSQLObservation, 128)}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		NewScheduler(repository, observer, nil, 100*time.Millisecond, 4*time.Second).Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	var last time.Time
	for round := 1; round <= 50; round++ {
		select {
		case result := <-observer.results:
			if result.err != nil {
				t.Fatalf("native background cycle %d: %v", round, result.err)
			}
			assertNative(result.snapshot)
			if !result.snapshot.ObservedAt.After(last) {
				t.Fatalf("cycle %d did not publish a new observation", round)
			}
			last = result.snapshot.ObservedAt
			encoded, err := json.Marshal(result.snapshot)
			if err != nil || strings.Contains(string(encoded), "replication_senders") || strings.Contains(string(encoded), "TopologyEvidence") {
				t.Fatal("adapter-local replication evidence leaked into topology JSON")
			}
		case <-ctx.Done():
			t.Fatalf("timed out after %d native background cycles", round-1)
		}
	}
	if query("pg01", "SELECT current_setting('clusterguard.primary_node_id', true)") != "" ||
		query("pg03", "SELECT current_setting('clusterguard.primary_node_id', true)") != pgReplayStandbyOneNativeID {
		t.Fatal("read-only discovery changed the database's upstream GUC")
	}
	query("pg01", "SELECT pg_wal_replay_pause()")
	for {
		select {
		case result := <-observer.results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.snapshot.Health.State == model.HealthDegraded && len(result.snapshot.Links) == 1 {
				t.Log("PostgreSQL 16: 50 distinct background cycles healthy with 2 links; paused replay degraded and retired its link")
				return
			}
		case <-ctx.Done():
			t.Fatal("paused replay did not degrade topology")
		}
	}
}
