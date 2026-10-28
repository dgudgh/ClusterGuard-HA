package store

import (
	"clusterguard.io/ha/pkg/model"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRegisterDatabaseEndpointsAtomicIdempotent(t *testing.T) {
	r := NewMemory()
	a, _, err := r.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "local"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "local", Port: 3306, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := r.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "other"}, []model.Endpoint{{Kind: model.EndpointDatabase, IPAddress: "192.0.2.9", Port: 3306, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	original := r.StateRevision()
	_, err = r.RegisterDatabaseEndpoints(a.ResourceID, []model.Endpoint{{Hostname: "cloud", Port: 3306}, {IPAddress: "192.0.2.9", Port: 3306}}, "admin")
	if !errors.Is(err, ErrConflict) || len(r.Endpoints(a.ResourceID)) != 1 || r.StateRevision() != original {
		t.Fatalf("partial batch: %v", err)
	}
	input := []model.Endpoint{{Hostname: "cloud", IPAddress: "192.0.2.40", Port: 3306}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := r.RegisterDatabaseEndpoints(a.ResourceID, input, "admin"); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if len(r.Endpoints(a.ResourceID)) != 2 || len(r.Endpoints(b.ResourceID)) != 1 || len(r.Audits()) != 1 {
		t.Fatal("retry duplicated endpoint or audit")
	}
	inv, _ := r.DiscoveryInventory(a.ResourceID)
	if inv.Generation != 2 {
		t.Fatalf("generation=%d", inv.Generation)
	}
	got := r.Endpoints(a.ResourceID)[0]
	again, e := r.RegisterDatabaseEndpoints(a.ResourceID, input, "admin")
	if e != nil || again[0].ResourceID != got.ResourceID {
		t.Fatalf("unstable id: %v", e)
	}
	if len(r.Nodes()) != 0 {
		t.Fatal("registration changed node membership")
	}
}

func TestRegisterDatabaseEndpointsValidation(t *testing.T) {
	r := NewMemory()
	c, _, _ := r.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EnginePostgreSQL, DisplayName: "pg"}, nil)
	for _, input := range [][]model.Endpoint{nil, {{Hostname: "db", Port: 0}}, {{Hostname: "db", Port: 3306}, {Hostname: "db", Port: 3306}}, {{Port: 3306}}} {
		if _, e := r.RegisterDatabaseEndpoints(c.ResourceID, input, "admin"); !errors.Is(e, ErrValidation) {
			t.Fatalf("accepted invalid input %+v: %v", input, e)
		}
	}
	if len(r.Endpoints(c.ResourceID)) != 0 {
		t.Fatal("invalid input changed inventory")
	}
}

func TestRegisterDatabaseEndpointsPersistsAndRejectsFailedCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := r.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "persistent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := []model.Endpoint{{Hostname: "cloud", Port: 3306}}
	r.syncFile = func(*os.File) error { return errors.New("injected storage failure") }
	if _, err := r.RegisterDatabaseEndpoints(c.ResourceID, input, "admin"); err == nil {
		t.Fatal("failed commit accepted")
	}
	if len(r.Endpoints(c.ResourceID)) != 0 || len(r.Audits()) != 0 {
		t.Fatal("failed commit changed live state")
	}
	r.syncFile = func(f *os.File) error { return f.Sync() }
	got, err := r.RegisterDatabaseEndpoints(c.ResourceID, input, "admin")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if entries := reopened.Endpoints(c.ResourceID); len(entries) != 1 || entries[0].ResourceID != got[0].ResourceID || len(reopened.Audits()) != 1 {
		t.Fatal("registration/audit not durable")
	}
}
