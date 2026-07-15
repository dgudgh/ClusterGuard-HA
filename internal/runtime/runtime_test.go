package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type runtimeFailoverAuthority struct{ err error }

func (stub runtimeFailoverAuthority) RequireMutationAuthority(context.Context) error { return stub.err }

type runtimeFailoverInventory struct {
	resource model.HAEndpoint
	endpoint model.Endpoint
}

func (inventory runtimeFailoverInventory) HAEndpoints(model.ResourceID) []model.HAEndpoint {
	return []model.HAEndpoint{inventory.resource}
}

func (inventory runtimeFailoverInventory) Endpoint(resourceID model.ResourceID) (model.Endpoint, bool) {
	return inventory.endpoint, resourceID == inventory.endpoint.ResourceID
}

type runtimeFailoverLeases struct{}

func (runtimeFailoverLeases) Acquire(context.Context, endpoint.LeaseRequest) (endpoint.Lease, error) {
	return endpoint.Lease{}, nil
}
func (runtimeFailoverLeases) Validate(context.Context, endpoint.Lease) error { return nil }
func (runtimeFailoverLeases) FinalizeTransition(_ context.Context, lease endpoint.Lease, _ time.Duration) (endpoint.Lease, error) {
	return lease, nil
}
func (runtimeFailoverLeases) Release(context.Context, model.ResourceID) error { return nil }

type runtimeFailoverTransport struct{}

func (runtimeFailoverTransport) Send(_ context.Context, _ model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	switch request.Command {
	case agent.CommandVIPStatus:
		owns := false
		return agent.Response{Status: agent.StatusOK, OwnsVIP: &owns}, nil
	case agent.CommandRoleStatus:
		readOnly, superReadOnly := true, true
		return agent.Response{Status: agent.StatusOK, ReadOnly: &readOnly, SuperReadOnly: &superReadOnly}, nil
	default:
		return agent.Response{Status: agent.StatusOK}, nil
	}
}

func runtimeFreeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate runtime address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release runtime address: %v", err)
	}
	return address
}

func TestMySQLOperationCredentialsAreIndependent(t *testing.T) {
	credentials, err := mysqlOperationCredentials(config.MySQL{
		Enabled:     true,
		Discovery:   config.Credential{Username: "discover", Password: "discovery-secret"},
		Operation:   config.Credential{Username: "operator", Password: "operation-secret"},
		Replication: config.Credential{Username: "replicator", Password: "replication-secret"},
	})
	if err != nil {
		t.Fatalf("resolve credentials: %v", err)
	}
	if credentials.Administrative != (adapter.Credentials{Username: "operator", Password: "operation-secret"}) {
		t.Fatalf("unexpected administrative credentials: %+v", credentials.Administrative)
	}
	if credentials.Replication != (adapter.Credentials{Username: "replicator", Password: "replication-secret"}) {
		t.Fatalf("unexpected replication credentials: %+v", credentials.Replication)
	}
}

func TestMySQLDiscoveryCredentialsDoNotUseOperationSecret(t *testing.T) {
	credentials, err := mysqlDiscoveryCredentials(config.MySQL{
		Enabled:   true,
		Discovery: config.Credential{Username: "discover", Password: "discovery-secret"},
		Operation: config.Credential{Username: "operator", Password: "operation-secret"},
	})
	if err != nil {
		t.Fatalf("resolve credentials: %v", err)
	}
	if credentials != (adapter.Credentials{Username: "discover", Password: "discovery-secret"}) {
		t.Fatalf("unexpected discovery credentials: %+v", credentials)
	}
}

func TestMySQLFailoverRuntimeSharesThirtySecondFailureEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 13, 20, 0, 30, 0, time.UTC)
	clusterID, primaryID, targetID := model.NewResourceID(), model.NewResourceID(), model.NewResourceID()
	endpointID, haEndpointID := model.NewResourceID(), model.NewResourceID()
	components := newMySQLFailoverRuntime(
		runtimeFailoverAuthority{},
		runtimeFailoverInventory{
			resource: model.HAEndpoint{
				ResourceMeta: model.ResourceMeta{ResourceID: haEndpointID}, ClusterID: clusterID,
				EndpointID: endpointID, Kind: model.EndpointVIP, Interface: "ens160", Prefix: 24,
			},
			endpoint: model.Endpoint{
				ResourceMeta: model.ResourceMeta{ResourceID: endpointID}, ClusterID: clusterID,
				Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Active: true,
			},
		},
		runtimeFailoverLeases{}, runtimeFailoverTransport{}, "agent-secret", func() time.Time { return now },
	)
	if components.failureObserver == nil {
		t.Fatal("runtime did not expose a primary-failure observer to discovery")
	}
	start := now.Add(-30 * time.Second)
	components.failureObserver.Record(clusterID, true, start)
	for index := 1; index <= 6; index++ {
		components.failureObserver.Record(clusterID, true, start.Add(time.Duration(index)*5*time.Second))
	}
	resolved := adapter.ResolvedOperation{
		OperationID: model.NewResourceID(),
		Cluster:     model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: clusterID}, Engine: model.EngineMySQL},
		Primary:     model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: primaryID}, ClusterID: clusterID, Engine: model.EngineMySQL},
		Target:      model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: targetID}, ClusterID: clusterID, Engine: model.EngineMySQL},
	}
	checks := components.safety.Precheck(context.Background(), resolved)
	for _, check := range checks {
		if check.Name == "stable_primary_failure" {
			if check.Status != model.CheckPass {
				t.Fatalf("runtime failover safety did not consume discovery evidence: %+v", checks)
			}
			return
		}
	}
	t.Fatalf("runtime failover safety omitted stable-primary-failure check: %+v", checks)
}

func TestRuntimeLocksPersistClusterMutationThroughQuorumStore(t *testing.T) {
	repository := store.NewMemory()
	locks := newRuntimeLocks(repository, runtimeFailoverAuthority{})
	operation := model.Operation{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		ClusterID:    model.NewResourceID(),
	}
	release, err := locks.operations.Acquire(context.Background(), operation)
	if err != nil {
		t.Fatalf("acquire runtime operation lock: %v", err)
	}
	if records := repository.CoordinationOperationLocks(); len(records) != 1 || records[0].OperationID != operation.ResourceID {
		t.Fatalf("runtime operation lock was not persisted: %+v", records)
	}
	release()
	if records := repository.CoordinationOperationLocks(); len(records) != 0 {
		t.Fatalf("runtime operation lock was not released: %+v", records)
	}
}

func TestLifecycleLockDoesNotFreezeDiscoveryButStillConflictsWithMutations(t *testing.T) {
	repository := store.NewMemory()
	locks := newRuntimeLocks(repository, runtimeFailoverAuthority{})
	clusterID := model.NewResourceID()
	releaseLifecycle, err := locks.lifecycle.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("acquire lifecycle lock: %v", err)
	}
	defer releaseLifecycle()

	releasePublication, err := locks.publication.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("lifecycle work froze topology publication: %v", err)
	}
	releasePublication()

	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: clusterID}
	if release, err := locks.operations.Acquire(context.Background(), operation); err == nil {
		release()
		t.Fatal("lifecycle lock did not block a concurrent cluster mutation")
	}
}

func TestRuntimeSafetyGuardUsesControllerMajorityWhenConfigured(t *testing.T) {
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID()}
	guard := newRuntimeSafetyGuard(runtimeFailoverAuthority{err: errors.New("no quorum")})
	if err := guard.Evaluate(context.Background(), operation); err == nil {
		t.Fatal("runtime safety guard ignored controller quorum loss")
	}
	if err := newRuntimeSafetyGuard(nil).Evaluate(context.Background(), operation); err != nil {
		t.Fatalf("standalone safety guard should preserve local-mode behavior: %v", err)
	}
}

func runtimeRequest(t *testing.T, handler http.Handler, method string, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	contents, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(contents))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer control")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestRuntimeWiresDurableOperationsWithUnsupportedDefaultEndpointProvider(t *testing.T) {
	server, err := New(config.File{
		MetadataPath: filepath.Join(t.TempDir(), "metadata.json"),
		ControlToken: "control", ApprovalToken: "approval",
		MySQL: config.MySQL{
			Enabled:     true,
			Discovery:   config.Credential{Username: "discover", Password: "discovery-secret"},
			Operation:   config.Credential{Username: "operator", Password: "operation-secret"},
			Replication: config.Credential{Username: "replicator", Password: "replication-secret"},
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	clusterID := model.NewResourceID()
	targetID := model.NewResourceID()
	create := runtimeRequest(t, server.Handler(), http.MethodPost, "/api/v1/operations", map[string]interface{}{
		"operation": map[string]interface{}{"cluster_id": clusterID, "engine": "mysql", "kind": "switchover", "requested_by": "dba"},
		"target_id": targetID, "idempotency_key": "runtime-default-endpoint",
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var envelope struct {
		Result model.OperationRecord `json:"result"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	execute := runtimeRequest(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(envelope.Result.ResourceID)+"/execute", map[string]string{"approval_token": "approval"})
	if execute.Code != http.StatusNotImplemented {
		t.Fatalf("default execution status=%d body=%s", execute.Code, execute.Body.String())
	}
	read := runtimeRequest(t, server.Handler(), http.MethodGet, "/api/v1/operations/"+string(envelope.Result.ResourceID), nil)
	if read.Code != http.StatusOK || !bytes.Contains(read.Body.Bytes(), []byte(`"status":"unsupported"`)) {
		t.Fatalf("unsupported outcome was not durable: status=%d body=%s", read.Code, read.Body.String())
	}
}

func TestRuntimeRaftBlocksMutationWithoutControllerMajority(t *testing.T) {
	addresses := []string{runtimeFreeAddress(t), runtimeFreeAddress(t), runtimeFreeAddress(t)}
	ids := []model.ResourceID{model.NewResourceID(), model.NewResourceID(), model.NewResourceID()}
	peers := make([]config.ConsensusPeer, 3)
	for index := range peers {
		peers[index] = config.ConsensusPeer{ResourceID: ids[index], Address: addresses[index]}
	}
	server, err := New(config.File{
		MetadataPath: filepath.Join(t.TempDir(), "metadata.json"), ControlToken: "control",
		Consensus: config.Consensus{
			Enabled: true, LocalID: ids[0], BindAddress: addresses[0], AdvertiseAddress: addresses[0],
			DataDirectory: filepath.Join(t.TempDir(), "raft"), Bootstrap: true, ApplyTimeoutSeconds: 1, Peers: peers,
			SnapshotCASEnabled: true,
		},
	})
	if err != nil {
		t.Fatalf("start quorum-gated runtime: %v", err)
	}
	defer server.Close()
	response := runtimeRequest(t, server.Handler(), http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": "must-not-publish", "engine": "mysql", "endpoints": []map[string]interface{}{{"hostname": "mysql-a", "port": 3306}},
	})
	if response.Code != http.StatusServiceUnavailable || !bytes.Contains(response.Body.Bytes(), []byte("Raft leader")) {
		t.Fatalf("minority mutation status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRuntimeStartsAutomaticFailoverOnlyWithGuardedDependencies(t *testing.T) {
	addresses := []string{runtimeFreeAddress(t), runtimeFreeAddress(t), runtimeFreeAddress(t)}
	ids := []model.ResourceID{model.NewResourceID(), model.NewResourceID(), model.NewResourceID()}
	peers := make([]config.ConsensusPeer, 3)
	for index := range peers {
		peers[index] = config.ConsensusPeer{ResourceID: ids[index], Address: addresses[index]}
	}
	server, err := New(config.File{
		MetadataPath: filepath.Join(t.TempDir(), "metadata.json"), ApprovalToken: "approval",
		Consensus: config.Consensus{
			Enabled: true, LocalID: ids[0], BindAddress: addresses[0], AdvertiseAddress: addresses[0],
			DataDirectory: filepath.Join(t.TempDir(), "raft"), Bootstrap: true, ApplyTimeoutSeconds: 1, Peers: peers,
			SnapshotCASEnabled: true,
		},
		Agent: config.Agent{
			Enabled: true, User: "cg-agent", IdentityFile: "/tmp/agent-key", KnownHostsFile: "/tmp/known-hosts", SharedSecret: "agent-secret",
		},
		MySQL: config.MySQL{
			Enabled: true, DiscoveryIntervalSeconds: 5, DiscoveryTimeoutSeconds: 4,
			AutomaticFailoverEnabled: true, AutomaticFailoverIntervalSeconds: 5, AutomaticFailoverRetrySeconds: 30,
			Discovery:   config.Credential{Username: "discover", Password: "discovery-secret"},
			Operation:   config.Credential{Username: "operator", Password: "operation-secret"},
			Replication: config.Credential{Username: "replicator", Password: "replication-secret"},
		},
	})
	if err != nil {
		t.Fatalf("start automatic failover runtime: %v", err)
	}
	defer server.Close()
	if server.automaticRecovery == nil {
		t.Fatal("guarded automatic failover controller was not started")
	}
}

func TestRuntimeResolvesNodeLifecycleSecretsAndCapabilitiesServerSide(t *testing.T) {
	configuration := config.NodeLifecycle{
		SSHPassword: "ssh-secret", MySQLRootPassword: "root-secret", ReplicationPassword: "replication-secret",
		CloneAvailable: true, XtraBackupVersions: map[string]bool{"8.0": true}, LogicalDumpAllowed: true,
	}
	secrets := nodeLifecycleSecrets(configuration)
	capabilities := nodeLifecycleCapabilities(configuration)
	if secrets.SSHPassword != "ssh-secret" || secrets.MySQLRootPassword != "root-secret" || secrets.ReplicationPassword != "replication-secret" {
		t.Fatalf("runtime lifecycle secrets=%+v", secrets)
	}
	if !capabilities.CloneAvailable || !capabilities.XtraBackupVersions["8.0"] || !capabilities.LogicalDumpAllowed {
		t.Fatalf("runtime lifecycle capabilities=%+v", capabilities)
	}
}
