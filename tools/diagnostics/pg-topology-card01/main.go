// Card 01 reproduces discovery behavior offline; it never connects to a database.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
)

const systemID = "7678901569924304935"

type node struct{ name, platformID, nativeID, ip string }

var nodes = []node{
	{"pg02", "f936f3da-15b4-4b33-a189-bfc28b7aba76", "5229477a-212a-4015-b0b7-e1ce53016f87", "192.168.102.153"},
	{"pg01", "a248d206-2d6d-4df7-b1c4-2cf79d5b1e2f", "a7dbb99d-7b75-4e3d-851d-6f0a25871306", "192.168.102.152"},
	{"pg03", "9cabbf9f-4971-4f01-9ce0-574606dc93bc", "d89ed249-b4fb-47d7-8f52-3617ac49c1cb", "192.168.102.154"},
}

type fixtureRunner struct {
	row     postgresql.Row
	queries []string
}

func (r *fixtureRunner) Query(_ context.Context, _ adapter.Endpoint, _ adapter.Credentials, sql string) ([]postgresql.Row, error) {
	r.queries = append(r.queries, sql)
	return []postgresql.Row{r.row}, nil
}

type instanceResult struct {
	Node     string               `json:"node"`
	Role     model.InstanceRole   `json:"role"`
	Health   model.HealthState    `json:"health"`
	Summary  string               `json:"summary"`
	Source   model.EngineIdentity `json:"source_identity"`
	RawLinks int                  `json:"adapter_links"`
	Error    string               `json:"error,omitempty"`
}

type result struct {
	Case       string           `json:"case"`
	Instances  []instanceResult `json:"instances"`
	Edges      []string         `json:"resolved_edges"`
	RawLinks   int              `json:"adapter_links"`
	Unresolved int              `json:"unresolved_sources"`
	SelfLinks  int              `json:"self_links"`
	ErrorCount int              `json:"discovery_errors"`
}

func rowFor(n node, primaryID string) postgresql.Row {
	inRecovery := n.name != "pg02"
	row := postgresql.Row{
		"node_id": n.nativeID, "primary_node_id": primaryID,
		"system_identifier": systemID, "hostname": n.name, "port": "5432", "version": "16.4",
		"in_recovery": fmt.Sprint(inRecovery), "transaction_read_only": fmt.Sprint(inRecovery),
		"replay_paused": "false", "wal_receiver_status": "streaming", "timeline_id": "11",
		"wal_log_hints": "true", "data_checksum_version": "1", "lag_seconds": "0",
		"receive_lsn": "0/9000000", "replay_lsn": "0/9000148", "receiver_latest_end_lsn": "0/9000148",
		// Extra fake columns cannot replace evidence absent from the real query.
		"application_name": n.nativeID, "slot_active": "true",
	}
	if !inRecovery {
		row["current_lsn"] = "0/9000148"
		row["wal_receiver_status"] = ""
		row["primary_node_id"] = ""
	}
	return row
}

func nativeKey(id string) string {
	key, err := identity.InstanceKey(model.EnginePostgreSQL, model.EngineIdentity{"resource_id": id, "system_identifier": systemID})
	if err != nil {
		panic(err)
	}
	return key
}

func run(name, upstream string, wantRaw, wantResolved, wantErrors int) result {
	out := result{Case: name, Instances: []instanceResult{}, Edges: []string{}}
	registered := map[string]node{}
	for _, n := range nodes {
		registered[nativeKey(n.nativeID)] = n
	}
	for _, n := range nodes {
		runner := &fixtureRunner{row: rowFor(n, upstream)}
		engine := postgresql.New(runner)
		request := adapter.DiscoverRequest{ClusterID: "8938f553-c7ac-4589-aaf2-5d6823c4cc7e", Endpoint: adapter.Endpoint{IPAddress: n.ip, Port: 55432}}
		discovered, err := engine.Discover(context.Background(), request)
		entry := instanceResult{Node: n.name}
		if err != nil {
			entry.Error = err.Error()
			out.ErrorCount++
		} else {
			entry.Role, entry.Health = discovered.Instance.Role, discovered.Instance.Health.State
			entry.Summary, entry.Source = discovered.Instance.Health.Summary, discovered.Instance.Replication.SourceIdentity
			topology, err := engine.Topology(context.Background(), request, discovered)
			if err != nil {
				panic(err)
			}
			entry.RawLinks = len(topology.Links)
			out.RawLinks += entry.RawLinks
			for _, link := range topology.Links {
				source, ok := registered[nativeKey(link.SourceIdentity["resource_id"])]
				if !ok {
					out.Unresolved++
				} else if source.platformID == n.platformID {
					out.SelfLinks++
				} else {
					out.Edges = append(out.Edges, source.name+" -> "+n.name)
				}
			}
		}
		if len(runner.queries) != 1 || strings.Contains(runner.queries[0], "pg_stat_replication") || strings.Contains(runner.queries[0], "application_name") {
			panic("discovery query changed; reevaluate the diagnosis")
		}
		out.Instances = append(out.Instances, entry)
	}
	if out.RawLinks != wantRaw || len(out.Edges) != wantResolved || out.ErrorCount != wantErrors {
		panic(fmt.Sprintf("unexpected result for %s: raw=%d resolved=%d errors=%d", name, out.RawLinks, len(out.Edges), out.ErrorCount))
	}
	return out
}

func main() {
	results := []result{
		run("missing_primary_node_id_with_correct_application_names", "", 0, 0, 0),
		run("correct_native_primary_node_id", nodes[0].nativeID, 2, 2, 0),
		run("platform_primary_id_instead_of_native_id", nodes[0].platformID, 2, 0, 0),
		run("old_primary_pg01_native_id", nodes[1].nativeID, 2, 1, 0),
		run("invalid_primary_node_id", "not-a-uuid", 0, 0, 2),
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(struct {
		AdapterCases    []result         `json:"adapter_cases"`
		RepositoryCases []repositoryCase `json:"repository_cases"`
	}{results, replayRepository()}); err != nil {
		panic(err)
	}
}
