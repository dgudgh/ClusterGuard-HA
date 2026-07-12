package mysql

import "testing"

func TestMySQLDialectSelectsLegacyAndModernReplicationStatements(t *testing.T) {
	tests := []struct {
		version string
		stop    string
		reset   string
	}{
		{version: "5.7.44-log", stop: "STOP SLAVE", reset: "RESET SLAVE ALL"},
		{version: "8.0.46", stop: "STOP REPLICA", reset: "RESET REPLICA ALL"},
		{version: "8.4.10", stop: "STOP REPLICA", reset: "RESET REPLICA ALL"},
		{version: "9.7.0", stop: "STOP REPLICA", reset: "RESET REPLICA ALL"},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			dialect, err := dialectForVersion(test.version)
			if err != nil {
				t.Fatalf("dialect: %v", err)
			}
			if dialect.StopReplication != test.stop || dialect.ResetReplication != test.reset {
				t.Fatalf("dialect=%+v, want stop=%q reset=%q", dialect, test.stop, test.reset)
			}
		})
	}
	if _, err := dialectForVersion("5.6.51"); err == nil {
		t.Fatal("unsupported MySQL release produced a mutation dialect")
	}
}
