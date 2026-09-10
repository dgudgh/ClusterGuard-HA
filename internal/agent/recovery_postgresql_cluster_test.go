package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/disaster"
	"clusterguard.io/ha/pkg/model"
)

type recoveryPGFixture struct {
	t                  *testing.T
	bin, root, sockets string
	dirs, socket       map[string]string
	running            map[string]bool
	policies           map[string]ClusterPolicy
	cluster            model.ResourceID
}

func newRecoveryPGFixture(t *testing.T) *recoveryPGFixture {
	t.Helper()
	bin := os.Getenv("CG_PG16_BIN")
	if bin == "" {
		t.Skip("CG_PG16_BIN required for real three-member recovery evidence")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("CG_PG16_BIN must be absolute")
	}
	sockets, err := os.MkdirTemp("/tmp", "cg-dr-")
	if err != nil {
		t.Fatal(err)
	}
	f := &recoveryPGFixture{t: t, bin: bin, root: t.TempDir(), sockets: sockets, dirs: map[string]string{}, socket: map[string]string{}, running: map[string]bool{}, policies: map[string]ClusterPolicy{}, cluster: model.NewResourceID()}
	t.Cleanup(func() {
		for name, running := range f.running {
			if !running {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			out, err := exec.CommandContext(ctx, filepath.Join(f.bin, "pg_ctl"), "-D", f.dirs[name], "-m", "immediate", "-w", "-t", "10", "stop").CombinedOutput()
			cancel()
			if err != nil {
				t.Errorf("fixture cleanup %s: %v %s", name, err, out)
			}
		}
		if err := os.RemoveAll(sockets); err != nil {
			t.Error(err)
		}
	})
	for _, name := range []string{"pg02", "pg01", "pg03"} {
		f.dirs[name] = filepath.Join(f.root, name)
		f.socket[name] = filepath.Join(sockets, name)
		if err := os.Mkdir(f.socket[name], 0700); err != nil {
			t.Fatal(err)
		}
		f.policies[name] = ClusterPolicy{ClusterID: f.cluster, InstanceID: model.NewResourceID(), Engine: model.EnginePostgreSQL, PostgreSQLNodeID: model.NewResourceID(), PostgreSQLDataDirectory: f.dirs[name]}
	}
	f.run("initdb", "-D", f.dirs["pg02"], "-U", "cg_fixture", "-A", "trust", "--locale=C")
	f.configure("pg02", "")
	f.start("pg02")
	f.query("pg02", "CREATE TABLE recovery_fixture(id integer primary key, branch text); INSERT INTO recovery_fixture VALUES(1, 'common');")
	for _, name := range []string{"pg01", "pg03"} {
		conn := fmt.Sprintf("host=%s user=cg_fixture application_name=%s", f.socket["pg02"], f.policies[name].PostgreSQLNodeID)
		f.run("pg_basebackup", "-d", conn, "-D", f.dirs[name], "-R", "-X", "stream", "--checkpoint=fast")
		f.configure(name, "")
		f.start(name)
	}
	f.query("pg02", "INSERT INTO recovery_fixture VALUES(2, 'shared-after-backup');")
	f.waitRows("pg01", 2)
	f.waitRows("pg03", 2)
	return f
}

func (f *recoveryPGFixture) run(name string, args ...string) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(f.bin, name), args...)
	cmd.Env = []string{"PATH=" + f.bin + ":/usr/bin:/bin", "LC_ALL=C", "PGCONNECT_TIMEOUT=5"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("fixture %s: %v %s", name, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *recoveryPGFixture) configure(name, extra string) {
	f.t.Helper()
	file, err := os.OpenFile(filepath.Join(f.dirs[name], "postgresql.auto.conf"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		f.t.Fatal(err)
	}
	_, err = fmt.Fprintf(file, "\nlisten_addresses=''\nunix_socket_directories='%s'\nwal_keep_size='128MB'\nmax_prepared_transactions=10\nclusterguard.node_id='%s'\n%s\n", f.socket[name], f.policies[name].PostgreSQLNodeID, extra)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		f.t.Fatalf("fixture config: %v %v", err, closeErr)
	}
}

func (f *recoveryPGFixture) start(name string) {
	f.run("pg_ctl", "-D", f.dirs[name], "-l", filepath.Join(f.root, name+".log"), "-w", "-t", "20", "start")
	f.running[name] = true
}

func (f *recoveryPGFixture) stop(name, mode string) {
	f.run("pg_ctl", "-D", f.dirs[name], "-m", mode, "-w", "-t", "20", "stop")
	f.running[name] = false
}

func (f *recoveryPGFixture) query(name, sql string) string {
	return f.run("psql", "-h", f.socket[name], "-U", "cg_fixture", "-d", "postgres", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c", sql)
}

func (f *recoveryPGFixture) waitRows(name string, rows int) {
	f.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for f.query(name, "SELECT count(*) FROM recovery_fixture") != fmt.Sprint(rows) {
		if time.Now().After(deadline) {
			f.t.Fatalf("fixture %s did not catch up", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *recoveryPGFixture) tool(name string) recoveryPGTool {
	return func(ctx context.Context, command string, args ...string) ([]byte, error) {
		if f.running[name] {
			return nil, fmt.Errorf("fixture must be offline")
		}
		switch command {
		case "postgres":
			args = append([]string{"-D", f.dirs[name]}, args...)
		case "pg_controldata":
			args = append([]string{f.dirs[name]}, args...)
		default:
			args = append([]string{"--path", filepath.Join(f.dirs[name], "pg_wal")}, args...)
		}
		return (OSCommandRunner{}).RunOrdered(ctx, filepath.Join(f.bin, command), args...)
	}
}

func (f *recoveryPGFixture) evidence() ([]model.DatabaseInstance, []model.RecoveryEvidence) {
	f.t.Helper()
	var members []model.DatabaseInstance
	var evidence []model.RecoveryEvidence
	for _, name := range []string{"pg01", "pg02", "pg03"} {
		p := f.policies[name]
		e, err := inspectStoppedPostgreSQL(context.Background(), p, f.tool(name))
		if err != nil {
			control, _ := f.tool(name)(context.Background(), "pg_controldata")
			dump, dumpErr := f.tool(name)(context.Background(), "pg_waldump", "--timeline", fmt.Sprint(e.Timeline), "--start", e.Redo, "--limit", "100000")
			if len(dump) > 2000 {
				dump = dump[len(dump)-2000:]
			}
			f.t.Logf("fixture control: %s\nWAL tail: %v %s", control, dumpErr, dump)
			f.t.Fatalf("inspect %s: %v", name, err)
		}
		members = append(members, model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: p.InstanceID}, ClusterID: f.cluster, Engine: model.EnginePostgreSQL, EngineIdentity: map[string]string{"resource_id": string(p.PostgreSQLNodeID), "system_identifier": e.SystemIdentifier}})
		evidence = append(evidence, e)
	}
	return members, evidence
}

func (f *recoveryPGFixture) VerifyWAL(ctx context.Context, e model.RecoveryEvidence, r model.RecoveryWALRequest) (model.RecoveryWALResult, error) {
	for name, p := range f.policies {
		if p.InstanceID == e.InstanceID {
			return verifyRecoveryWAL(ctx, p, f.tool(name), r)
		}
	}
	return model.RecoveryWALResult{}, fmt.Errorf("unregistered fixture member")
}

func TestRecoveryPostgreSQLActualThreeNodeWALSelection(t *testing.T) {
	f := newRecoveryPGFixture(t)
	f.stop("pg01", "fast")
	f.stop("pg03", "fast")
	f.query("pg02", "INSERT INTO recovery_fixture VALUES(3, 'latest-authoritative-commit');")
	f.stop("pg02", "fast")
	members, evidence := f.evidence()
	proofs, err := disaster.CompareWAL(context.Background(), evidence, f)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := disaster.Select(members, evidence, proofs)
	if err != nil || selected != f.policies["pg02"].InstanceID {
		t.Fatalf("newest authoritative primary not selected: %s %v", selected, err)
	}
	if len(proofs) != 6 {
		t.Fatalf("expected six directed proofs, got %d", len(proofs))
	}
	t.Log("real pg02 committed history selected; six directed WAL byte proofs verified")
}

func TestRecoveryPostgreSQLActualDivergentCommits(t *testing.T) {
	f := newRecoveryPGFixture(t)
	f.stop("pg01", "fast")
	f.stop("pg03", "fast")
	f.configure("pg01", "primary_conninfo=''\nprimary_slot_name=''")
	f.start("pg01")
	f.run("pg_ctl", "-D", f.dirs["pg01"], "-w", "-t", "20", "promote")
	f.query("pg01", "INSERT INTO recovery_fixture VALUES(10, 'independent-new-branch');")
	f.query("pg02", "INSERT INTO recovery_fixture VALUES(20, 'independent-old-branch');")
	f.stop("pg01", "immediate")
	f.stop("pg02", "immediate")
	members, evidence := f.evidence()
	proofs, err := disaster.CompareWAL(context.Background(), evidence, f)
	if err != nil {
		t.Fatalf("cannot complete real branch proof: %v", err)
	}
	independent := 0
	for _, proof := range proofs {
		if proof.DiscardedWALVerified && proof.DiscardedTransactions {
			independent++
		}
	}
	if independent < 2 {
		t.Fatalf("both committed branches were not detected: %+v", proofs)
	}
	if primary, err := disaster.Select(members, evidence, proofs); err == nil || primary != "" {
		t.Fatalf("divergent business commits selected a winner: %s %v", primary, err)
	}
	t.Log("independent business COMMIT records verified on both branches; automatic selection blocked")
}

func TestRecoveryPostgreSQLActualPromotedBranchWithoutOldCommits(t *testing.T) {
	f := newRecoveryPGFixture(t)
	f.stop("pg01", "fast")
	f.stop("pg03", "fast")
	f.configure("pg01", "primary_conninfo=''\nprimary_slot_name=''")
	f.start("pg01")
	f.run("pg_ctl", "-D", f.dirs["pg01"], "-w", "-t", "20", "promote")
	f.query("pg01", "INSERT INTO recovery_fixture VALUES(10, 'only-new-branch-committed');")
	f.stop("pg01", "immediate")
	f.stop("pg02", "immediate")
	members, evidence := f.evidence()
	proofs, err := disaster.CompareWAL(context.Background(), evidence, f)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := disaster.Select(members, evidence, proofs)
	if err != nil || selected != f.policies["pg01"].InstanceID {
		t.Fatalf("safe promoted branch not selected: %s %v", selected, err)
	}
	t.Log("promoted branch selected from verified ancestor WAL and unique committed history")
}

func TestRecoveryPostgreSQLActualMissingWALBlocksSelection(t *testing.T) {
	f := newRecoveryPGFixture(t)
	f.stop("pg01", "fast")
	f.stop("pg03", "fast")
	f.stop("pg02", "fast")
	_, evidence := f.evidence()
	var selected model.RecoveryEvidence
	for _, e := range evidence {
		if e.InstanceID == f.policies["pg02"].InstanceID {
			selected = e
		}
	}
	position, err := disaster.LSN(selected.Redo)
	if err != nil {
		t.Fatal(err)
	}
	segment := position / selected.WALSegmentBytes
	perLog := (uint64(1) << 32) / selected.WALSegmentBytes
	path := filepath.Join(f.dirs["pg02"], "pg_wal", fmt.Sprintf("%08X%08X%08X", selected.Timeline, segment/perLog, segment%perLog))
	if err := os.Rename(path, path+".withheld-for-test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Rename(path+".withheld-for-test", path); err != nil {
			t.Error(err)
		}
	})
	if _, err := disaster.CompareWAL(context.Background(), evidence, f); err == nil {
		t.Fatal("missing physical WAL accepted as an empty branch")
	}
	t.Log("missing physical WAL blocked comparison without discarding data")
}

func TestRecoveryPostgreSQLActualPreparedTransactionBlocksSelection(t *testing.T) {
	f := newRecoveryPGFixture(t)
	f.stop("pg01", "fast")
	f.stop("pg03", "fast")
	f.query("pg02", "BEGIN; INSERT INTO recovery_fixture VALUES(30, 'prepared-not-decided'); PREPARE TRANSACTION 'fixture-prepared';")
	f.stop("pg02", "immediate")
	members, evidence := f.evidence()
	prepared := false
	for _, e := range evidence {
		prepared = prepared || e.PreparedTransactions
	}
	if !prepared {
		t.Fatal("prepared WAL transaction not detected")
	}
	if err := disaster.ValidateEvidence(members, evidence); err == nil {
		t.Fatal("unresolved prepared transaction accepted for automatic recovery")
	}
	t.Log("unresolved PREPARE in physical WAL blocked automatic recovery")
}
