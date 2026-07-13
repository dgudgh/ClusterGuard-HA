package agent

import (
	"context"
	"strings"
	"testing"
)

func TestMySQLRoleStatusReadsBothGlobalReadOnlyFlags(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string]string{}}
	controller := NewMySQLRoleController(runner, "/usr/local/mysql/bin/mysql", t.TempDir())
	policy := ClusterPolicy{MySQLPort: 3306, MySQLDefaultsFile: "/etc/clusterguard/mysql.cnf"}
	readOnly, superReadOnly, err := controller.Status(context.Background(), policy)
	if err == nil {
		t.Fatal("empty role status output was accepted")
	}
	if readOnly || superReadOnly {
		t.Fatalf("empty output invented role flags: read_only=%v super_read_only=%v", readOnly, superReadOnly)
	}
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only") {
		t.Fatalf("role status command=%v", runner.calls)
	}

	runner.outputs[runner.calls[0]] = "1\t1\n"
	readOnly, superReadOnly, err = controller.Status(context.Background(), policy)
	if err != nil || !readOnly || !superReadOnly {
		t.Fatalf("role status: read_only=%v super_read_only=%v err=%v", readOnly, superReadOnly, err)
	}
}
