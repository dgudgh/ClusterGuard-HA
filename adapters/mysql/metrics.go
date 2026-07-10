package mysql

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const globalStatusQuery = "SHOW GLOBAL STATUS"

func queryMetrics(ctx context.Context, runner SQLRunner, request adapter.DiscoverRequest) ([]model.MetricSample, error) {
	rows, err := runner.Query(ctx, request.Endpoint, request.Credentials, globalStatusQuery)
	if err != nil {
		return nil, err
	}
	values, err := parseMetrics(rows)
	if err != nil {
		return nil, err
	}
	return []model.MetricSample{{ObservedAt: time.Now().UTC(), Values: values}}, nil
}

func parseMetrics(rows []Row) (map[string]float64, error) {
	status := make(map[string]string, len(rows))
	for _, row := range rows {
		name := first(row, "Variable_name", "VARIABLE_NAME")
		if name != "" {
			status[name] = first(row, "Value", "VALUE")
		}
	}
	metric := func(name string) (float64, error) {
		value, ok := status[name]
		if !ok || value == "" {
			return 0, fmt.Errorf("MySQL global status is missing %s", name)
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, fmt.Errorf("invalid MySQL global status %s=%q", name, value)
		}
		return parsed, nil
	}

	questions, err := metric("Questions")
	if err != nil {
		return nil, err
	}
	commits, err := metric("Com_commit")
	if err != nil {
		return nil, err
	}
	rollbacks, err := metric("Com_rollback")
	if err != nil {
		return nil, err
	}
	connections, err := metric("Threads_connected")
	if err != nil {
		return nil, err
	}
	runningThreads, err := metric("Threads_running")
	if err != nil {
		return nil, err
	}
	slowQueries, err := metric("Slow_queries")
	if err != nil {
		return nil, err
	}
	bufferPoolReads, err := metric("Innodb_buffer_pool_reads")
	if err != nil {
		return nil, err
	}
	bufferPoolRequests, err := metric("Innodb_buffer_pool_read_requests")
	if err != nil {
		return nil, err
	}

	bufferPoolHitRatio := 1.0
	if bufferPoolRequests != 0 {
		bufferPoolHitRatio = 1 - bufferPoolReads/bufferPoolRequests
	}
	bufferPoolHitRatio = math.Max(0, math.Min(1, bufferPoolHitRatio))
	return map[string]float64{
		"questions_total":       questions,
		"transactions_total":    commits + rollbacks,
		"connections":           connections,
		"running_threads":       runningThreads,
		"slow_queries_total":    slowQueries,
		"buffer_pool_hit_ratio": bufferPoolHitRatio,
	}, nil
}
