package postgresql

import (
	"context"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	testPostgreSQLClusterID = "11111111-1111-4111-8111-111111111111"
	testPostgreSQLNodeID    = "22222222-2222-4222-8222-222222222222"
	testPostgreSQLPrimaryID = "33333333-3333-4333-8333-333333333333"
)

type fakeRunner struct {
	rows    []Row
	err     error
	queries []string
}

func (runner *fakeRunner) Query(_ context.Context, _ adapter.Endpoint, _ adapter.Credentials, query string) ([]Row, error) {
	runner.queries = append(runner.queries, query)
	return runner.rows, runner.err
}

func TestDiscoverPrimaryUsesStableNodeAndSystemIdentities(t *testing.T) {
	runner := &fakeRunner{rows: []Row{primaryProbeRow()}}
	result, err := discover(context.Background(), runner, postgresqlRequest())
	if err != nil {
		t.Fatalf("discover primary: %v", err)
	}
	instance := result.Instance
	if instance.Engine != model.EnginePostgreSQL || instance.Role != model.RolePrimary || instance.Health.State != model.HealthHealthy {
		t.Fatalf("unexpected primary: %+v", instance)
	}
	if instance.EngineIdentity["resource_id"] != testPostgreSQLNodeID || instance.EngineIdentity["system_identifier"] != "7428625847249870011" {
		t.Fatalf("unexpected identities: %+v", instance.EngineIdentity)
	}
	if instance.Hostname != "pg-renamed" || instance.IPAddress != "192.0.2.30" || instance.Port != 5432 {
		t.Fatalf("unexpected mutable endpoint: %+v", instance)
	}
	if instance.EngineMetadata["timeline_id"] != "7" || instance.EngineMetadata["current_lsn"] != "0/5000060" {
		t.Fatalf("unexpected PostgreSQL metadata: %+v", instance.EngineMetadata)
	}
	if instance.EngineMetadata["wal_log_hints"] != "true" || instance.EngineMetadata["data_checksum_version"] != "1" {
		t.Fatalf("rewind prerequisites were not discovered: %+v", instance.EngineMetadata)
	}
	if len(runner.queries) != 1 || strings.Contains(strings.ToLower(runner.queries[0]), "primary_conninfo") {
		t.Fatalf("probe must be single-shot and must not read secrets: %v", runner.queries)
	}
}

func TestDiscoverKeepsPublishedContainerPort(t *testing.T) {
	request := postgresqlRequest()
	request.Endpoint.Port = 55432
	row := primaryProbeRow()
	row["port"] = "5432"
	result, err := discover(context.Background(), &fakeRunner{rows: []Row{row}}, request)
	if err != nil {
		t.Fatalf("discover published PostgreSQL endpoint: %v", err)
	}
	if result.Instance.Port != 55432 {
		t.Fatalf("published PostgreSQL port was replaced by container port: %+v", result.Instance)
	}
}

func TestDiscoverHealthyStreamingStandby(t *testing.T) {
	runner := &fakeRunner{rows: []Row{standbyProbeRow()}}
	result, err := discover(context.Background(), runner, postgresqlRequest())
	if err != nil {
		t.Fatalf("discover standby: %v", err)
	}
	instance := result.Instance
	if instance.Role != model.RoleStandby || instance.Health.State != model.HealthHealthy || !instance.PromotionEligible {
		t.Fatalf("unexpected standby: %+v", instance)
	}
	if instance.Replication.SourceIdentity["resource_id"] != testPostgreSQLPrimaryID || instance.Replication.SourceIdentity["system_identifier"] != "7428625847249870011" {
		t.Fatalf("unexpected source identity: %+v", instance.Replication.SourceIdentity)
	}
	if instance.Replication.IOThread != model.ThreadRunning || instance.Replication.SQLThread != model.ThreadRunning {
		t.Fatalf("unexpected replication state: %+v", instance.Replication)
	}
	if instance.Replication.LagSeconds == nil || *instance.Replication.LagSeconds != 2 {
		t.Fatalf("unexpected lag: %+v", instance.Replication)
	}
}

func TestDiscoverStandbyWithUnknownLagDoesNotInventZero(t *testing.T) {
	row := standbyProbeRow()
	row["lag_seconds"] = ""
	result, err := discover(context.Background(), &fakeRunner{rows: []Row{row}}, postgresqlRequest())
	if err != nil {
		t.Fatalf("discover standby: %v", err)
	}
	if result.Instance.Replication.LagSeconds != nil {
		t.Fatalf("unknown lag was invented: %+v", result.Instance.Replication)
	}
}

func TestDiscoverStandbyRetainsReceiverEndEvidenceAfterTimelineChange(t *testing.T) {
	row := standbyProbeRow()
	row["receive_lsn"] = "0/5000000"
	row["replay_lsn"] = "0/5000050"
	row["receiver_latest_end_lsn"] = "0/5000050"
	row["lag_seconds"] = "0"
	result, err := discover(context.Background(), &fakeRunner{rows: []Row{row}}, postgresqlRequest())
	if err != nil {
		t.Fatalf("discover standby after timeline change: %v", err)
	}
	if result.Instance.Replication.LagSeconds == nil || *result.Instance.Replication.LagSeconds != 0 {
		t.Fatalf("receiver end evidence did not preserve zero lag: %+v", result.Instance.Replication)
	}
	if result.Instance.EngineMetadata["receiver_latest_end_lsn"] != row["replay_lsn"] {
		t.Fatalf("receiver end LSN evidence is missing: %+v", result.Instance.EngineMetadata)
	}
}

func TestIdentityQueryReportsZeroLagWhenStandbyReplayedAllReceivedWAL(t *testing.T) {
	replayedAll := "pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn()"
	receiverStreaming := "(SELECT status FROM receiver) = 'streaming'"
	receiverCaughtUp := "(SELECT latest_end_lsn FROM receiver) = pg_last_wal_replay_lsn()"
	timestampFallback := "pg_last_xact_replay_timestamp() IS NOT NULL"
	replayedAllIndex := strings.Index(identityQuery, replayedAll)
	receiverCaughtUpIndex := strings.Index(identityQuery, receiverCaughtUp)
	timestampFallbackIndex := strings.Index(identityQuery, timestampFallback)
	if replayedAllIndex < 0 {
		t.Fatalf("identity query does not detect a fully replayed standby: %s", identityQuery)
	}
	if timestampFallbackIndex < 0 || replayedAllIndex > timestampFallbackIndex {
		t.Fatalf("fully replayed WAL must take precedence over timestamp lag: %s", identityQuery)
	}
	if !strings.Contains(identityQuery, receiverStreaming) || receiverCaughtUpIndex < 0 || receiverCaughtUpIndex > timestampFallbackIndex {
		t.Fatalf("streaming receiver end LSN must recover zero-lag evidence after a timeline change: %s", identityQuery)
	}
}

func TestIdentityQueryUsesPostgreSQL16ChecksumField(t *testing.T) {
	if !strings.Contains(identityQuery, "(pg_control_init()).data_page_checksum_version") {
		t.Fatalf("identity query must use PostgreSQL 16 data_page_checksum_version: %s", identityQuery)
	}
	if strings.Contains(identityQuery, "(pg_control_init()).data_checksum_version") {
		t.Fatalf("identity query uses a field that does not exist in PostgreSQL 16: %s", identityQuery)
	}
}

func TestIdentityQueryOnlyChecksReplayPauseDuringRecovery(t *testing.T) {
	guarded := "CASE WHEN pg_is_in_recovery() THEN pg_is_wal_replay_paused() ELSE false END AS replay_paused"
	if !strings.Contains(identityQuery, guarded) {
		t.Fatalf("identity query must not call pg_is_wal_replay_paused on a primary: %s", identityQuery)
	}
}

func TestIdentityQueryUsesReceivedTimelineForStreamingStandby(t *testing.T) {
	receiverSnapshot := "SELECT status, received_tli, latest_end_lsn, sender_host, sender_port\n  FROM pg_stat_wal_receiver"
	receiverTimeline := "SELECT received_tli::text FROM receiver"
	checkpointTimeline := "(pg_control_checkpoint()).timeline_id::text"
	if !strings.Contains(identityQuery, receiverSnapshot) || !strings.Contains(identityQuery, receiverTimeline) {
		t.Fatalf("identity query must use the WAL receiver timeline for a streaming standby: %s", identityQuery)
	}
	if !strings.Contains(identityQuery, "COALESCE(") || strings.Index(identityQuery, receiverTimeline) > strings.LastIndex(identityQuery, checkpointTimeline) {
		t.Fatalf("checkpoint timeline must only be a standby fallback after WAL receiver evidence: %s", identityQuery)
	}
}

func TestDiscoverPausedStandbyFailsPromotionEligibility(t *testing.T) {
	row := standbyProbeRow()
	row["replay_paused"] = "true"
	result, err := discover(context.Background(), &fakeRunner{rows: []Row{row}}, postgresqlRequest())
	if err != nil {
		t.Fatalf("discover paused standby: %v", err)
	}
	instance := result.Instance
	if instance.Health.State != model.HealthDegraded || instance.PromotionEligible || instance.Replication.SQLThread != model.ThreadStopped {
		t.Fatalf("paused replay was not degraded: %+v", instance)
	}
}

func TestDiscoverStandbyRequiresStableUpstreamIdentity(t *testing.T) {
	row := standbyProbeRow()
	row["primary_node_id"] = ""
	result, err := discover(context.Background(), &fakeRunner{rows: []Row{row}}, postgresqlRequest())
	if err != nil {
		t.Fatalf("discover identity-less standby: %v", err)
	}
	if result.Instance.Health.State != model.HealthDegraded || result.Instance.PromotionEligible {
		t.Fatalf("standby without upstream identity was not degraded: %+v", result.Instance)
	}
}

func TestDiscoverRejectsInvalidStableIdentityAndLSN(t *testing.T) {
	for name, mutate := range map[string]func(Row){
		"node UUID":         func(row Row) { row["node_id"] = "pg-01" },
		"system identifier": func(row Row) { row["system_identifier"] = "not-a-number" },
		"replay LSN":        func(row Row) { row["replay_lsn"] = "broken" },
		"receiver end LSN":  func(row Row) { row["receiver_latest_end_lsn"] = "broken" },
	} {
		t.Run(name, func(t *testing.T) {
			row := standbyProbeRow()
			mutate(row)
			if _, err := discover(context.Background(), &fakeRunner{rows: []Row{row}}, postgresqlRequest()); err == nil {
				t.Fatal("invalid probe unexpectedly succeeded")
			}
		})
	}
}

func primaryProbeRow() Row {
	return Row{
		"node_id":                 testPostgreSQLNodeID,
		"primary_node_id":         "",
		"system_identifier":       "7428625847249870011",
		"hostname":                "pg-renamed",
		"port":                    "5440",
		"version":                 "16.3",
		"in_recovery":             "false",
		"transaction_read_only":   "off",
		"replay_paused":           "false",
		"wal_receiver_status":     "",
		"current_lsn":             "0/5000060",
		"receive_lsn":             "",
		"replay_lsn":              "",
		"receiver_latest_end_lsn": "",
		"lag_seconds":             "",
		"timeline_id":             "7",
		"wal_log_hints":           "true",
		"data_checksum_version":   "1",
	}
}

func standbyProbeRow() Row {
	return Row{
		"node_id":                 testPostgreSQLNodeID,
		"primary_node_id":         testPostgreSQLPrimaryID,
		"system_identifier":       "7428625847249870011",
		"hostname":                "pg-standby-renamed",
		"port":                    "5440",
		"version":                 "16.3",
		"in_recovery":             "true",
		"transaction_read_only":   "on",
		"replay_paused":           "false",
		"wal_receiver_status":     "streaming",
		"current_lsn":             "",
		"receive_lsn":             "0/5000050",
		"replay_lsn":              "0/5000040",
		"receiver_latest_end_lsn": "0/5000050",
		"lag_seconds":             "2",
		"timeline_id":             "7",
		"wal_log_hints":           "true",
		"data_checksum_version":   "1",
	}
}

func postgresqlRequest() adapter.DiscoverRequest {
	return adapter.DiscoverRequest{
		ClusterID:   model.ResourceID(testPostgreSQLClusterID),
		Endpoint:    adapter.Endpoint{Hostname: "pg-old", IPAddress: "192.0.2.30", Port: 5432},
		Credentials: adapter.Credentials{Username: "monitor", Password: "secret", Database: "postgres"},
	}
}
