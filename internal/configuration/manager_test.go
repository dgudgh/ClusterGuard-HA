package configuration

import (
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testAuthority struct {
	ID      string
	Cluster *testCluster
}

func (a testAuthority) RequireMutationAuthority(context.Context) error {
	if a.Cluster.Leader != a.ID || !a.Cluster.Quorum {
		return fmt.Errorf("no majority")
	}
	return nil
}
func (a testAuthority) Status(context.Context) consensus.Status {
	role := "follower"
	if a.ID == a.Cluster.Leader {
		role = "leader"
	}
	return consensus.Status{Enabled: true, Role: role, LeaderKnown: true, QuorumConfirmed: a.Cluster.Quorum, LocalControllerID: model.ResourceID(a.ID), LeaderID: model.ResourceID(a.Cluster.Leader), LeaderAPIAddress: a.Cluster.URLs[a.Cluster.Leader], AppliedIndex: 5, CommitIndex: 5}
}
func (a testAuthority) ControllerMembers(context.Context) ([]consensus.ControllerMember, error) {
	if a.Cluster.Leader != a.ID {
		return nil, fmt.Errorf("not leader")
	}
	members := []consensus.ControllerMember{}
	for _, id := range a.Cluster.IDs {
		members = append(members, consensus.ControllerMember{ResourceID: model.ResourceID(id), APIAddress: a.Cluster.URLs[id]})
	}
	return members, nil
}

type testRestarter struct {
	Calls int
	Fail  bool
}

func (r *testRestarter) Ready(context.Context) error { return nil }
func (r *testRestarter) RestartController(context.Context) error {
	r.Calls++
	if r.Fail {
		return fmt.Errorf("restart refused")
	}
	return nil
}

type testCluster struct {
	IDs        []string
	URLs       map[string]string
	Managers   map[string]*Manager
	Restarts   map[string]*testRestarter
	Leader     string
	Quorum     bool
	Repository *store.Repository
}

func newTestCluster(t *testing.T) *testCluster {
	t.Helper()
	root := t.TempDir()
	repo, e := store.Open(filepath.Join(root, "tasks.json"))
	if e != nil {
		t.Fatal(e)
	}
	c := &testCluster{URLs: map[string]string{}, Managers: map[string]*Manager{}, Restarts: map[string]*testRestarter{}, Repository: repo, Quorum: true}
	for i := 0; i < 3; i++ {
		c.IDs = append(c.IDs, string(model.NewResourceID()))
	}
	c.Leader = c.IDs[0]
	for i, id := range c.IDs {
		dir := filepath.Join(root, id)
		if e = os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(dir, "config.json")
		raw := map[string]any{"http_address": "127.0.0.1:3000", "allow_insecure_http": true, "metadata_path": filepath.Join(dir, "metadata.json"), "mysql": map[string]any{"discovery_interval_seconds": i + 1}, "consensus": map[string]any{"local_id": id}}
		b, _ := json.Marshal(raw)
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		file, e := config.Load(path)
		if e != nil {
			t.Fatal(e)
		}
		restart := &testRestarter{}
		m := &Manager{Repository: repo, Config: file, ConfigPath: path, Authority: testAuthority{id, c}, Restart: restart, Peer: HTTPPeer{Client: &http.Client{Timeout: time.Second}, Token: "test-control"}}
		if e = m.Initialize(); e != nil {
			t.Fatal(e)
		}
		c.Managers[id] = m
		c.Restarts[id] = restart
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-control" {
				w.WriteHeader(401)
				return
			}
			var result any
			var e error
			switch r.URL.Path {
			case "/api/v1/control-plane/configuration/node":
				result, e = m.Local(r.Context())
			case "/api/v1/control-plane/configuration/candidate":
				var p struct {
					Changes map[string]int `json:"changes"`
				}
				_ = json.NewDecoder(r.Body).Decode(&p)
				e = m.Candidate(r.Context(), p.Changes)
			case "/api/v1/control-plane/configuration/permit":
				var p Permit
				_ = json.NewDecoder(r.Body).Decode(&p)
				e = m.Permit(r.Context(), p)
			default:
				w.WriteHeader(404)
				return
			}
			if e != nil {
				w.WriteHeader(409)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
		}))
		t.Cleanup(server.Close)
		c.URLs[id] = server.URL
	}
	return c
}
func (c *testCluster) dispatch(t *testing.T) store.ConfigurationTask {
	t.Helper()
	m := c.Managers[c.Leader]
	r := Request{RequestID: string(model.NewResourceID()), NodeIDs: c.IDs, Changes: map[string]int{"mysql.discovery_interval_seconds": 9}}
	plan, e := m.Plan(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	r.PlanHash = plan.Hash
	task, e := m.Dispatch(context.Background(), r, "admin")
	if e != nil {
		t.Fatal(e)
	}
	again, e := m.Dispatch(context.Background(), r, "admin")
	if e != nil || again.TaskID != task.TaskID {
		t.Fatalf("duplicate dispatch: %v", e)
	}
	r.Changes = map[string]int{"mysql.discovery_interval_seconds": 10}
	if _, e = m.Dispatch(context.Background(), r, "admin"); e == nil {
		t.Fatal("UUID reused for different changes")
	}
	return task
}
func (c *testCluster) reload(t *testing.T, id string) {
	t.Helper()
	m := c.Managers[id]
	file, e := config.Load(m.ConfigPath)
	if e != nil {
		t.Fatal(e)
	}
	m.Config = file
	m.restartRequested = ""
	if e = m.Initialize(); e != nil {
		t.Fatal(e)
	}
}
func TestRollingConfigurationVerifiesNewProcessAndSurvivesLeaderChange(t *testing.T) {
	c := newTestCluster(t)
	ctx := context.Background()
	task := c.dispatch(t)
	originalLeader := c.Leader
	c.Managers[c.Leader].Tick(ctx)
	task, _ = c.Repository.ConfigurationTask(task.TaskID)
	if task.CurrentNode == originalLeader {
		t.Fatal("restarted leader before followers")
	}
	for step := 0; step < 3; step++ {
		task, _ = c.Repository.ConfigurationTask(task.TaskID)
		id := task.CurrentNode
		if id == "" {
			t.Fatal("no selected node")
		}
		m := c.Managers[id]
		before, _ := os.ReadFile(m.ConfigPath)
		m.Tick(ctx)
		saved, e := config.ReadConfigurationOverrides(config.ConfigurationOverridePath(m.Config.MetadataPath))
		if e != nil || saved.Values["mysql.discovery_interval_seconds"] != 9 {
			t.Fatalf("save: %v", e)
		}
		observed, _ := m.Local(ctx)
		if observed.TaskID == task.TaskID || observed.Values["mysql.discovery_interval_seconds"] == 9 {
			t.Fatal("saved file reported as new running process")
		}
		latest, _ := c.Repository.ConfigurationTask(task.TaskID)
		for _, target := range latest.Targets {
			if target.NodeID == id && target.Status == "applied" {
				t.Fatal("write acknowledged before restart")
			}
		}
		after, _ := os.ReadFile(m.ConfigPath)
		if string(before) != string(after) {
			t.Fatal("original configuration changed")
		}
		c.reload(t, id)
		if id == originalLeader {
			c.Leader = c.IDs[1]
		}
		c.Managers[c.Leader].Tick(ctx)
	}
	done, _ := c.Repository.ConfigurationTask(task.TaskID)
	if done.Active || done.Status != "succeeded" {
		t.Fatalf("not verified: %+v", done)
	}
	for _, id := range c.IDs {
		if c.Restarts[id].Calls != 1 || c.Managers[id].Config.MySQL.DiscoveryIntervalSeconds != 9 {
			t.Fatal("node not restarted and applied exactly once")
		}
	}
	reopened, e := store.Open(filepath.Join(filepath.Dir(c.Managers[c.IDs[0]].ConfigPath), "..", "tasks.json"))
	if e != nil {
		t.Fatal(e)
	}
	history, ok := reopened.ConfigurationTask(task.TaskID)
	if !ok || history.Active || history.Status != "succeeded" {
		t.Fatal("task lost on repository reopen")
	}
}
func TestFailureRetainsGateAndRollbackPreservesNodeDifferences(t *testing.T) {
	c := newTestCluster(t)
	ctx := context.Background()
	task := c.dispatch(t)
	leader := c.Managers[c.Leader]
	leader.Tick(ctx)
	task, _ = c.Repository.ConfigurationTask(task.TaskID)
	id := task.CurrentNode
	c.Restarts[id].Fail = true
	c.Managers[id].Tick(ctx)
	leader.Tick(ctx)
	failed, _ := c.Repository.ConfigurationTask(task.TaskID)
	if failed.Status != "failed" || !failed.Active {
		t.Fatalf("failure falsely released gate: %+v", failed)
	}
	if _, e := leader.Recover(ctx, task.TaskID, "rollback", failed.Revision-1); e == nil {
		t.Fatal("accepted stale recovery")
	}
	if _, e := leader.Recover(ctx, task.TaskID, "rollback", failed.Revision); e != nil {
		t.Fatal(e)
	}
	c.Restarts[id].Fail = false
	for i := 0; i < 12; i++ {
		for _, nodeID := range c.IDs {
			m := c.Managers[nodeID]
			m.Tick(ctx)
			saved, _ := config.ReadConfigurationOverrides(config.ConfigurationOverridePath(m.Config.MetadataPath))
			if strings.HasSuffix(saved.TaskID, ".rollback") && m.StartedOverride.TaskID != saved.TaskID {
				c.reload(t, nodeID)
			}
		}
	}
	done, _ := c.Repository.ConfigurationTask(task.TaskID)
	if done.Status != "rolled_back" || done.Active {
		t.Fatalf("rollback incomplete %+v", done)
	}
	for i, id := range c.IDs {
		if c.Managers[id].Config.MySQL.DiscoveryIntervalSeconds != i+1 {
			t.Fatal("node-specific original lost")
		}
	}
}
func TestPreflightDriftQuorumAndUnsafeFieldFailBeforeWrites(t *testing.T) {
	c := newTestCluster(t)
	ctx := context.Background()
	m := c.Managers[c.Leader]
	r := Request{RequestID: string(model.NewResourceID()), NodeIDs: c.IDs, Changes: map[string]int{"mysql.discovery_interval_seconds": 9}}
	plan, e := m.Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	r.PlanHash = plan.Hash
	p := c.Managers[c.IDs[1]].ConfigPath
	b, _ := os.ReadFile(p)
	_ = os.WriteFile(p, append(b, ' '), 0600)
	if _, e = m.Dispatch(ctx, r, "admin"); e == nil {
		t.Fatal("accepted changed configuration")
	}
	_ = os.WriteFile(p, b, 0600)
	c.Quorum = false
	if _, e = m.Dispatch(ctx, r, "admin"); e == nil {
		t.Fatal("accepted minority")
	}
	c.Quorum = true
	r.Changes = map[string]int{"fencing.enabled": 0}
	if _, e = m.Plan(ctx, r); e == nil {
		t.Fatal("allowed fencing disable")
	}
	for _, id := range c.IDs {
		if _, e = os.Stat(config.ConfigurationOverridePath(c.Managers[id].Config.MetadataPath)); !os.IsNotExist(e) {
			t.Fatal("failed preflight wrote configuration")
		}
	}
}
func TestFreshLeaderPermitRequiredBeforeSavingOrRestart(t *testing.T) {
	c := newTestCluster(t)
	ctx := context.Background()
	task := c.dispatch(t)
	c.Managers[c.Leader].Tick(ctx)
	task, _ = c.Repository.ConfigurationTask(task.TaskID)
	c.Quorum = false
	m := c.Managers[task.CurrentNode]
	m.Tick(ctx)
	if c.Restarts[task.CurrentNode].Calls != 0 {
		t.Fatal("restarted without majority permit")
	}
	if _, e := os.Stat(config.ConfigurationOverridePath(m.Config.MetadataPath)); !os.IsNotExist(e) {
		t.Fatal("saved without majority permit")
	}
}
func TestHTTPPeerNeverFollowsRedirectOrLeaksToken(t *testing.T) {
	called := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer dest.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 307) }))
	defer src.Close()
	p := HTTPPeer{Client: http.DefaultClient, Token: "test-control"}
	if _, e := p.Node(context.Background(), consensus.ControllerMember{APIAddress: src.URL}); e == nil || called {
		t.Fatal("redirect followed")
	}
}

func TestConfigurationTaskSubjectImmutableAndPolicyChangesCannotRaceRollout(t *testing.T) {
	c := newTestCluster(t)
	task := c.dispatch(t)
	changed := store.CloneConfigurationTask(task)
	changed.Targets[0].Before["mysql.discovery_interval_seconds"] = 900
	if _, e := c.Repository.UpdateConfigurationTask(changed, task.Revision); e == nil {
		t.Fatal("changed rollback subject accepted")
	}
	changed = store.CloneConfigurationTask(task)
	changed.Actor = "other-admin"
	if _, e := c.Repository.UpdateConfigurationTask(changed, task.Revision); e == nil {
		t.Fatal("changed actor accepted")
	}
	changed = store.CloneConfigurationTask(task)
	changed.Status = "succeeded"
	changed.Active = false
	if _, e := c.Repository.UpdateConfigurationTask(changed, task.Revision); e == nil {
		t.Fatal("pending nodes declared complete")
	}
	if _, e := c.Repository.PutClusterPolicy(store.ClusterPolicy{Engines: map[string]store.ClusterEnginePolicy{"mysql": {AutomaticFailoverMinimumObservations: 4}}}, "admin"); e == nil {
		t.Fatal("dynamic policy modified during rollout")
	}
	if _, e := c.Repository.AcquireSoftwareUpdateGate("3.1.2.1", "another-operation"); e == nil {
		t.Fatal("software update overlapped configuration rollout")
	}
}
