package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestReconcileSandboxNarrowPathsWithoutCredentials(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := recoveryGuardPolicy(t)
	p.PostgreSQLDataDirectory = filepath.Join(base, "pgdata")
	if err := os.Mkdir(p.PostgreSQLDataDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(fileConfig{SharedSecretEnv: "UNSET_SANDBOX_TEST_SECRET", Clusters: []ClusterPolicy{p, p, {Engine: model.EngineMySQL}}})
	config, output := filepath.Join(base, "agent.json"), filepath.Join(base, "dropin")
	if err := os.WriteFile(config, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := configureReconcileSandbox(config, output); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(output, "20-postgresql-recovery.conf"))
	if err != nil {
		t.Fatal(err)
	}
	want := "ReadWritePaths=-" + p.PostgreSQLDataDirectory + " -" + p.PostgreSQLDataDirectory + ".clusterguard-recovery\n"
	if strings.Count(string(actual), "ReadWritePaths=") != 1 || !strings.Contains(string(actual), want) || !strings.Contains(string(actual), "CAP_DAC_OVERRIDE") || strings.Contains(string(actual), "ProtectSystem=") {
		t.Fatalf("sandbox was not narrow and additive: %s", actual)
	}
	info, err := os.Stat(p.PostgreSQLDataDirectory + ".clusterguard-recovery")
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("private receipt directory: %v %v", info, err)
	}
	for _, unsafe := range []string{"/data", "/var/lib", "/var/lib/pg\nReadWritePaths=/", "/var/lib/%H", "/var/lib/a/../pg", "/var/lib/pg data"} {
		p.PostgreSQLDataDirectory = unsafe
		b, _ := json.Marshal(fileConfig{Clusters: []ClusterPolicy{p}})
		if _, err := sandboxPostgreSQLPaths(b); err == nil {
			t.Fatalf("accepted unsafe path %q", unsafe)
		}
	}
	link := filepath.Join(base, "alias")
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	p.PostgreSQLDataDirectory = filepath.Join(link, "pgdata")
	b, _ = json.Marshal(fileConfig{Clusters: []ClusterPolicy{p}})
	if _, err := sandboxPostgreSQLPaths(b); err == nil {
		t.Fatal("accepted symlinked ancestor")
	}
}

func TestRecoveryGuardMigratesFrozenLegacyReceipt(t *testing.T) {
	p := recoveryGuardPolicy(t)
	hba, manifest, _ := pgGuardPaths(p)
	original := []byte("local all all peer\n")
	if err := os.WriteFile(hba, original, 0600); err != nil {
		t.Fatal(err)
	}
	task := model.NewResourceID()
	tool := func(context.Context, string, ...string) ([]byte, error) { return []byte(hba), nil }
	if err := preparePGGuard(context.Background(), p, task, tool); err != nil {
		t.Fatal(err)
	}
	legacy := p.PostgreSQLDataDirectory + ".clusterguard-recovery-guard.json"
	if err := os.Rename(manifest, legacy); err != nil {
		t.Fatal(err)
	}
	backup, _ := os.ReadFile(legacy)
	if _, err := verifyPGGuardFile(p, task); err != nil {
		t.Fatal(err)
	}
	if err := preparePGGuard(context.Background(), p, task, tool); err != nil {
		t.Fatal(err)
	}
	query := func(context.Context, ClusterPolicy, string) ([]byte, error) { return []byte("t\n"), nil }
	if err := releasePGGuard(context.Background(), p, task, query); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(hba)
	retained, _ := os.ReadFile(legacy)
	if string(actual) != string(original) || string(retained) != string(backup) {
		t.Fatal("lost original HBA or altered legacy receipt")
	}
}
