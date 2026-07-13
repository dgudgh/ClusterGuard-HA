package runtime

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

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
