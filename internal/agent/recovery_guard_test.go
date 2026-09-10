package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func recoveryGuardPolicy(t *testing.T) ClusterPolicy {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := ClusterPolicy{ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID(), Engine: model.EnginePostgreSQL, PostgreSQLUser: "postgres", PostgreSQLReplicationUser: "cg_replication", PostgreSQLDataDirectory: dir}
	for _, ip := range []string{"192.168.102.152", "192.168.102.153"} {
		p.PostgreSQLPeers = append(p.PostgreSQLPeers, PostgreSQLPeer{InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(), IPAddress: ip, Port: 55432})
	}
	return p
}

func TestRecoveryGuardIncludesCoLocatedControllerFromLoadedConfig(t *testing.T) {
	p := recoveryGuardPolicy(t)
	p.PostgreSQLNodeID = model.NewResourceID()
	p.PostgreSQLHostname = "pg03"
	p.PostgreSQLPort = 55432
	p.PostgreSQLService = "postgresql-16"
	p.PostgreSQLBinaryDirectory = "/usr/lib/postgresql/16/bin"
	p.PostgreSQLPassfile = "/etc/clusterguard/pgpass"
	p.PostgreSQLDatabase = "postgres"
	t.Setenv("CG_GUARD_TEST_SECRET", "fixture-secret")
	config := fileConfig{SharedSecretEnv: "CG_GUARD_TEST_SECRET", ControllerURLs: []string{"https://192.168.102.152:3000", "https://192.168.102.153:3000", "https://192.168.102.154:3000", "https://controller.example:3000"}, Clusters: []ClusterPolicy{p}}
	b, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	hba, err := pgGuardHBA(loaded.Clusters[p.ClusterID])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hba), "host all postgres 192.168.102.154/32 scram-sha-256\n") {
		t.Fatal("co-located Raft Leader cannot probe guarded primary through its registered IP")
	}
	for _, forbidden := range []string{"host all all 192.168.102.154", "host replication cg_replication 192.168.102.154", "/24", " trust", "controller.example"} {
		if strings.Contains(string(hba), forbidden) {
			t.Fatalf("guard widened access: %s", forbidden)
		}
	}
	if !strings.Contains(string(hba), "host all all 0.0.0.0/0 reject") {
		t.Fatal("business fence missing")
	}
}

func TestRecoveryGuardControllerUpgradePreservesOriginalAndRejectsTampering(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "tampered"}[tamper], func(t *testing.T) {
			p := recoveryGuardPolicy(t)
			hba, _, _ := pgGuardPaths(p)
			original := []byte("local all all peer\nhost all all 0.0.0.0/0 scram-sha-256\n")
			if err := os.WriteFile(hba, original, 0600); err != nil {
				t.Fatal(err)
			}
			task := model.NewResourceID()
			tool := func(context.Context, string, ...string) ([]byte, error) { return []byte(hba), nil }
			if err := preparePGGuard(context.Background(), p, task, tool); err != nil {
				t.Fatal(err)
			}
			if tamper {
				if err := os.WriteFile(hba, []byte("host all all 0.0.0.0/0 trust\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			p.RecoveryControllerAddresses = []string{"192.168.102.154"}
			err := preparePGGuard(context.Background(), p, task, tool)
			if tamper {
				if err == nil {
					t.Fatal("unrecognized guard was migrated")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			state, err := verifyPGGuardFile(p, task)
			if err != nil || state.TaskID != task || string(state.Original) != string(original) || state.Released {
				t.Fatalf("legacy migration lost recovery protection: %v", err)
			}
			if err := preparePGGuard(context.Background(), p, task, tool); err != nil {
				t.Fatalf("migration is not idempotent: %v", err)
			}
		})
	}
}

func TestRecoveryGuardControllerAddressDoesNotAcceptNetworksOrDNS(t *testing.T) {
	for _, address := range []string{"192.168.102.0/24", "controller.example", "0.0.0.0", "::", "224.0.0.1"} {
		p := recoveryGuardPolicy(t)
		p.RecoveryControllerAddresses = []string{address}
		if _, err := pgGuardHBA(p); err == nil {
			t.Fatalf("unqualified controller address accepted: %s", address)
		}
	}
}

func TestRecoveryGuardFileIdentityAndRestoration(t *testing.T) {
	p := recoveryGuardPolicy(t)
	ctx := context.Background()
	hba, manifest, _ := pgGuardPaths(p)
	original := []byte("local all all peer\nhost all all 0.0.0.0/0 scram-sha-256\n")
	if err := os.WriteFile(hba, original, 0600); err != nil {
		t.Fatal(err)
	}
	task := model.NewResourceID()
	tool := func(_ context.Context, name string, args ...string) ([]byte, error) { return []byte(hba + "\n"), nil }
	if err := preparePGGuard(ctx, p, task, tool); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyPGGuardFile(p, task); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyPGGuardFile(p, model.NewResourceID()); err == nil {
		t.Fatal("accepted another task")
	}
	if err := preparePGGuard(ctx, p, model.NewResourceID(), tool); err == nil {
		t.Fatal("replaced active guard of another task")
	}
	info, _ := os.Stat(manifest)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("guard receipt is not private: %v", info.Mode())
	}
	contents, _ := os.ReadFile(hba)
	if strings.Contains(string(contents), "trust") || !strings.Contains(string(contents), "host all all 0.0.0.0/0 reject") {
		t.Fatal("guard is not restrictive")
	}
	if err := os.WriteFile(hba, []byte("host all all all trust\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyPGGuardFile(p, task); err == nil {
		t.Fatal("accepted changed HBA")
	}
	query := func(context.Context, ClusterPolicy, string) ([]byte, error) { return []byte("t\n"), nil }
	if err := releasePGGuard(ctx, p, task, query); err == nil {
		t.Fatal("overwrote independently changed HBA")
	}
	if err := preparePGGuard(ctx, p, task, tool); err != nil {
		t.Fatal(err)
	}
	if err := releasePGGuard(ctx, p, task, query); err != nil {
		t.Fatal(err)
	}
	if err := releasePGGuard(ctx, p, task, query); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(hba)
	if string(actual) != string(original) {
		t.Fatal("original HBA was not exactly restored")
	}
	if _, err := verifyPGGuardFile(p, task); err == nil {
		t.Fatal("released guard still authorizes recovery")
	}
	if err := preparePGGuard(ctx, p, model.NewResourceID(), tool); err != nil {
		t.Fatalf("new recovery after completed restoration: %v", err)
	}
}

func TestRecoveryGuardRejectsUnqualifiedHBAAndPolicy(t *testing.T) {
	p := recoveryGuardPolicy(t)
	hba, _, _ := pgGuardPaths(p)
	if err := os.Symlink("/etc/hosts", hba); err != nil {
		t.Fatal(err)
	}
	tool := func(_ context.Context, name string, args ...string) ([]byte, error) { return []byte(hba), nil }
	if err := preparePGGuard(context.Background(), p, model.NewResourceID(), tool); err == nil {
		t.Fatal("accepted symlink HBA")
	}
	p.PostgreSQLPeers[0].IPAddress = "untrusted.example"
	if _, err := pgGuardHBA(p); err == nil {
		t.Fatal("accepted unpinned recovery host")
	}
}

func TestRecoveryGuardRequiresPostInstallProcess(t *testing.T) {
	p := recoveryGuardPolicy(t)
	hba, manifest, _ := pgGuardPaths(p)
	if err := os.WriteFile(hba, []byte("local all all peer\n"), 0600); err != nil {
		t.Fatal(err)
	}
	task := model.NewResourceID()
	tool := func(_ context.Context, name string, args ...string) ([]byte, error) { return []byte(hba), nil }
	if err := preparePGGuard(context.Background(), p, task, tool); err != nil {
		t.Fatal(err)
	}
	state, _, _, _ := readPGGuard(p, task)
	state.InstalledAt = time.Now().Add(-time.Minute)
	b, _ := json.Marshal(state)
	if err := os.WriteFile(manifest, b, 0600); err != nil {
		t.Fatal(err)
	}
	query := func(_ context.Context, _ ClusterPolicy, q string) ([]byte, error) {
		if !strings.Contains(q, "pg_postmaster_start_time()") || !strings.Contains(q, "current_setting('hba_file')") {
			t.Fatalf("missing live guard proof: %s", q)
		}
		return []byte("f\n"), nil
	}
	if err := verifyPGGuardLoaded(context.Background(), p, task, query); err == nil {
		t.Fatal("accepted pre-guard PostgreSQL process")
	}
}

func TestRecoveryGuardPostgreSQLActualBusinessAccess(t *testing.T) {
	f := newRecoveryPGFixture(t)
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if !postgresSQLNamePattern.MatchString(account.Username) {
		t.Skip("fixture requires a simple OS username for peer authentication")
	}
	f.query("pg02", `CREATE ROLE "`+account.Username+`" LOGIN SUPERUSER; CREATE ROLE recovery_business LOGIN;`)
	f.stop("pg01", "fast")
	f.stop("pg03", "fast")
	f.stop("pg02", "fast")
	p := f.policies["pg02"]
	p.PostgreSQLUser = account.Username
	p.PostgreSQLReplicationUser = "cg_fixture"
	p.PostgreSQLPeers = []PostgreSQLPeer{{IPAddress: "127.0.0.1"}, {IPAddress: "::1"}}
	task := model.NewResourceID()
	if err = preparePGGuard(context.Background(), p, task, f.tool("pg02")); err != nil {
		t.Fatal(err)
	}
	f.start("pg02")
	query := func(ctx context.Context, _ ClusterPolicy, sql string) ([]byte, error) {
		return exec.CommandContext(ctx, filepath.Join(f.bin, "psql"), "-h", f.socket["pg02"], "-U", account.Username, "-d", "postgres", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c", sql).CombinedOutput()
	}
	if err = verifyPGGuardLoaded(context.Background(), p, task, query); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, filepath.Join(f.bin, "psql"), "-h", f.socket["pg02"], "-U", "recovery_business", "-d", "postgres", "-w", "-c", "SELECT 1").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "reject") {
		t.Fatalf("business connection was not rejected: %v %s", err, out)
	}
	if err = releasePGGuard(context.Background(), p, task, query); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err = exec.CommandContext(ctx, filepath.Join(f.bin, "psql"), "-h", f.socket["pg02"], "-U", "recovery_business", "-d", "postgres", "-w", "-c", "SELECT 1").CombinedOutput()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("original business access was not restored")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
