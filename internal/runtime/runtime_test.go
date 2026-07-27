package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/internal/agent"
	platformauth "clusterguard.io/ha/internal/auth"
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type runtimeExternalFencer struct{}

type runtimeRoundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip runtimeRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func (runtimeExternalFencer) Fence(context.Context, coordination.ExternalFenceRequest) error {
	return nil
}
func (runtimeExternalFencer) Status(context.Context, coordination.ExternalFenceRequest) (bool, error) {
	return true, nil
}

type failingBootstrapHasher struct{ err error }

func (hasher failingBootstrapHasher) Hash(string) (string, error) { return "", hasher.err }
func (failingBootstrapHasher) Verify(string, string) bool         { return false }

func TestAuthenticationBootstrapReportsLeaderCommitFailures(t *testing.T) {
	repository := store.NewMemory()
	want := errors.New("password hasher unavailable")
	service := platformauth.New(repository, failingBootstrapHasher{err: want}, nil, time.Now, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reported := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		runAuthenticationBootstrap(ctx, repository, service, nil, func(err error) {
			select {
			case reported <- err:
			default:
			}
			cancel()
		})
		close(done)
	}()
	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), want.Error()) {
			t.Fatalf("reported bootstrap error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bootstrap failure was silently swallowed")
	}
	<-done
}

func TestRuntimeBootstrapsDefaultAdministratorOnce(t *testing.T) {
	metadataPath := filepath.Join(t.TempDir(), "metadata.json")
	configuration := config.File{MetadataPath: metadataPath}

	first, err := New(configuration)
	if err != nil {
		t.Fatalf("start first runtime: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first runtime: %v", err)
	}
	repository, err := store.Open(metadataPath)
	if err != nil {
		t.Fatalf("open bootstrapped metadata: %v", err)
	}
	users := repository.PlatformUsers()
	if len(users) != 1 || users[0].Username != platformauth.DefaultAdminUsername ||
		users[0].Role != model.PlatformRoleAdmin || !users[0].MustChangePassword {
		t.Fatalf("unexpected bootstrap users: %+v", users)
	}
	if users[0].PasswordHash == "" || bytes.Contains([]byte(users[0].PasswordHash), []byte(platformauth.DefaultAdminPassword)) {
		t.Fatal("runtime persisted the bootstrap password without hashing")
	}
	firstUserID := users[0].ResourceID

	second, err := New(configuration)
	if err != nil {
		t.Fatalf("restart runtime: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close restarted runtime: %v", err)
	}
	reopened, err := store.Open(metadataPath)
	if err != nil {
		t.Fatalf("reopen metadata: %v", err)
	}
	if users := reopened.PlatformUsers(); len(users) != 1 || users[0].ResourceID != firstUserID {
		t.Fatalf("runtime restart duplicated bootstrap administrator: %+v", users)
	}
}

func TestRuntimeMarksAuthenticationCookiesSecureWhenTLSIsConfigured(t *testing.T) {
	server, err := New(config.File{
		MetadataPath: filepath.Join(t.TempDir(), "metadata.json"),
		TLSCertFile:  "/etc/clusterguard/tls/server.crt",
		TLSKeyFile:   "/etc/clusterguard/tls/server.key",
	})
	if err != nil {
		t.Fatalf("start TLS-configured runtime: %v", err)
	}
	defer server.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"admin123"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	for _, cookie := range response.Result().Cookies() {
		if (cookie.Name == "clusterguard_session" || cookie.Name == "clusterguard_csrf") && !cookie.Secure {
			t.Fatalf("TLS runtime issued insecure cookie: %+v", cookie)
		}
	}
}

func TestMutationRPCClientTrustsConfiguredControlPlaneCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("test TLS server has no certificate")
	}
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	contents := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if err := os.WriteFile(caPath, contents, 0600); err != nil {
		t.Fatalf("write test CA: %v", err)
	}
	client, err := newMutationRPCClient(config.File{TLSCAFile: caPath})
	if err != nil {
		t.Fatalf("create mutation RPC client: %v", err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("call TLS leader with configured CA: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("leader response status = %d", response.StatusCode)
	}
}

func TestMutationRPCClientRejectsInvalidControlPlaneCA(t *testing.T) {
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caPath, []byte("not a certificate"), 0600); err != nil {
		t.Fatalf("write invalid test CA: %v", err)
	}
	if _, err := newMutationRPCClient(config.File{TLSCAFile: caPath}); err == nil {
		t.Fatal("invalid control-plane CA was accepted")
	}
}

func TestMutationRPCClientAcceptsCustomDefaultTransport(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = runtimeRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("test transport")
	})
	t.Cleanup(func() { http.DefaultTransport = original })

	client, err := newMutationRPCClient(config.File{})
	if err != nil {
		t.Fatalf("create mutation RPC client with custom default transport: %v", err)
	}
	if client == nil || client.Transport == nil {
		t.Fatal("mutation RPC client has no isolated transport")
	}
}

func TestConsensusPeerAPIAddressUsesTrustedControllerConfiguration(t *testing.T) {
	peer := config.ConsensusPeer{
		ResourceID: model.NewResourceID(),
		Address:    "192.0.2.25:10009",
	}
	address, err := consensusPeerAPIAddress(config.File{
		HTTPAddress: "0.0.0.0:3000",
		TLSCertFile: "/etc/clusterguard/tls/server.crt",
		Consensus:   config.Consensus{Peers: []config.ConsensusPeer{peer}},
	}, peer)
	if err != nil || address != "https://192.0.2.25:3000" {
		t.Fatalf("derived controller API address=%q err=%v", address, err)
	}
	peer.APIAddress = "https://controller-a.example:8443"
	address, err = consensusPeerAPIAddress(config.File{HTTPAddress: "0.0.0.0:3000"}, peer)
	if err != nil || address != peer.APIAddress {
		t.Fatalf("explicit controller API address=%q err=%v", address, err)
	}
}

type runtimeRecoveryAuthority struct{ calls int }

func (authority *runtimeRecoveryAuthority) RequireMutationAuthority(context.Context) error {
	authority.calls++
	if authority.calls < 3 {
		return errors.New("not leader")
	}
	return nil
}

type runtimeRecoveryConsensus struct {
	repository  *store.Repository
	synchronize func() error
	syncCalls   int
}

func (consensus *runtimeRecoveryConsensus) Commit(state []byte) error {
	return consensus.repository.ApplyReplicatedState(state)
}

func (consensus *runtimeRecoveryConsensus) SynchronizeForCommit() error {
	consensus.syncCalls++
	if consensus.synchronize == nil {
		return nil
	}
	return consensus.synchronize()
}

func TestAuthenticationRecoveryWaitsForLeaderConsumesArtifactAndRestoresLogin(t *testing.T) {
	now := time.Date(2026, time.July, 17, 3, 0, 0, 0, time.UTC)
	repository := store.NewMemory()
	service := platformauth.New(
		repository,
		platformauth.Argon2Hasher{
			Params: platformauth.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x33}, 256)),
		},
		bytes.NewReader(bytes.Repeat([]byte{0x44}, 4096)),
		func() time.Time { return now }, 8*time.Hour,
	)
	if _, err := service.EnsureBootstrapAdmin(context.Background()); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	password := "Recovery-temporary-password-123"
	artifact, err := platformauth.NewAdminRecoveryArtifact(
		password,
		platformauth.Argon2Hasher{
			Params: platformauth.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x55}, 256)),
		},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("new recovery artifact: %v", err)
	}
	path := filepath.Join(t.TempDir(), "admin-recovery.json")
	if err := platformauth.WriteAdminRecoveryArtifact(path, artifact); err != nil {
		t.Fatalf("write recovery artifact: %v", err)
	}
	authority := &runtimeRecoveryAuthority{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runAuthenticationRecovery(ctx, repository, service, authority, path, func() time.Time { return now }, time.Millisecond); err != nil {
		t.Fatalf("run authentication recovery: %v", err)
	}
	if authority.calls < 3 {
		t.Fatalf("recovery bypassed leader gate: calls=%d", authority.calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("consumed recovery artifact remains: %v", err)
	}
	login, err := service.Login(context.Background(), platformauth.DefaultAdminUsername, password)
	if err != nil || !login.Principal.MustChangePassword {
		t.Fatalf("recovery login=%+v err=%v", login, err)
	}
}

func TestAuthenticationRecoveryRetriesRepositoryConflictUntilLeaderCommitSucceeds(t *testing.T) {
	now := time.Date(2026, time.July, 17, 3, 0, 0, 0, time.UTC)
	repository := store.NewMemory()
	service := platformauth.New(
		repository,
		platformauth.Argon2Hasher{
			Params: platformauth.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x63}, 256)),
		},
		bytes.NewReader(bytes.Repeat([]byte{0x64}, 4096)),
		func() time.Time { return now }, 8*time.Hour,
	)
	if _, err := service.EnsureBootstrapAdmin(context.Background()); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	initialState, err := repository.ReplicatedState()
	if err != nil {
		t.Fatalf("read initial state: %v", err)
	}
	concurrent := store.NewMemory()
	if err := concurrent.RestoreReplicatedState(initialState); err != nil {
		t.Fatalf("restore concurrent state: %v", err)
	}
	if _, err := concurrent.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "concurrent-discovery"}); err != nil {
		t.Fatalf("create concurrent metadata: %v", err)
	}
	concurrentState, err := concurrent.ReplicatedState()
	if err != nil {
		t.Fatalf("read concurrent state: %v", err)
	}
	consensus := &runtimeRecoveryConsensus{repository: repository}
	consensus.synchronize = func() error {
		if consensus.syncCalls == 1 {
			return repository.ApplyReplicatedState(concurrentState)
		}
		return nil
	}
	if err := repository.SetSnapshotConsensus(consensus); err != nil {
		t.Fatalf("configure consensus: %v", err)
	}

	password := "Recovery-after-conflict-password-123"
	artifact, err := platformauth.NewAdminRecoveryArtifact(
		password,
		platformauth.Argon2Hasher{
			Params: platformauth.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x65}, 256)),
		},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("new recovery artifact: %v", err)
	}
	path := filepath.Join(t.TempDir(), "admin-recovery.json")
	if err := platformauth.WriteAdminRecoveryArtifact(path, artifact); err != nil {
		t.Fatalf("write recovery artifact: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runAuthenticationRecovery(ctx, repository, service, nil, path, func() time.Time { return now }, time.Millisecond); err != nil {
		t.Fatalf("recover after transient repository conflict: %v", err)
	}
	if consensus.syncCalls < 2 {
		t.Fatalf("recovery commit attempts=%d, want at least 2", consensus.syncCalls)
	}
	if _, err := service.Login(context.Background(), platformauth.DefaultAdminUsername, password); err != nil {
		t.Fatalf("login with recovered password: %v", err)
	}
}

func TestAuthenticationRecoveryRejectsStaleArtifactWithoutRetryingForever(t *testing.T) {
	now := time.Date(2026, time.July, 17, 3, 0, 0, 0, time.UTC)
	repository := store.NewMemory()
	service := platformauth.New(
		repository,
		platformauth.Argon2Hasher{
			Params: platformauth.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x73}, 512)),
		},
		bytes.NewReader(bytes.Repeat([]byte{0x74}, 4096)),
		func() time.Time { return now }, 8*time.Hour,
	)
	if _, err := service.EnsureBootstrapAdmin(context.Background()); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	artifact, err := platformauth.NewAdminRecoveryArtifact(
		"Stale-recovery-password-123",
		platformauth.Argon2Hasher{
			Params: platformauth.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x75}, 256)),
		},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("new recovery artifact: %v", err)
	}
	login, err := service.Login(context.Background(), platformauth.DefaultAdminUsername, platformauth.DefaultAdminPassword)
	if err != nil {
		t.Fatalf("login administrator: %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := service.ChangePassword(
		context.Background(), login.SessionToken, platformauth.DefaultAdminPassword, "Newer-administrator-password-123",
	); err != nil {
		t.Fatalf("change administrator password: %v", err)
	}
	path := filepath.Join(t.TempDir(), "admin-recovery.json")
	if err := platformauth.WriteAdminRecoveryArtifact(path, artifact); err != nil {
		t.Fatalf("write stale recovery artifact: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = runAuthenticationRecovery(ctx, repository, service, nil, path, func() time.Time { return now }, time.Millisecond)
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale recovery artifact error=%v, want immediate repository conflict", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("rejected recovery artifact should remain for operator inspection: %v", err)
	}
}

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
func (runtimeFailoverLeases) RollbackTransition(_ context.Context, lease endpoint.Lease, _ time.Duration) (endpoint.Lease, error) {
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

func TestDatabaseDiscoveryCredentialsAreRoutedByEngine(t *testing.T) {
	configuration := config.File{
		MySQL: config.MySQL{
			Enabled:   true,
			Discovery: config.Credential{Username: "mysql-monitor", Password: "mysql-secret"},
		},
		PostgreSQL: config.PostgreSQL{
			Enabled:   true,
			Discovery: config.Credential{Username: "pg-monitor", Password: "pg-secret", Database: "postgres"},
		},
		Oracle: config.Oracle{
			Enabled:   true,
			Discovery: config.Credential{Username: "ora-monitor", Password: "ora-secret", Database: "CGPROD"},
		},
		SQLServer: config.SQLServer{
			Enabled:   true,
			Discovery: config.Credential{Username: "sql-monitor", Password: "sql-secret", Database: "master"},
		},
	}
	mysqlCredentials, err := databaseDiscoveryCredentials(configuration, model.DatabaseCluster{Engine: model.EngineMySQL})
	if err != nil || mysqlCredentials != (adapter.Credentials{Username: "mysql-monitor", Password: "mysql-secret"}) {
		t.Fatalf("MySQL discovery credentials=%+v err=%v", mysqlCredentials, err)
	}
	postgresqlCredentials, err := databaseDiscoveryCredentials(configuration, model.DatabaseCluster{Engine: model.EnginePostgreSQL})
	if err != nil || postgresqlCredentials != (adapter.Credentials{Username: "pg-monitor", Password: "pg-secret", Database: "postgres"}) {
		t.Fatalf("PostgreSQL discovery credentials=%+v err=%v", postgresqlCredentials, err)
	}
	oracleCredentials, err := databaseDiscoveryCredentials(configuration, model.DatabaseCluster{Engine: model.EngineOracle})
	if err != nil || oracleCredentials != (adapter.Credentials{Username: "ora-monitor", Password: "ora-secret", Database: "CGPROD"}) {
		t.Fatalf("Oracle discovery credentials=%+v err=%v", oracleCredentials, err)
	}
	sqlServerCredentials, err := databaseDiscoveryCredentials(configuration, model.DatabaseCluster{Engine: model.EngineSQLServer})
	if err != nil || sqlServerCredentials != (adapter.Credentials{Username: "sql-monitor", Password: "sql-secret", Database: "master"}) {
		t.Fatalf("SQL Server discovery credentials=%+v err=%v", sqlServerCredentials, err)
	}
}

func TestDatabaseOperationCredentialsAreRoutedForOracleAndSQLServer(t *testing.T) {
	configuration := config.File{
		Oracle: config.Oracle{
			Enabled:   true,
			Discovery: config.Credential{Database: "CGPROD"},
			Operation: config.Credential{Username: "sys", Password: "ora-operation-secret", Database: "CGPROD"},
		},
		SQLServer: config.SQLServer{
			Enabled:   true,
			Discovery: config.Credential{Database: "master"},
			Operation: config.Credential{Username: "ag-operator", Password: "sql-operation-secret"},
		},
	}
	oracleCredentials, err := databaseOperationCredentials(configuration, model.DatabaseCluster{Engine: model.EngineOracle})
	if err != nil || oracleCredentials.Administrative != (adapter.Credentials{Username: "sys", Password: "ora-operation-secret", Database: "CGPROD"}) {
		t.Fatalf("Oracle operation credentials=%+v err=%v", oracleCredentials, err)
	}
	sqlServerCredentials, err := databaseOperationCredentials(configuration, model.DatabaseCluster{Engine: model.EngineSQLServer})
	if err != nil || sqlServerCredentials.Administrative != (adapter.Credentials{Username: "ag-operator", Password: "sql-operation-secret", Database: "master"}) {
		t.Fatalf("SQL Server operation credentials=%+v err=%v", sqlServerCredentials, err)
	}
	configuration.Oracle.Operation = config.Credential{}
	if _, err := databaseOperationCredentials(configuration, model.DatabaseCluster{Engine: model.EngineOracle}); err == nil {
		t.Fatal("incomplete Oracle operation credentials were accepted")
	}
}

func TestDatabaseOperationCredentialsAreRoutedForPostgreSQL(t *testing.T) {
	configuration := config.File{PostgreSQL: config.PostgreSQL{
		Enabled:     true,
		Operation:   config.Credential{Username: "pg-operator", Password: "operation-secret", Database: "postgres"},
		Replication: config.Credential{Username: "pg-replication", Password: "replication-secret", Database: "postgres"},
	}}
	credentials, err := databaseOperationCredentials(configuration, model.DatabaseCluster{Engine: model.EnginePostgreSQL})
	if err != nil || credentials.Administrative != (adapter.Credentials{Username: "pg-operator", Password: "operation-secret", Database: "postgres"}) || credentials.Replication != (adapter.Credentials{Username: "pg-replication", Password: "replication-secret", Database: "postgres"}) {
		t.Fatalf("PostgreSQL mutation credentials=%+v err=%v", credentials, err)
	}
	configuration.PostgreSQL.Replication = config.Credential{}
	if _, err := databaseOperationCredentials(configuration, model.DatabaseCluster{Engine: model.EnginePostgreSQL}); err == nil {
		t.Fatal("incomplete PostgreSQL mutation credentials were accepted")
	}
}

type runtimePostgreSQLEndpointProvider struct{}

func (runtimePostgreSQLEndpointProvider) Executable(context.Context) bool { return true }
func (runtimePostgreSQLEndpointProvider) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{Name: "writer_endpoint", Status: model.CheckPass}}
}
func (runtimePostgreSQLEndpointProvider) AuthorizeTransition(ctx context.Context, _ adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	guarded, cancel := context.WithCancel(ctx)
	return adapter.TransitionAuthorization{Context: guarded, Cancel: cancel, Abort: func(context.Context) error { return nil }, Finalize: func(context.Context) error { return nil }, LeaseID: model.NewResourceID()}, nil
}
func (runtimePostgreSQLEndpointProvider) Transfer(context.Context, adapter.ResolvedOperation) error {
	return nil
}
func (runtimePostgreSQLEndpointProvider) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass}
}

type runtimePostgreSQLAgentTransport struct{}

func (runtimePostgreSQLAgentTransport) Send(context.Context, model.DatabaseInstance, agent.Request) (agent.Response, error) {
	return agent.Response{Status: agent.StatusOK}, nil
}

func TestRuntimePostgreSQLAdapterAdvertisesExecutionOnlyWhenFullyConfigured(t *testing.T) {
	configuration := config.File{
		PostgreSQL: config.PostgreSQL{
			Enabled:     true,
			Operation:   config.Credential{Username: "pg-operator", Password: "operation-secret", Database: "postgres"},
			Replication: config.Credential{Username: "pg-replication", Password: "replication-secret", Database: "postgres"},
		},
		Agent: config.Agent{SharedSecret: "agent-secret"},
	}
	candidate := newPostgreSQLRuntimeAdapter(configuration, runtimePostgreSQLEndpointProvider{}, runtimePostgreSQLAgentTransport{}, postgresql.UnsupportedFailoverSafetyProvider{})
	if !candidate.Capabilities(context.Background()).Supports(adapter.CapabilityExecute) {
		t.Fatal("fully configured PostgreSQL runtime did not advertise execution")
	}
	configuration.PostgreSQL.Replication = config.Credential{}
	candidate = newPostgreSQLRuntimeAdapter(configuration, runtimePostgreSQLEndpointProvider{}, runtimePostgreSQLAgentTransport{}, postgresql.UnsupportedFailoverSafetyProvider{})
	if candidate.Capabilities(context.Background()).Supports(adapter.CapabilityExecute) {
		t.Fatal("PostgreSQL runtime advertised execution without replication credentials")
	}
	configuration.PostgreSQL.Replication = config.Credential{Username: "pg-replication", Password: "replication-secret"}
	candidate = newPostgreSQLRuntimeAdapter(configuration, runtimePostgreSQLEndpointProvider{}, nil, postgresql.UnsupportedFailoverSafetyProvider{})
	if candidate.Capabilities(context.Background()).Supports(adapter.CapabilityExecute) {
		t.Fatal("PostgreSQL runtime advertised execution without restricted agent transport")
	}
}

func TestRuntimeOracleAdapterAdvertisesExecutionOnlyWithRestrictedAgentAndCredentials(t *testing.T) {
	configuration := config.File{
		Oracle: config.Oracle{
			Enabled: true,
			Operation: config.Credential{
				Username: "sys", Password: "operation-secret", Database: "mesdb",
			},
		},
		Agent: config.Agent{SharedSecret: "agent-secret"},
	}
	candidate := newOracleRuntimeAdapter(configuration, runtimePostgreSQLAgentTransport{})
	if !candidate.Capabilities(context.Background()).Supports(adapter.CapabilityExecute) {
		t.Fatal("fully configured Oracle runtime did not advertise controlled switchover")
	}
	configuration.Oracle.Operation = config.Credential{}
	candidate = newOracleRuntimeAdapter(configuration, runtimePostgreSQLAgentTransport{})
	if candidate.Capabilities(context.Background()).Supports(adapter.CapabilityExecute) {
		t.Fatal("Oracle runtime advertised execution without operation credentials")
	}
	configuration.Oracle.Operation = config.Credential{Username: "sys", Password: "operation-secret"}
	candidate = newOracleRuntimeAdapter(configuration, nil)
	if candidate.Capabilities(context.Background()).Supports(adapter.CapabilityExecute) {
		t.Fatal("Oracle runtime advertised execution without restricted agent transport")
	}
}

func TestEngineClusterSourceFiltersIndependentSchedulers(t *testing.T) {
	clusters := runtimeClusterSource{clusters: []model.DatabaseCluster{
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, DisplayName: "mysql-a"},
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EnginePostgreSQL, DisplayName: "pg-a"},
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, DisplayName: "mysql-b"},
	}}
	postgresqlClusters := (engineClusterSource{source: clusters, engine: model.EnginePostgreSQL}).Clusters()
	if len(postgresqlClusters) != 1 || postgresqlClusters[0].DisplayName != "pg-a" {
		t.Fatalf("PostgreSQL scheduler inventory=%+v", postgresqlClusters)
	}
	mysqlClusters := (engineClusterSource{source: clusters, engine: model.EngineMySQL}).Clusters()
	if len(mysqlClusters) != 2 {
		t.Fatalf("MySQL scheduler inventory=%+v", mysqlClusters)
	}
}

type runtimeClusterSource struct {
	clusters []model.DatabaseCluster
}

func (source runtimeClusterSource) Clusters() []model.DatabaseCluster {
	return append([]model.DatabaseCluster{}, source.clusters...)
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

func TestMySQLFailoverRuntimeAcceptsExternalFencingWithoutOldPrimaryAgentReachability(t *testing.T) {
	components := newMySQLFailoverRuntime(
		runtimeFailoverAuthority{}, runtimeFailoverInventory{}, runtimeFailoverLeases{}, nil, "", nil,
		runtimeExternalFencer{},
	)
	if _, ok := components.safety.(*coordination.GuardedFailoverSafety); !ok {
		t.Fatalf("external fencing was not wired into guarded failover safety: %T", components.safety)
	}
}

func TestRuntimeLocksPersistClusterMutationThroughQuorumStore(t *testing.T) {
	repository := store.NewMemory()
	locks := newRuntimeLocks(repository, runtimeFailoverAuthority{})
	operation := model.Operation{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		ClusterID:    model.NewResourceID(),
	}
	_, release, err := locks.operations.Acquire(context.Background(), operation)
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
	_, releaseLifecycle, err := locks.lifecycle.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("acquire lifecycle lock: %v", err)
	}
	defer releaseLifecycle()

	_, releasePublication, err := locks.publication.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("lifecycle work froze topology publication: %v", err)
	}
	releasePublication()

	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: clusterID}
	if _, release, err := locks.operations.Acquire(context.Background(), operation); err == nil {
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

func TestRuntimeApprovalGatesSeparateOneTimeDatabaseAndAdministrativeMetadataAuthorization(t *testing.T) {
	gates := runtimeApprovalGates{administrativeToken: "administrator-secret"}
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID()}
	for _, token := range []string{"", "wrong-secret"} {
		if err := gates.Validate(context.Background(), operation, token); err == nil {
			t.Fatalf("administrative token %q was accepted", token)
		}
	}
	if err := gates.Validate(context.Background(), operation, "administrator-secret"); err != nil {
		t.Fatalf("valid administrative metadata credential was rejected: %v", err)
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

func TestRuntimeRejectsLegacyStaticApprovalForDatabaseExecution(t *testing.T) {
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
	if execute.Code != http.StatusUnauthorized || !bytes.Contains(execute.Body.Bytes(), []byte("approval grant is invalid")) {
		t.Fatalf("legacy approval execution status=%d body=%s", execute.Code, execute.Body.String())
	}
	read := runtimeRequest(t, server.Handler(), http.MethodGet, "/api/v1/operations/"+string(envelope.Result.ResourceID), nil)
	if read.Code != http.StatusOK || bytes.Contains(read.Body.Bytes(), []byte(`"status":"unsupported"`)) {
		t.Fatalf("rejected legacy approval changed durable state: status=%d body=%s", read.Code, read.Body.String())
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

func TestRuntimeStartsEngineIsolatedAutomaticFailoverWithGuardedDependencies(t *testing.T) {
	addresses := []string{runtimeFreeAddress(t), runtimeFreeAddress(t), runtimeFreeAddress(t)}
	ids := []model.ResourceID{model.NewResourceID(), model.NewResourceID(), model.NewResourceID()}
	peers := make([]config.ConsensusPeer, 3)
	for index := range peers {
		peers[index] = config.ConsensusPeer{ResourceID: ids[index], Address: addresses[index]}
	}
	server, err := New(config.File{
		MetadataPath: filepath.Join(t.TempDir(), "metadata.json"),
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
		PostgreSQL: config.PostgreSQL{
			Enabled: true, DiscoveryIntervalSeconds: 5, DiscoveryTimeoutSeconds: 4,
			AutomaticFailoverEnabled: true, AutomaticFailoverIntervalSeconds: 5, AutomaticFailoverRetrySeconds: 30,
			Discovery:   config.Credential{Username: "pg-discover", Password: "pg-discovery-secret", Database: "postgres"},
			Operation:   config.Credential{Username: "pg-operator", Password: "pg-operation-secret", Database: "postgres"},
			Replication: config.Credential{Username: "pg-replicator", Password: "pg-replication-secret", Database: "postgres"},
		},
	})
	if err != nil {
		t.Fatalf("start automatic failover runtime: %v", err)
	}
	defer server.Close()
	if server.automaticRecovery == nil || len(server.automaticRecoveries) != 2 {
		t.Fatalf("engine-isolated automatic failover controllers=%d", len(server.automaticRecoveries))
	}
}

func TestRuntimeResolvesNodeLifecycleSecretsAndCapabilitiesServerSide(t *testing.T) {
	configuration := config.NodeLifecycle{
		SSHPassword: "ssh-secret", MySQLRootPassword: "root-secret", ReplicationPassword: "replication-secret",
		PostgreSQLAdminPassword: "pg-admin-secret", PostgreSQLReplicationPassword: "pg-replication-secret",
		CloneAvailable: true, XtraBackupVersions: map[string]bool{"8.0": true}, LogicalDumpAllowed: true,
		PostgreSQLBaseBackupAvailable: true, PostgreSQLRewindAvailable: true,
	}
	secrets := nodeLifecycleSecrets(configuration)
	capabilities := nodeLifecycleCapabilities(configuration)
	if secrets.SSHPassword != "ssh-secret" || secrets.MySQLRootPassword != "root-secret" || secrets.ReplicationPassword != "replication-secret" || secrets.PostgreSQLAdminPassword != "pg-admin-secret" || secrets.PostgreSQLReplicationPassword != "pg-replication-secret" {
		t.Fatalf("runtime lifecycle secrets=%+v", secrets)
	}
	if !capabilities.CloneAvailable || !capabilities.XtraBackupVersions["8.0"] || !capabilities.LogicalDumpAllowed || !capabilities.PostgreSQLBaseBackupAvailable || !capabilities.PostgreSQLRewindAvailable {
		t.Fatalf("runtime lifecycle capabilities=%+v", capabilities)
	}
}
