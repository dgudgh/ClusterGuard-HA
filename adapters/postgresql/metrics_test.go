package postgresql

import (
	"context"
	"math"
	"testing"
)

func TestParsePostgreSQLMetricsReturnsNativeCountersAndGauges(t *testing.T) {
	values, err := parsePostgreSQLMetrics([]Row{{
		"connections": "18", "active_connections": "3", "transactions_total": "1200",
		"deadlocks_total": "2", "temp_bytes_total": "4096", "blocks_read_total": "25",
		"blocks_hit_total": "975", "database_size_bytes": "1048576", "replication_clients": "2",
		"conflicts_total": "3", "wal_bytes": "8192", "checkpoints_total": "9", "max_transaction_age_seconds": "12.5",
	}})
	if err != nil {
		t.Fatalf("parse PostgreSQL metrics: %v", err)
	}
	want := map[string]float64{
		"connections": 18, "active_connections": 3, "transactions_total": 1200,
		"deadlocks_total": 2, "temp_bytes_total": 4096, "blocks_read_total": 25,
		"blocks_hit_total": 975, "buffer_cache_hit_ratio": 0.975,
		"database_size_bytes": 1048576, "replication_clients": 2, "conflicts_total": 3,
		"wal_bytes": 8192, "checkpoints_total": 9, "max_transaction_age_seconds": 12.5,
	}
	for name, expected := range want {
		if math.Abs(values[name]-expected) > 0.000001 {
			t.Fatalf("%s=%v want %v; values=%+v", name, values[name], expected, values)
		}
	}
}

func TestPostgreSQLMetricsUseOneReadOnlyQuery(t *testing.T) {
	runner := &fakeRunner{rows: []Row{{
		"connections": "1", "active_connections": "0", "transactions_total": "2",
		"deadlocks_total": "0", "temp_bytes_total": "0", "blocks_read_total": "0",
		"blocks_hit_total": "0", "database_size_bytes": "100", "replication_clients": "0",
		"max_transaction_age_seconds": "",
	}}}
	samples, err := New(runner).Metrics(context.Background(), postgresqlRequest())
	if err != nil {
		t.Fatalf("PostgreSQL metrics: %v", err)
	}
	if len(samples) != 1 || samples[0].ObservedAt.IsZero() || samples[0].InstanceID != "" || samples[0].Values["transactions_total"] != 2 {
		t.Fatalf("samples=%+v", samples)
	}
	if len(runner.queries) != 1 || runner.queries[0] != postgresqlMetricsQuery {
		t.Fatalf("queries=%v", runner.queries)
	}
	if _, found := samples[0].Values["max_transaction_age_seconds"]; found {
		t.Fatalf("unknown transaction age was synthesized: %+v", samples[0].Values)
	}
}

func TestParsePostgreSQLMetricsRejectsIncompleteOrInvalidRows(t *testing.T) {
	for name, rows := range map[string][]Row{
		"missing row": nil,
		"invalid numeric": {{
			"connections": "NaN", "active_connections": "0", "transactions_total": "2",
			"deadlocks_total": "0", "temp_bytes_total": "0", "blocks_read_total": "0",
			"blocks_hit_total": "0", "database_size_bytes": "100", "replication_clients": "0",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePostgreSQLMetrics(rows); err == nil {
				t.Fatal("invalid PostgreSQL metrics were accepted")
			}
		})
	}
}
