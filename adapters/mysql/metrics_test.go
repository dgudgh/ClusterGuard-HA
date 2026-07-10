package mysql

import (
	"context"
	"math"
	"testing"
)

func TestParseMetricsReturnsPortableCountersAndGauges(t *testing.T) {
	values, err := parseMetrics(statusRows("1000", "60", "40", "18", "3", "7", "25", "1000"))
	if err != nil {
		t.Fatalf("parse metrics: %v", err)
	}
	want := map[string]float64{
		"questions_total":       1000,
		"transactions_total":    100,
		"connections":           18,
		"running_threads":       3,
		"slow_queries_total":    7,
		"buffer_pool_hit_ratio": 0.975,
	}
	for key, wantValue := range want {
		if math.Abs(values[key]-wantValue) > 0.000001 {
			t.Fatalf("%s: got %v want %v (all values: %+v)", key, values[key], wantValue, values)
		}
	}
}

func TestParseMetricsClampsBufferPoolHitRatio(t *testing.T) {
	for _, test := range []struct {
		name     string
		reads    string
		requests string
		want     float64
	}{
		{name: "below zero", reads: "150", requests: "100", want: 0},
		{name: "above one", reads: "-10", requests: "100", want: 1},
		{name: "no requests", reads: "0", requests: "0", want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			values, err := parseMetrics(statusRows("1", "2", "3", "4", "5", "6", test.reads, test.requests))
			if err != nil {
				t.Fatalf("parse metrics: %v", err)
			}
			if values["buffer_pool_hit_ratio"] != test.want {
				t.Fatalf("ratio: got %v want %v", values["buffer_pool_hit_ratio"], test.want)
			}
		})
	}
}

func TestMetricsQueriesGlobalStatusAndLeavesInstanceIDForReconciliation(t *testing.T) {
	runner := &fakeRunner{status: statusRows("1000", "60", "40", "18", "3", "7", "25", "1000")}
	samples, err := New(runner).Metrics(context.Background(), adapterRequest())
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if len(samples) != 1 || samples[0].InstanceID != "" || samples[0].ObservedAt.IsZero() || samples[0].Values["transactions_total"] != 100 {
		t.Fatalf("unexpected metric samples: %+v", samples)
	}
	if len(runner.queries) != 1 || runner.queries[0] != "SHOW GLOBAL STATUS" {
		t.Fatalf("unexpected metric query: %v", runner.queries)
	}
}

func statusRows(questions, commits, rollbacks, connected, running, slow, reads, requests string) []Row {
	return []Row{
		{"Variable_name": "Questions", "Value": questions},
		{"Variable_name": "Com_commit", "Value": commits},
		{"Variable_name": "Com_rollback", "Value": rollbacks},
		{"Variable_name": "Threads_connected", "Value": connected},
		{"Variable_name": "Threads_running", "Value": running},
		{"Variable_name": "Slow_queries", "Value": slow},
		{"Variable_name": "Innodb_buffer_pool_reads", "Value": reads},
		{"Variable_name": "Innodb_buffer_pool_read_requests", "Value": requests},
	}
}
