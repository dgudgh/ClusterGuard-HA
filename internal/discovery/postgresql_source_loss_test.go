package discovery

import (
	pgadapter "clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"context"
	"errors"
	"testing"
	"time"
)

func TestPostgreSQLSourceLossKeepsOnlyVerifiedBoundIdentity(t *testing.T) {
	for _, change := range []string{"none", "config", "timeline", "native", "replay paused", "lag", "unreplayed", "wal regression", "no previous proof", "source endpoint move"} {
		t.Run(change, func(t *testing.T) {
			f := newPostgreSQLTopologyFixture(t)
			for _, host := range []string{"pg01", "pg03"} {
				f.runner.updateRow(host, func(r pgadapter.Row) {
					r["replication_config_fingerprint"] = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
				})
			}
			if change != "no previous proof" {
				if _, err := f.service.Refresh(context.Background(), f.cluster.ResourceID); err != nil {
					t.Fatal(err)
				}
			}
			if change == "source endpoint move" {
				inventory, _ := f.repository.DiscoveryInventory(f.cluster.ResourceID)
				for _, e := range inventory.Endpoints {
					if e.Hostname == "pg02" {
						e.IPAddress = "10.20.0.22"
						if _, err := f.repository.UpsertEndpoint(e); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			f.runner.setIdentityError("pg02", errors.New("connection refused"))
			f.clock.Set(pgReplayStart.Add(5 * time.Second))
			f.runner.updateRow("pg01", func(r pgadapter.Row) {
				r["wal_receiver_status"] = ""
				r["receiver_sender_host"] = ""
				r["receiver_sender_port"] = ""
				r["receive_lsn"] = "0/5000060"
				r["replay_lsn"] = "0/5000060"
				switch change {
				case "config":
					r["replication_config_fingerprint"] = "1111111111111111111111111111111111111111111111111111111111111111"
				case "timeline":
					r["timeline_id"] = "8"
				case "native":
					r["node_id"] = pgReplayOldPrimaryGUC
				case "replay paused":
					r["replay_paused"] = "true"
				case "lag":
					r["lag_seconds"] = "1"
				case "unreplayed":
					r["receive_lsn"] = "0/5000070"
				case "wal regression":
					r["receive_lsn"] = "0/4000000"
					r["replay_lsn"] = "0/4000000"
				}
			})
			snapshot, err := f.service.Refresh(context.Background(), f.cluster.ResourceID)
			if err != nil {
				if change == "native" || change == "no previous proof" {
					return
				}
				t.Fatal(err)
			}
			var target, primary model.DatabaseInstance
			for _, i := range snapshot.Instances {
				if i.IPAddress == "10.20.0.1" {
					target = i
				}
				if i.EngineIdentity["resource_id"] == pgReplayPrimaryNativeID {
					primary = i
				}
			}
			bound := target.Replication.SourceIdentity["resource_id"] == pgReplayPrimaryNativeID
			if change == "none" {
				if !bound || target.Health.State == model.HealthHealthy || target.PromotionEligible {
					t.Fatalf("lost verified historical source or invented healthy streaming: %+v", target)
				}
				cluster, _ := f.repository.Cluster(f.cluster.ResourceID)
				candidates, err := pgadapter.New().EvaluateCandidates(context.Background(), adapter.CandidateRequest{Cluster: cluster, Primary: primary, Instances: []model.DatabaseInstance{target}, Probes: snapshot.Probes, ObservedAt: snapshot.ObservedAt, Policy: model.CandidatePolicy{AllowSourceDisconnected: true, MaximumLagSeconds: 0}})
				if err != nil || len(candidates) != 1 || !candidates[0].Eligible {
					t.Fatalf("real discovery cannot reach safe source-loss candidate: %+v %v", candidates, err)
				}
				// A second disconnected round must retain the original binding, not mint a
				// new proof or require the primary to reappear before automatic recovery.
				f.clock.Set(pgReplayStart.Add(10 * time.Second))
				again, err := f.service.Refresh(context.Background(), f.cluster.ResourceID)
				if err != nil {
					t.Fatal(err)
				}
				for _, i := range again.Instances {
					if i.ResourceID == target.ResourceID && i.Replication.SourceIdentity["resource_id"] != pgReplayPrimaryNativeID {
						t.Fatal("lost provenance on subsequent source-loss round")
					}
				}
			} else if bound {
				t.Fatalf("unsafe %s retained trusted source: %+v", change, target)
			}
		})
	}
}
