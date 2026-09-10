package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type repositoryCase struct {
	Case              string            `json:"case"`
	AdapterLinks      int               `json:"adapter_links"`
	RepositoryLinks   int               `json:"repository_links"`
	Health            model.HealthState `json:"cluster_health"`
	Edges             []string          `json:"edges"`
	StaleRejected     bool              `json:"stale_rejected"`
	SnapshotRoundTrip bool              `json:"replicated_state_roundtrip_equal"`
	JSONRoundTrip     bool              `json:"topology_json_roundtrip_equal"`
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func replayRepository() []repositoryCase {
	repo := store.NewMemory()
	cluster, err := repo.UpsertCluster(model.DatabaseCluster{Engine: model.EnginePostgreSQL, DisplayName: "card01-offline-only"})
	must(err)
	endpoints := make(map[string]model.Endpoint)
	for _, n := range nodes {
		endpoint, err := repo.UpsertEndpoint(model.Endpoint{
			ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Active: true,
			Hostname: n.name, IPAddress: n.ip, Port: 55432,
		})
		must(err)
		endpoints[n.name] = endpoint
	}
	observedAt := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	apply := func(name, upstream string, at time.Time, wantLinks int, wantHealth model.HealthState, stale bool) repositoryCase {
		inventory, exists := repo.DiscoveryInventory(cluster.ResourceID)
		if !exists {
			panic("diagnostic inventory missing")
		}
		refresh := store.DiscoveryRefresh{
			ClusterID: cluster.ResourceID, ClusterIdentity: model.EngineIdentity{"system_identifier": systemID},
			InventoryGeneration: inventory.Generation, TopologyAuthoritative: true, ObservedAt: at,
			Health: model.Health{State: model.HealthHealthy, ObservedAt: at},
		}
		for _, n := range nodes {
			engine := postgresql.New(&fixtureRunner{row: rowFor(n, upstream)})
			request := adapter.DiscoverRequest{ClusterID: cluster.ResourceID, Endpoint: adapter.Endpoint{IPAddress: n.ip, Port: 55432}}
			discovered, err := engine.Discover(context.Background(), request)
			must(err)
			topology, err := engine.Topology(context.Background(), request, discovered)
			must(err)
			refresh.Observations = append(refresh.Observations, store.DiscoveryObservation{EndpointID: endpoints[n.name].ResourceID, Instance: discovered.Instance})
			for _, link := range topology.Links {
				refresh.NativeLinks = append(refresh.NativeLinks, model.NativeReplicationLink{
					SourceIdentity: link.SourceIdentity, TargetIdentity: link.TargetIdentity,
					Healthy: link.Healthy, LagSeconds: link.LagSeconds,
				})
			}
		}
		snapshot, err := repo.ApplyDiscoveryRefresh(refresh)
		if stale {
			if !errors.Is(err, store.ErrStaleObservation) {
				panic(fmt.Sprintf("expected stale rejection, got %v", err))
			}
			var ok bool
			snapshot, ok = repo.TopologySnapshot(cluster.ResourceID)
			if !ok {
				panic("topology missing after stale observation")
			}
		} else {
			must(err)
		}
		if len(snapshot.Links) != wantLinks || snapshot.Health.State != wantHealth {
			panic(fmt.Sprintf("%s: links=%d health=%s", name, len(snapshot.Links), snapshot.Health.State))
		}
		// Exercise the actual replicated-state codec and restore path, without a Raft network.
		state, err := repo.ReplicatedState()
		must(err)
		follower := store.NewMemory()
		must(follower.ApplyReplicatedState(state))
		restored, ok := follower.TopologySnapshot(cluster.ResourceID)
		if !ok || !reflect.DeepEqual(snapshot, restored) {
			panic("replicated-state roundtrip changed topology")
		}
		encoded, err := json.Marshal(snapshot)
		must(err)
		var decoded model.TopologySnapshot
		must(json.Unmarshal(encoded, &decoded))
		reencoded, err := json.Marshal(decoded)
		must(err)
		if !bytes.Equal(encoded, reencoded) {
			panic("topology JSON roundtrip changed topology")
		}
		names := make(map[model.ResourceID]string)
		for _, instance := range snapshot.Instances {
			for _, n := range nodes {
				if instance.EngineIdentity["resource_id"] == n.nativeID {
					names[instance.ResourceID] = n.name
				}
			}
		}
		edges := []string{}
		for _, link := range snapshot.Links {
			edges = append(edges, names[link.SourceInstanceID]+" -> "+names[link.TargetInstanceID])
		}
		sort.Strings(edges)
		return repositoryCase{
			Case: name, AdapterLinks: len(refresh.NativeLinks), RepositoryLinks: len(snapshot.Links),
			Health: snapshot.Health.State, Edges: edges, StaleRejected: stale,
			SnapshotRoundTrip: true, JSONRoundTrip: true,
		}
	}
	return []repositoryCase{
		apply("native_identity_resolves_and_persists", nodes[0].nativeID, observedAt, 2, model.HealthHealthy, false),
		apply("older_missing_identity_cannot_overwrite", "", observedAt.Add(-time.Second), 2, model.HealthHealthy, true),
		apply("newer_missing_identity_retires_old_edges", "", observedAt.Add(time.Second), 0, model.HealthDegraded, false),
		apply("later_native_identity_rebuilds_edges", nodes[0].nativeID, observedAt.Add(2*time.Second), 2, model.HealthHealthy, false),
		apply("platform_id_is_not_native_identity", nodes[0].platformID, observedAt.Add(3*time.Second), 0, model.HealthDegraded, false),
		apply("old_native_primary_self_link_is_rejected", nodes[1].nativeID, observedAt.Add(4*time.Second), 1, model.HealthDegraded, false),
	}
}
