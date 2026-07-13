package store

import (
	"errors"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func seedHAEndpointCluster(t *testing.T, repository *Repository, name string, port int) (model.DatabaseCluster, model.DatabaseInstance) {
	t.Helper()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: name}, []model.Endpoint{
		{Kind: model.EndpointDatabase, Hostname: name + "-db", IPAddress: "192.0.2.10", Port: port, Active: true},
	})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	result, err := repository.ReconcileInstance(model.DatabaseInstance{
		ClusterID: cluster.ResourceID, Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": name + "-uuid"},
		Hostname:       name + "-db", IPAddress: "192.0.2.10", Port: port,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	return cluster, result.Instance
}

func TestPutHAEndpointAllowsOneActiveVIPPerCluster(t *testing.T) {
	repository := NewMemory()
	cluster, owner := seedHAEndpointCluster(t, repository, "payments", 3306)
	resource, endpoint, err := repository.PutHAEndpoint(HAEndpointSpec{
		ClusterID: cluster.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100",
		Interface: "ens160", Prefix: 24, OwnerID: owner.ResourceID, Active: true,
	})
	if err != nil {
		t.Fatalf("put HA endpoint: %v", err)
	}
	if resource.ClusterID != cluster.ResourceID || resource.OwnerID != owner.ResourceID || resource.Interface != "ens160" || resource.Prefix != 24 {
		t.Fatalf("unexpected HA endpoint: %+v", resource)
	}
	if endpoint.ResourceID != resource.EndpointID || endpoint.IPAddress != "192.0.2.100" || endpoint.Kind != model.EndpointVIP {
		t.Fatalf("unexpected endpoint: %+v", endpoint)
	}

	updated, updatedEndpoint, err := repository.PutHAEndpoint(HAEndpointSpec{
		ClusterID: cluster.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.101",
		Interface: "ens192", Prefix: 25, OwnerID: owner.ResourceID, Active: true,
	})
	if err != nil {
		t.Fatalf("update HA endpoint: %v", err)
	}
	if updated.ResourceID != resource.ResourceID || updated.EndpointID != endpoint.ResourceID || updated.MetadataRevision != resource.MetadataRevision+1 {
		t.Fatalf("VIP update created another resource: old=%+v new=%+v", resource, updated)
	}
	if updatedEndpoint.IPAddress != "192.0.2.101" || len(repository.HAEndpoints(cluster.ResourceID)) != 1 {
		t.Fatalf("VIP update was not canonical: endpoint=%+v all=%+v", updatedEndpoint, repository.HAEndpoints(cluster.ResourceID))
	}
}

func TestPutHAEndpointRejectsVIPSharedByTwoClusters(t *testing.T) {
	repository := NewMemory()
	clusterA, ownerA := seedHAEndpointCluster(t, repository, "payments", 3306)
	clusterB, ownerB := seedHAEndpointCluster(t, repository, "orders", 3307)
	if _, _, err := repository.PutHAEndpoint(HAEndpointSpec{ClusterID: clusterA.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Interface: "ens160", Prefix: 24, OwnerID: ownerA.ResourceID, Active: true}); err != nil {
		t.Fatalf("put first VIP: %v", err)
	}
	if _, _, err := repository.PutHAEndpoint(HAEndpointSpec{ClusterID: clusterB.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Interface: "ens160", Prefix: 24, OwnerID: ownerB.ResourceID, Active: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("shared active VIP error=%v", err)
	}
}

func TestPutHAEndpointRejectsUnknownOrForeignOwner(t *testing.T) {
	repository := NewMemory()
	clusterA, _ := seedHAEndpointCluster(t, repository, "payments", 3306)
	_, ownerB := seedHAEndpointCluster(t, repository, "orders", 3307)
	for _, ownerID := range []model.ResourceID{model.NewResourceID(), ownerB.ResourceID} {
		if _, _, err := repository.PutHAEndpoint(HAEndpointSpec{ClusterID: clusterA.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Interface: "ens160", Prefix: 24, OwnerID: ownerID, Active: true}); !errors.Is(err, ErrValidation) {
			t.Fatalf("owner %s error=%v", ownerID, err)
		}
	}
}

func TestHAEndpointSurvivesRepositoryRestart(t *testing.T) {
	path := t.TempDir() + "/metadata.json"
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cluster, owner := seedHAEndpointCluster(t, repository, "payments", 3306)
	created, _, err := repository.PutHAEndpoint(HAEndpointSpec{ClusterID: cluster.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Interface: "ens160", Prefix: 24, OwnerID: owner.ResourceID, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, found := reopened.HAEndpoint(created.ResourceID)
	if !found || loaded.EndpointID != created.EndpointID || loaded.OwnerID != owner.ResourceID {
		t.Fatalf("loaded HA endpoint=%+v found=%t", loaded, found)
	}
}
