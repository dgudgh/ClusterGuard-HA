package postgresql

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const postgresqlMetricsQuery = `SELECT row_to_json(clusterguard_metrics) FROM (
  SELECT
    (SELECT count(*) FROM pg_stat_activity WHERE datid IS NOT NULL)::text AS connections,
    (SELECT count(*) FROM pg_stat_activity WHERE datid IS NOT NULL AND state = 'active')::text AS active_connections,
	    COALESCE((SELECT sum(xact_commit + xact_rollback) FROM pg_stat_database), 0)::text AS transactions_total,
	    COALESCE((SELECT sum(deadlocks) FROM pg_stat_database), 0)::text AS deadlocks_total,
	    COALESCE((SELECT sum(conflicts) FROM pg_stat_database), 0)::text AS conflicts_total,
	    COALESCE((SELECT sum(temp_bytes) FROM pg_stat_database), 0)::text AS temp_bytes_total,
    COALESCE((SELECT sum(blks_read) FROM pg_stat_database), 0)::text AS blocks_read_total,
    COALESCE((SELECT sum(blks_hit) FROM pg_stat_database), 0)::text AS blocks_hit_total,
	    pg_database_size(current_database())::text AS database_size_bytes,
	    (SELECT count(*) FROM pg_stat_replication)::text AS replication_clients,
	    (CASE WHEN pg_is_in_recovery()
	      THEN pg_wal_lsn_diff(COALESCE(pg_last_wal_replay_lsn(), '0/0'::pg_lsn), '0/0'::pg_lsn)
	      ELSE pg_wal_lsn_diff(pg_current_wal_lsn(), '0/0'::pg_lsn)
	    END)::text AS wal_bytes,
	    (SELECT CASE
	      WHEN to_jsonb(bg) ? 'checkpoints_timed' AND to_jsonb(bg) ? 'checkpoints_req'
	      THEN ((to_jsonb(bg)->>'checkpoints_timed')::numeric + (to_jsonb(bg)->>'checkpoints_req')::numeric)::text
	      ELSE NULL
	    END FROM pg_stat_bgwriter AS bg) AS checkpoints_total,
	    (SELECT EXTRACT(EPOCH FROM (clock_timestamp() - min(xact_start)))::text FROM pg_stat_activity WHERE xact_start IS NOT NULL) AS max_transaction_age_seconds
) AS clusterguard_metrics`

func parsePostgreSQLMetrics(rows []Row) (map[string]float64, error) {
	if len(rows) != 1 {
		return nil, fmt.Errorf("PostgreSQL metrics query returned %d rows", len(rows))
	}
	row := rows[0]
	parse := func(name string, required bool) (float64, bool, error) {
		raw, found := row[name]
		raw = strings.TrimSpace(raw)
		if !found || raw == "" {
			if required {
				return 0, false, fmt.Errorf("PostgreSQL metrics are missing %s", name)
			}
			return 0, false, nil
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, false, fmt.Errorf("invalid PostgreSQL metric %s=%q", name, raw)
		}
		return value, true, nil
	}
	required := []string{
		"connections", "active_connections", "transactions_total", "deadlocks_total", "temp_bytes_total",
		"blocks_read_total", "blocks_hit_total", "database_size_bytes", "replication_clients",
	}
	values := make(map[string]float64, len(required)+2)
	for _, name := range required {
		value, _, err := parse(name, true)
		if err != nil {
			return nil, err
		}
		values[name] = value
	}
	blocks := values["blocks_read_total"] + values["blocks_hit_total"]
	ratio := 1.0
	if blocks > 0 {
		ratio = values["blocks_hit_total"] / blocks
	}
	values["buffer_cache_hit_ratio"] = math.Max(0, math.Min(1, ratio))
	for _, name := range []string{"conflicts_total", "wal_bytes", "checkpoints_total", "max_transaction_age_seconds"} {
		if value, found, err := parse(name, false); err != nil {
			return nil, err
		} else if found {
			values[name] = value
		}
	}
	return values, nil
}

func queryPostgreSQLMetrics(ctx context.Context, runner SQLRunner, request adapter.DiscoverRequest) ([]model.MetricSample, error) {
	if runner == nil {
		return nil, fmt.Errorf("PostgreSQL query runner is required")
	}
	rows, err := runner.Query(ctx, request.Endpoint, request.Credentials, postgresqlMetricsQuery)
	if err != nil {
		return nil, err
	}
	values, err := parsePostgreSQLMetrics(rows)
	if err != nil {
		return nil, err
	}
	return []model.MetricSample{{ObservedAt: time.Now().UTC(), Values: values}}, nil
}
