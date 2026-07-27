package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

type oracleInputRunnerStub struct {
	input     string
	inputs    []string
	name      string
	arguments []string
	output    string
	outputs   []string
	err       error
}

func (runner *oracleInputRunnerStub) RunInput(_ context.Context, input []byte, name string, arguments ...string) ([]byte, error) {
	runner.input = string(input)
	runner.inputs = append(runner.inputs, runner.input)
	runner.name = name
	runner.arguments = append([]string{}, arguments...)
	if len(runner.outputs) > 0 {
		output := runner.outputs[0]
		runner.outputs = runner.outputs[1:]
		return []byte(output), runner.err
	}
	return []byte(runner.output), runner.err
}

func oracleControllerPolicy() ClusterPolicy {
	return ClusterPolicy{
		ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID(), Engine: model.EngineOracle,
		OracleHome: "/u01/app/oracle/product/19.3.0/db", OracleSID: "mesdb",
		OracleOSUser: "oracle", OracleDGMGRLBinary: "/u01/app/oracle/product/19.3.0/db/bin/dgmgrl",
		OracleUsername: "clusterguard_dg", OracleAuthenticationRole: "sysdg",
		OraclePassword:          "never-print-this",
		OracleConnectIdentifier: "mesdb", OracleDatabaseUniqueName: "mesdb",
		OracleBrokerConfiguration: "MESDB_DG", OracleMembers: []string{"mesdb", "reportdb"},
	}
}

func testOracleController(t *testing.T, runner InputCommandRunner) *OracleLocalController {
	t.Helper()
	controller, err := newOracleController(
		runner, "/usr/bin/setpriv",
		func(name string) (string, string, []string, error) {
			if name != "oracle" {
				t.Fatalf("unexpected Oracle OS user %q", name)
			}
			return "54321", "54321", []string{"54321", "54322"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func TestOracleControllerStatusUsesFixedBinaryAndParsesBrokerEvidence(t *testing.T) {
	runner := &oracleInputRunnerStub{outputs: []string{`
Configuration - MESDB_DG
Configuration Status:
SUCCESS
Database Role: PRIMARY
Database Status: SUCCESS
`, `
Database Role: PHYSICAL STANDBY
Ready for Switchover: Yes
Transport Lag: 0 seconds
Apply Lag: 0 seconds
`}}
	controller := testOracleController(t, runner)
	status, err := controller.Status(context.Background(), oracleControllerPolicy(), "reportdb")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Configuration != "MESDB_DG" || status.Database != "mesdb" ||
		status.Role != "PRIMARY" || status.ConfigurationStatus != "SUCCESS" ||
		!status.ReadyForSwitchover || status.TransportLagSeconds == nil || *status.TransportLagSeconds != 0 ||
		status.ApplyLagSeconds == nil || *status.ApplyLagSeconds != 0 {
		t.Fatalf("status=%+v", status)
	}
	command := runner.name + " " + strings.Join(runner.arguments, " ")
	for _, expected := range []string{
		"/usr/bin/setpriv --reuid 54321 --regid 54321 --groups 54321,54322 -- /usr/bin/env",
		"ORACLE_HOME=/u01/app/oracle/product/19.3.0/db", "ORACLE_SID=mesdb",
		"/u01/app/oracle/product/19.3.0/db/bin/dgmgrl -silent",
	} {
		if !strings.Contains(command, expected) {
			t.Fatalf("command missing %q: %s", expected, command)
		}
	}
	if strings.Contains(command, "never-print-this") {
		t.Fatalf("Oracle password leaked through process arguments: %s", command)
	}
	input := strings.Join(runner.inputs, "\n")
	for _, expected := range []string{
		`CONNECT clusterguard_dg/"never-print-this"@mesdb AS SYSDG`, "SHOW CONFIGURATION",
		"SHOW DATABASE VERBOSE mesdb", "VALIDATE DATABASE VERBOSE reportdb",
	} {
		if !strings.Contains(input, expected) {
			t.Fatalf("DGMGRL input missing %q: %s", expected, input)
		}
	}
}

func TestOracleControllerDiscoveryUsesLocalBrokerIdentity(t *testing.T) {
	runner := &oracleInputRunnerStub{outputs: []string{`
Configuration - MESDB_DG
Configuration Status:
SUCCESS
Database - mesdb
  Role: PRIMARY
  Database Status: SUCCESS
  Transport Lag: 0 seconds
  Apply Lag: 0 seconds
`, "1234567890|mesdb|mesdb|PRIMARY|READ WRITE|TRUE\n"}}
	controller := testOracleController(t, runner)
	status, err := controller.Discover(context.Background(), oracleControllerPolicy())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if status.Configuration != "MESDB_DG" || status.DBID != "1234567890" || status.Database != "mesdb" ||
		status.InstanceName != "mesdb" || status.Role != "PRIMARY" ||
		status.ConfigurationStatus != "SUCCESS" || status.DatabaseStatus != "SUCCESS" ||
		!status.BrokerEnabled {
		t.Fatalf("discovery status=%+v", status)
	}
	input := strings.Join(runner.inputs, "\n")
	if strings.Contains(input, "VALIDATE DATABASE") ||
		!strings.Contains(input, "SHOW DATABASE VERBOSE mesdb") ||
		!strings.Contains(input, "from v$database d cross join v$instance i") {
		t.Fatalf("discovery input=%s", input)
	}
}

func TestOracleControllerDiscoveryRejectsIdentityMismatch(t *testing.T) {
	runner := &oracleInputRunnerStub{outputs: []string{`
Configuration - MESDB_DG
Configuration Status:
SUCCESS
Role: PRIMARY
Database Status:
SUCCESS
`, "1234567890|otherdb|mesdb|PRIMARY|READ WRITE|TRUE\n"}}
	controller := testOracleController(t, runner)
	if _, err := controller.Discover(context.Background(), oracleControllerPolicy()); err == nil {
		t.Fatal("mismatched SQLPlus identity was accepted")
	}
}

func TestOracleControllerSwitchoverRequiresExplicitSuccess(t *testing.T) {
	runner := &oracleInputRunnerStub{outputs: []string{`
Configuration - MESDB_DG
Configuration Status:
SUCCESS
Database Role: PRIMARY
Database Status: SUCCESS
`, `
Ready for Switchover: Yes
Transport Lag: 0 seconds
Apply Lag: 0 seconds
`, `
Configuration - MESDB_DG
Switchover succeeded, new primary is "reportdb"
Configuration Status:
SUCCESS
`}}
	controller := testOracleController(t, runner)
	if err := controller.Switchover(context.Background(), oracleControllerPolicy(), "reportdb"); err != nil {
		t.Fatalf("switchover: %v", err)
	}
	input := strings.Join(runner.inputs, "\n")
	if !strings.Contains(input, "VALIDATE DATABASE VERBOSE reportdb") ||
		!strings.Contains(input, "SWITCHOVER TO reportdb") {
		t.Fatalf("switchover input=%s", input)
	}

	runner.outputs = []string{`
Configuration - MESDB_DG
Configuration Status:
SUCCESS
Database Role: PRIMARY
Database Status: SUCCESS
`, `
Ready for Switchover: Yes
Transport Lag: 0 seconds
Apply Lag: 0 seconds
`, "Configuration - MESDB_DG\nConfiguration Status:\nSUCCESS\n"}
	runner.output = "Configuration Status:\nSUCCESS\n"
	if err := controller.Switchover(context.Background(), oracleControllerPolicy(), "reportdb"); err == nil {
		t.Fatal("switchover without explicit success evidence was accepted")
	}
}

func TestOracleControllerRedactsCredentialOnFailure(t *testing.T) {
	runner := &oracleInputRunnerStub{err: errors.New("failed with never-print-this")}
	controller := testOracleController(t, runner)
	err := controller.Switchover(context.Background(), oracleControllerPolicy(), "reportdb")
	if err == nil || strings.Contains(err.Error(), "never-print-this") {
		t.Fatalf("credential leaked in error: %v", err)
	}
}
