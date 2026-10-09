package configuration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

func TestSensitiveConfigurationRequiresAllVoters(t *testing.T) {
	c := newTestCluster(t)
	m := c.Managers[c.Leader]
	ctx := context.Background()
	for _, field := range config.EditableConfigurationFields() {
		r := Request{NodeIDs: c.IDs[:2], Changes: map[string]int{field.Path: field.Minimum}}
		_, err := m.Plan(ctx, r)
		if field.Scope == "cluster" && (err == nil || !strings.Contains(err.Error(), "all current voters")) {
			t.Fatalf("partial cluster field %s accepted: %v", field.Path, err)
		}
		if field.Scope == "node" && err != nil {
			t.Fatalf("local tuning %s rejected: %v", field.Path, err)
		}
	}
	if len(config.EditableConfigurationFields()) != 25 {
		t.Fatal("allowlist changed")
	}
}

func TestTimeoutBindsPlanAndTaskAcrossLeaderChanges(t *testing.T) {
	c := newTestCluster(t)
	m := c.Managers[c.Leader]
	ctx := context.Background()
	m.Config.ConfigurationDistribution.StepTimeoutSeconds = 900
	r := Request{RequestID: string(model.NewResourceID()), NodeIDs: c.IDs, Changes: map[string]int{"mysql.discovery_interval_seconds": 9}}
	plan, err := m.Plan(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if plan.StepTimeoutSeconds != 900 {
		t.Fatal("plan hides configured timeout")
	}
	r.PlanHash = plan.Hash
	m.Config.ConfigurationDistribution.StepTimeoutSeconds = 180
	if _, err = m.Dispatch(ctx, r, "admin"); err == nil {
		t.Fatal("changed timeout did not invalidate plan")
	}
	m.Config.ConfigurationDistribution.StepTimeoutSeconds = 900
	task, err := m.Dispatch(ctx, r, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if task.StepTimeoutSeconds != 900 {
		t.Fatal("task deadline not persisted")
	}
	changed := store.CloneConfigurationTask(task)
	changed.StepTimeoutSeconds = 180
	if _, err = c.Repository.UpdateConfigurationTask(changed, task.Revision); err == nil {
		t.Fatal("task timeout mutable")
	}
	m.Tick(ctx)
	task, _ = c.Repository.ConfigurationTask(task.TaskID)
	c.Leader = c.IDs[1]
	next := c.Managers[c.Leader]
	next.Config.ConfigurationDistribution.StepTimeoutSeconds = 30
	next.Now = func() time.Time { return task.StepStartedAt.Add(181 * time.Second) }
	next.Tick(ctx)
	observed, _ := c.Repository.ConfigurationTask(task.TaskID)
	if observed.Status != "running" {
		t.Fatal("new leader used local timeout instead of task timeout")
	}
	next.Now = func() time.Time { return task.StepStartedAt.Add(901 * time.Second) }
	next.Tick(ctx)
	observed, _ = c.Repository.ConfigurationTask(task.TaskID)
	if observed.Status != "failed" || !observed.Active {
		t.Fatal("timeout did not retain maintenance")
	}
}

func TestRejectedOverlayRestoresPrestateAndRetainsEvidence(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "runtime-configuration.json")
			previous := config.ConfigurationOverrides{}
			if existing {
				values := map[string]int{"agent.max_concurrent_sessions": 3}
				previous = config.ConfigurationOverrides{SchemaVersion: 1, TaskID: "previous", Values: values, Digest: config.ConfigurationDigest(values)}
				if err := config.WriteConfigurationOverrides(p, previous); err != nil {
					t.Fatal(err)
				}
			}
			values := map[string]int{"agent.max_concurrent_sessions": 4}
			next := config.ConfigurationOverrides{SchemaVersion: 1, TaskID: "new-task", Values: values, Digest: config.ConfigurationDigest(values)}
			err := publishValidatedOverrides(p, previous, next, func() error { return fmt.Errorf("load validation failed") })
			if err == nil || !strings.Contains(err.Error(), "previous overlay restored") {
				t.Fatalf("failure restoration: %v", err)
			}
			got, err := config.ReadConfigurationOverrides(p)
			if err != nil || !reflect.DeepEqual(got, previous) {
				t.Fatalf("prestate lost: %v", err)
			}
			evidence, err := config.ReadConfigurationOverrides(filepath.Join(dir, "configuration-"+next.TaskID+".rejected-"+next.Digest+".json"))
			if err != nil || !reflect.DeepEqual(evidence, next) {
				t.Fatal("rejected evidence lost")
			}
			if !existing {
				if _, err = os.Lstat(p); !os.IsNotExist(err) {
					t.Fatal("first overlay remains on disk")
				}
			}
		})
	}
}

func TestRejectedOverlayDoesNotOverwriteForeignChange(t *testing.T) {
	p := filepath.Join(t.TempDir(), "runtime-configuration.json")
	values := map[string]int{"agent.max_concurrent_sessions": 4}
	next := config.ConfigurationOverrides{SchemaVersion: 1, TaskID: "task", Values: values, Digest: config.ConfigurationDigest(values)}
	foreign := next
	foreign.TaskID = "foreign"
	err := publishValidatedOverrides(p, config.ConfigurationOverrides{}, next, func() error {
		if err := config.WriteConfigurationOverrides(p, foreign); err != nil {
			t.Fatal(err)
		}
		return fmt.Errorf("candidate changed")
	})
	if err == nil {
		t.Fatal("foreign write accepted")
	}
	got, _ := config.ReadConfigurationOverrides(p)
	if !reflect.DeepEqual(got, foreign) {
		t.Fatal("foreign evidence overwritten")
	}
}

func TestValidationFailureNeverRestartsAndRollbackReleasesOnlyVerifiedPrestate(t *testing.T) {
	c := newTestCluster(t)
	ctx := context.Background()
	task := c.dispatch(t)
	leader := c.Managers[c.Leader]
	leader.Tick(ctx)
	task, _ = c.Repository.ConfigurationTask(task.TaskID)
	node := c.Managers[task.CurrentNode]
	calls := 0
	node.Validate = func(config.File) error {
		calls++
		if calls >= 2 {
			return fmt.Errorf("post-save runtime validation failed")
		}
		return nil
	}
	node.Tick(ctx)
	leader.Tick(ctx)
	task, _ = c.Repository.ConfigurationTask(task.TaskID)
	if task.Status != "failed" || !task.Active || c.Restarts[task.CurrentNode].Calls != 0 {
		t.Fatal("validation failure restarted or released gate")
	}
	if _, err := config.Load(node.ConfigPath); err != nil {
		t.Fatalf("invalid overlay left in startup path: %v", err)
	}
	if _, err := leader.Recover(ctx, task.TaskID, "rollback", task.Revision); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		for _, id := range c.IDs {
			c.Managers[id].Tick(ctx)
		}
	}
	task, _ = c.Repository.ConfigurationTask(task.TaskID)
	if task.Status != "rolled_back" || task.Active {
		t.Fatalf("restored untouched prestate cannot recover: %+v", task)
	}
}

// A mixed interface deployment must complete software rollout before planning.
type oldInterfacePeer struct{ Peer }

func (p oldInterfacePeer) Node(ctx context.Context, member consensus.ControllerMember) (Node, error) {
	n, e := p.Peer.Node(ctx, member)
	n.SchemaVersion = 1
	return n, e
}
func TestConfigurationPlanRefusesOlderVoterInterface(t *testing.T) {
	c := newTestCluster(t)
	m := c.Managers[c.Leader]
	m.Peer = oldInterfacePeer{m.Peer}
	_, e := m.Plan(context.Background(), Request{NodeIDs: c.IDs, Changes: map[string]int{"mysql.discovery_interval_seconds": 9}})
	if e == nil || !strings.Contains(e.Error(), "not ready") {
		t.Fatal("legacy voter silently admitted")
	}
}
