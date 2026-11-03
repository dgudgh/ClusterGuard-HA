package postgresql

import (
	"context"
	"strings"
	"testing"
)

func TestLiveSourceLossRevalidatesBoundHistoryWithoutBootstrapGUC(t *testing.T) {
	for _, changed := range []bool{false, true} {
		req := postgresqlSourceLossFailoverRequest()
		target := &req.Resolved.Target
		fp := strings.Repeat("a", 64)
		target.EngineMetadata["verified_upstream_source_endpoint"] = observedEndpointKey(postgresqlInstanceEndpoint(req.Resolved.Primary))
		target.EngineMetadata["verified_upstream_endpoint"] = observedEndpointKey(postgresqlInstanceEndpoint(*target))
		target.EngineMetadata["verified_upstream_config"] = fp
		target.EngineMetadata["verified_upstream_timeline"] = target.EngineMetadata["timeline_id"]
		for i := range req.Resolved.Snapshot.Instances {
			if req.Resolved.Snapshot.Instances[i].ResourceID == target.ResourceID {
				req.Resolved.Snapshot.Instances[i] = *target
			}
		}
		row := postgresqlLiveRow(*target, "", false)
		row["primary_node_id"] = ""
		row["wal_receiver_status"] = ""
		row["receive_lsn"] = "0/5000070"
		row["replay_lsn"] = "0/5000070"
		row["lag_seconds"] = "0"
		row["replication_config_fingerprint"] = fp
		if changed {
			row["replication_config_fingerprint"] = strings.Repeat("b", 64)
		}
		runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{target.Hostname: {row}}}
		err := New(runner).postgresqlLiveFailoverTargetPrecheck(context.Background(), *req.Resolved)
		if (err != nil) != changed {
			t.Fatalf("changed=%v live precheck=%v", changed, err)
		}
	}
}
