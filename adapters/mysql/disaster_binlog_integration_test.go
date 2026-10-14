package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/gtid"
	"clusterguard.io/ha/pkg/model"
)

type nativeRecoveryFixture struct {
	Scope             string `json:"scope"`
	Nonce             string `json:"nonce"`
	Root              string `json:"root"`
	MySQL             string `json:"mysql_binary"`
	MySQLD            string `json:"mysqld_binary"`
	Administrative    string `json:"administrative_username"`
	AdminPasswordFile string `json:"administrative_password_file"`
	Replication       string `json:"replication_username"`
	ReplPasswordFile  string `json:"replication_password_file"`
	Members           []struct {
		Port      int    `json:"port"`
		NativeID  string `json:"native_id"`
		Directory string `json:"directory"`
		Data      string `json:"data_directory"`
		Options   string `json:"option_file"`
		PID       string `json:"pid_file"`
	} `json:"members"`
}

func TestDisasterNativePhysicalBinlogLoss(t *testing.T) {
	testDisasterNativeBinlogRecovery(t, true)
}

func TestDisasterNativeReadableBinlogIncremental(t *testing.T) {
	testDisasterNativeBinlogRecovery(t, false)
}

func testDisasterNativeBinlogRecovery(t *testing.T, physicalLoss bool) {
	path := os.Getenv("CG_MYSQL_RECOVERY_NATIVE_FIXTURE")
	if path == "" {
		t.Skip("CG_MYSQL_RECOVERY_NATIVE_FIXTURE required for fresh isolated native MySQL recovery")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture nativeRecoveryFixture
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	owner, err := os.ReadFile(filepath.Join(fixture.Root, "fixture-owner"))
	if err != nil || string(owner) != fixture.Nonce || len(fixture.Nonce) != 32 ||
		fixture.Scope != "fresh-loopback-mysql-binlog-recovery" || len(fixture.Members) != 3 ||
		filepath.Dir(fixture.Root) != "/var/lib/clusterguard/diagnostics" ||
		filepath.Base(fixture.Root) != "dr-binlog-"+fixture.Nonce[:12] {
		t.Fatal("native test requires a newly created tool-owned private fixture")
	}
	for i, member := range fixture.Members {
		if member.Port != 43306+i || member.Directory != filepath.Join(fixture.Root, fmt.Sprint(i)) ||
			member.Data != filepath.Join(member.Directory, "data") || member.Options != filepath.Join(member.Directory, "my.cnf") ||
			member.PID != filepath.Join(member.Directory, "mysqld.pid") {
			t.Fatal("fixture member is outside the reserved scratch paths and ports")
		}
	}
	admin, err := os.ReadFile(fixture.AdminPasswordFile)
	if err != nil || fixture.AdminPasswordFile != filepath.Join(fixture.Root, "admin-password") {
		t.Fatal("fixture admin proof missing")
	}
	repl, err := os.ReadFile(fixture.ReplPasswordFile)
	if err != nil || fixture.ReplPasswordFile != filepath.Join(fixture.Root, "replication-password") {
		t.Fatal("fixture replication proof missing")
	}
	credentials := adapter.OperationCredentials{
		Administrative: adapter.Credentials{Username: fixture.Administrative, Password: string(admin)},
		Replication:    adapter.Credentials{Username: fixture.Replication, Password: string(repl)},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	runner := CLIQueryRunner{Binary: fixture.MySQL}
	e := DisasterExecutor{Runner: runner}
	members := make([]model.DatabaseInstance, 3)
	for i, native := range fixture.Members {
		members[i] = model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
			IPAddress: "127.0.0.1", Port: native.Port, Engine: model.EngineMySQL,
			EngineIdentity: model.EngineIdentity{"server_uuid": native.NativeID}}
		p, err := probeIdentity(ctx, runner, instanceEndpoint(members[i]), credentials.Administrative)
		if err != nil || p.serverUUID != native.NativeID || !strings.HasPrefix(p.version, "8.0.") {
			t.Fatalf("scratch native identity differs on member %d: %v", i, err)
		}
	}
	query := func(i int, sql string) []Row {
		t.Helper()
		rows, err := runner.Query(ctx, instanceEndpoint(members[i]), credentials.Administrative, sql)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	execute := func(i int, sql string) {
		t.Helper()
		if err := runner.Exec(ctx, instanceEndpoint(members[i]), credentials.Administrative, sql); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(i int, sql, key, expected string) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			rows, err := runner.Query(ctx, instanceEndpoint(members[i]), credentials.Administrative, sql)
			if err == nil && len(rows) == 1 && rows[0][key] == expected {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("scratch member %d failed to reach %s", i, key)
	}
	execute(0, "CREATE DATABASE cg_dr_fixture; CREATE TABLE cg_dr_fixture.transactions(id INT PRIMARY KEY); INSERT INTO cg_dr_fixture.transactions VALUES(1)")
	for _, i := range []int{1, 2} {
		statement, err := buildChangeSourceStatement("8.0.44", members[0], credentials.Replication)
		if err != nil {
			t.Fatal(err)
		}
		execute(i, statement+"; START REPLICA")
		wait(i, "SELECT COUNT(*) AS rows_present FROM cg_dr_fixture.transactions", "rows_present", "1")
		execute(i, "STOP REPLICA IO_THREAD")
	}
	for i := 2; i <= 15; i++ {
		execute(0, fmt.Sprintf("INSERT INTO cg_dr_fixture.transactions VALUES(%d)", i))
	}
	before := query(0, identityQuery)[0]
	status := query(0, "SHOW MASTER STATUS")
	if len(status) != 1 {
		t.Fatal("scratch binlog status missing")
	}
	logName := status[0]["File"]
	if filepath.Base(logName) != logName || !strings.HasPrefix(logName, "binlog.") {
		t.Fatal("unexpected scratch log name")
	}
	logPath := filepath.Join(fixture.Members[0].Data, logName)
	if physicalLoss {
		if err := os.Link(logPath, filepath.Join(fixture.Root, "original-binlog-inode")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(logPath); err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Query(ctx, instanceEndpoint(members[0]), credentials.Administrative, "SHOW BINLOG EVENTS IN "+mysqlStringLiteral(logName)+" LIMIT 1"); err == nil {
			t.Fatal("physical deletion did not make the original binlog unreadable")
		} else {
			var native *QueryError
			if !errors.As(err, &native) {
				t.Fatal(err)
			}
			t.Logf("physically missing scratch binlog produces native query error code %d", native.Code)
		}
	}
	for _, i := range []int{1, 2} {
		if physicalLoss {
			execute(i, "START REPLICA IO_THREAD")
			wait(i, "SELECT LAST_ERROR_NUMBER AS io_error FROM performance_schema.replication_connection_status", "io_error", "13114")
		}
		execute(i, "STOP REPLICA")
	}
	for i, native := range fixture.Members {
		execute(i, "SET PERSIST super_read_only=ON; SET PERSIST read_only=ON; SET PERSIST offline_mode=ON")
		guard, err := os.OpenFile(native.Options, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = guard.WriteString("\n# scratch recovery restart guard\nread_only=ON\nsuper_read_only=ON\noffline_mode=" + boolString(i != 0) + "\n")
		if closeErr := guard.Close(); err != nil || closeErr != nil {
			t.Fatal(errors.Join(err, closeErr))
		}
	}
	authorize := func() error { return ctx.Err() }
	guardFor := func(i int) func() error {
		return func() error {
			rows, err := runner.Query(ctx, instanceEndpoint(members[i]), credentials.Administrative, "SELECT @@GLOBAL.offline_mode AS guarded")
			if err != nil {
				return err
			}
			if len(rows) != 1 || rows[0]["guarded"] != "1" {
				return fmt.Errorf("scratch offline guard missing")
			}
			return nil
		}
	}
	for i := range members {
		if err := e.Prepare(ctx, members[i], credentials.Administrative, authorize, guardFor(i)); err != nil {
			t.Fatal(err)
		}
	}
	execute(0, "SET PERSIST offline_mode=OFF")
	if got := query(0, identityQuery)[0]; got["gtid_purged"] != before["gtid_purged"] || got["gtid_executed"] != before["gtid_executed"] {
		t.Fatal("physical deletion fixture unexpectedly changed the frozen GTID evidence")
	}
	if err := e.StartPrimary(ctx, members[0], credentials.Administrative, before["gtid_executed"], authorize); err != nil {
		t.Fatal(err)
	}
	current := query(0, "SHOW MASTER STATUS")[0]["File"]
	if current == logName {
		t.Fatal("recovery did not establish a fresh readable source binlog boundary")
	}
	query(0, "SHOW BINLOG EVENTS IN "+mysqlStringLiteral(current)+" LIMIT 1")
	for _, i := range []int{1, 2} {
		i := i
		restart := func() error {
			member := fixture.Members[i]
			if data, err := os.ReadFile(member.PID); err == nil {
				if arguments, err := os.ReadFile(filepath.Join("/proc", strings.TrimSpace(string(data)), "cmdline")); err == nil &&
					strings.Contains(string(arguments), "--defaults-file="+member.Options) {
					return nil
				}
			}
			command := exec.CommandContext(ctx, fixture.MySQLD, "--defaults-file="+member.Options, "--daemonize")
			if err := command.Run(); err != nil {
				return fmt.Errorf("restart scratch fenced recipient: %w", err)
			}
			return nil
		}
		if err := e.Rebuild(ctx, members[i], members[0], credentials, authorize, guardFor(i), restart); err != nil {
			t.Fatal(err)
		}
		identity := query(i, identityQuery)[0]
		if identity["server_uuid"] != fixture.Members[i].NativeID || identity["gtid_executed"] != before["gtid_executed"] ||
			identity["read_only"] != "1" || identity["super_read_only"] != "1" {
			t.Fatal("clone changed native identity, frozen GTID history or write fences")
		}
		clone := query(i, "SELECT STATE, ERROR_NO FROM performance_schema.clone_status")
		if physicalLoss && (len(clone) != 1 || clone[0]["STATE"] != "Completed" || clone[0]["ERROR_NO"] != "0") {
			t.Fatal("missing-log recipient was not actually cloned")
		}
		if !physicalLoss && len(clone) != 0 {
			t.Fatal("readable source history unexpectedly triggered a clone")
		}
		if query(i, "SELECT COUNT(*) AS rows_present FROM cg_dr_fixture.transactions")[0]["rows_present"] != "15" {
			t.Fatal("reconstruction lost missing business rows")
		}
		execute(i, "SET PERSIST offline_mode=OFF")
	}
	execute(0, "SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF; INSERT INTO cg_dr_fixture.transactions VALUES(16)")
	for _, i := range []int{1, 2} {
		wait(i, "SELECT COUNT(*) AS rows_present FROM cg_dr_fixture.transactions", "rows_present", "16")
		actual, err := gtid.ParseGTIDSet(query(i, identityQuery)[0]["gtid_executed"])
		if err != nil {
			t.Fatal(err)
		}
		want, err := gtid.ParseGTIDSet(query(0, identityQuery)[0]["gtid_executed"])
		if err != nil {
			t.Fatal(err)
		}
		comparison, err := gtid.CompareGTIDSets(want, actual)
		if err != nil || comparison.MissingTransactions != 0 || comparison.ErrantTransactions != 0 {
			t.Fatal("post-reconstruction replication did not deliver the exact new transaction")
		}
	}
	if physicalLoss {
		t.Log("physical source binlog loss recovered by guarded clones; recipient UUIDs, all 14 missing rows, exact GTIDs and subsequent replication verified")
	} else {
		t.Log("readable original binlogs used incremental recovery without clone; exact GTIDs, all rows and subsequent replication verified")
	}
}
