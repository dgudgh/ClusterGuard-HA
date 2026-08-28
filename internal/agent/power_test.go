package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func powerTestPolicy() ClusterPolicy {
	return ClusterPolicy{
		ClusterID:         "cluster-power",
		InstanceID:        "instance-power-a",
		Engine:            "mysql",
		MySQLService:      "mysqld",
		MySQLBinary:       "/usr/bin/mysql",
		MySQLDefaultsFile: "/etc/clusterguard/mysql.cnf",
		MySQLPort:         3306,
	}
}

func TestPowerControllerStopServiceRunsSystemctl(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string]string{}}
	controller := NewLinuxPowerController(runner)
	if err := controller.StopService(context.Background(), powerTestPolicy()); err != nil {
		t.Fatalf("stop service: %v", err)
	}
	joined := strings.Join(runner.calls, "\n")
	if joined != "/usr/bin/systemctl stop mysqld" {
		t.Fatalf("stop call=%q, want systemctl stop mysqld", joined)
	}
}

func TestPowerControllerStartServiceRunsSystemctl(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string]string{}}
	controller := NewLinuxPowerController(runner)
	if err := controller.StartService(context.Background(), powerTestPolicy()); err != nil {
		t.Fatalf("start service: %v", err)
	}
	joined := strings.Join(runner.calls, "\n")
	if joined != "/usr/bin/systemctl start mysqld" {
		t.Fatalf("start call=%q, want systemctl start mysqld", joined)
	}
}

func TestPowerControllerRejectsMissingServiceName(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string]string{}}
	controller := NewLinuxPowerController(runner)
	policy := powerTestPolicy()
	policy.MySQLService = ""
	for name, operation := range map[string]func() error{
		"stop":   func() error { return controller.StopService(context.Background(), policy) },
		"start":  func() error { return controller.StartService(context.Background(), policy) },
		"status": func() error { _, _, err := controller.ServiceStatus(context.Background(), policy); return err },
	} {
		if err := operation(); err == nil || !strings.Contains(err.Error(), "MySQL service name") {
			t.Errorf("%s without service name: got %v, want configured-name error", name, err)
		}
	}
}

func TestPowerControllerServiceStatusParsesIsActive(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string]string{
		"/usr/bin/systemctl is-active mysqld": "active\n",
		"/usr/bin/mysql --defaults-file=/etc/clusterguard/mysql.cnf --protocol=tcp --host=127.0.0.1 --port=3306 --batch --skip-column-names --execute SELECT 1": "1\n",
	}}
	controller := NewLinuxPowerController(runner)
	running, reachable, err := controller.ServiceStatus(context.Background(), powerTestPolicy())
	if err != nil {
		t.Fatalf("service status: %v", err)
	}
	if !running || !reachable {
		t.Fatalf("status running=%t reachable=%t, want both true", running, reachable)
	}
}

func TestPowerControllerServiceStatusReportsStoppedService(t *testing.T) {
	// A stopped service makes is-active exit non-zero; the fake runner maps
	// the call to an empty output but the fake never errors, so emulate a
	// stopped service with a runner that fails on is-active.
	runner := &failingCommandRunner{failure: "inactive"}
	controller := NewLinuxPowerController(runner)
	running, reachable, err := controller.ServiceStatus(context.Background(), powerTestPolicy())
	if err != nil {
		t.Fatalf("service status: %v", err)
	}
	if running || reachable {
		t.Fatalf("status running=%t reachable=%t, want both false", running, reachable)
	}
}

func TestPowerControllerPowerOffSchedulesDelayedSystemdUnit(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string]string{}}
	controller := NewLinuxPowerController(runner)
	if err := controller.PowerOff(context.Background(), powerTestPolicy()); err != nil {
		t.Fatalf("power off: %v", err)
	}
	joined := strings.Join(runner.calls, "\n")
	want := "/usr/bin/systemd-run --unit=clusterguard-node-poweroff --collect --on-active=10s /usr/bin/systemctl poweroff"
	if joined != want {
		t.Fatalf("poweroff call=%q, want %q", joined, want)
	}
}

func TestPowerControllerPrepareRecoverySnapshotWritesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster-topology.json")
	controller, err := NewLinuxPowerControllerWithSnapshotPath(&fakeCommandRunner{}, path)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	policy := powerTestPolicy()
	policy.ClusterID = model.NewResourceID()
	policy.InstanceID = model.NewResourceID()
	snapshot := model.PowerSnapshot{
		ClusterID: policy.ClusterID, ClusterName: "mysql-production", Engine: model.EngineMySQL,
		Primary: model.PowerInstanceRef{
			InstanceID: policy.InstanceID, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306,
		},
		CapturedAt: time.Now().UTC(),
	}
	if err := controller.PrepareRecoverySnapshot(context.Background(), policy, snapshot); err != nil {
		t.Fatalf("prepare recovery snapshot: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovery snapshot: %v", err)
	}
	var persisted model.PowerSnapshot
	if err := json.Unmarshal(contents, &persisted); err != nil {
		t.Fatalf("decode recovery snapshot: %v", err)
	}
	if persisted.ClusterID != snapshot.ClusterID || persisted.Primary.InstanceID != policy.InstanceID {
		t.Fatalf("persisted snapshot=%+v, want cluster and local instance identity", persisted)
	}
	if persisted.LocalInstanceID != policy.InstanceID || persisted.LocalServiceName != policy.MySQLService {
		t.Fatalf("persisted local restore metadata=%+v, want instance=%s service=%s", persisted, policy.InstanceID, policy.MySQLService)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat recovery snapshot: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("snapshot permissions=%#o, want 0600", permissions)
	}
	if matches, _ := filepath.Glob(path + ".tmp-*"); len(matches) != 0 {
		t.Fatalf("temporary snapshot files were not cleaned up: %v", matches)
	}
}

func TestPowerControllerStoresEachClusterSnapshotIndependently(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "power-snapshots")
	controller, err := NewLinuxPowerControllerWithSnapshotPath(&fakeCommandRunner{}, directory)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	for _, name := range []string{"mysql-production-a", "mysql-production-b"} {
		policy := powerTestPolicy()
		policy.ClusterID = model.NewResourceID()
		policy.InstanceID = model.NewResourceID()
		snapshot := model.PowerSnapshot{
			ClusterID: policy.ClusterID, ClusterName: name, Engine: model.EngineMySQL,
			Primary: model.PowerInstanceRef{
				InstanceID: policy.InstanceID, Hostname: name + "-primary", IPAddress: "192.0.2.20", Port: 3306,
			},
			CapturedAt: time.Now().UTC(),
		}
		if err := controller.PrepareRecoverySnapshot(context.Background(), policy, snapshot); err != nil {
			t.Fatalf("prepare %s: %v", name, err)
		}
		path := filepath.Join(directory, string(policy.ClusterID)+".json")
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s snapshot: %v", name, err)
		}
		if !strings.Contains(string(contents), name) {
			t.Fatalf("snapshot %s does not contain cluster name %q", path, name)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("snapshot entries=%d, want 2 independent cluster files", len(entries))
	}
}

func TestPowerControllerPrepareRecoverySnapshotRejectsIdentityMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster-topology.json")
	controller, err := NewLinuxPowerControllerWithSnapshotPath(&fakeCommandRunner{}, path)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	policy := powerTestPolicy()
	policy.ClusterID = model.NewResourceID()
	policy.InstanceID = model.NewResourceID()
	snapshot := model.PowerSnapshot{
		ClusterID: model.NewResourceID(), ClusterName: "wrong-cluster", Engine: model.EngineMySQL,
		Primary:    model.PowerInstanceRef{InstanceID: model.NewResourceID(), Hostname: "mysql-b", IPAddress: "192.0.2.11", Port: 3306},
		CapturedAt: time.Now().UTC(),
	}
	if err := controller.PrepareRecoverySnapshot(context.Background(), policy, snapshot); err == nil {
		t.Fatal("identity-mismatched snapshot must be rejected")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected snapshot must not be written, stat err=%v", err)
	}
}

func TestPowerControllerRejectsUnsafeSnapshotPath(t *testing.T) {
	if _, err := NewLinuxPowerControllerWithSnapshotPath(&fakeCommandRunner{}, "relative/cluster-topology.json"); err == nil {
		t.Fatal("relative snapshot path must be rejected")
	}
}

// failingCommandRunner fails every command, modelling a stopped service or an
// unreachable host for status probes.
type failingCommandRunner struct {
	failure string
}

func (runner *failingCommandRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return nil, &commandErrorStub{message: runner.failure}
}

type commandErrorStub struct {
	message string
}

func (err *commandErrorStub) Error() string { return err.message }
