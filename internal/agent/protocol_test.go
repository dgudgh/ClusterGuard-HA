package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type fakeVIPController struct {
	owns       bool
	calls      *[]string
	releaseErr error
}

func (controller *fakeVIPController) Status(context.Context, ClusterPolicy) (bool, error) {
	if controller.calls != nil {
		*controller.calls = append(*controller.calls, "status")
	}
	return controller.owns, nil
}

func (controller *fakeVIPController) Acquire(context.Context, ClusterPolicy) error {
	if controller.calls != nil {
		*controller.calls = append(*controller.calls, "acquire")
	}
	controller.owns = true
	return nil
}

func (controller *fakeVIPController) Release(context.Context, ClusterPolicy) error {
	if controller.calls != nil {
		*controller.calls = append(*controller.calls, "release")
	}
	controller.owns = false
	return controller.releaseErr
}

type fakeRoleController struct {
	calls         *[]string
	readOnly      bool
	superReadOnly bool
}

type fakePostgreSQLController struct {
	calls      *[]string
	running    bool
	inRecovery bool
}

func (controller *fakePostgreSQLController) Status(context.Context, ClusterPolicy) (bool, bool, error) {
	*controller.calls = append(*controller.calls, "postgresql_status")
	return controller.running, controller.inRecovery, nil
}
func (controller *fakePostgreSQLController) Stop(context.Context, ClusterPolicy) error {
	*controller.calls = append(*controller.calls, "postgresql_stop")
	controller.running = false
	return nil
}
func (controller *fakePostgreSQLController) Start(context.Context, ClusterPolicy) error {
	*controller.calls = append(*controller.calls, "postgresql_start")
	controller.running = true
	return nil
}
func (controller *fakePostgreSQLController) Promote(context.Context, ClusterPolicy) error {
	*controller.calls = append(*controller.calls, "postgresql_promote")
	controller.inRecovery = false
	return nil
}
func (controller *fakePostgreSQLController) Repoint(_ context.Context, _ ClusterPolicy, source PostgreSQLPeer) error {
	*controller.calls = append(*controller.calls, "postgresql_repoint:"+string(source.InstanceID))
	return nil
}
func (controller *fakePostgreSQLController) Rewind(_ context.Context, _ ClusterPolicy, source PostgreSQLPeer) error {
	*controller.calls = append(*controller.calls, "postgresql_rewind:"+string(source.InstanceID))
	return nil
}
func (controller *fakePostgreSQLController) BaseBackup(_ context.Context, _ ClusterPolicy, source PostgreSQLPeer) error {
	*controller.calls = append(*controller.calls, "postgresql_basebackup:"+string(source.InstanceID))
	return nil
}

func (controller fakeRoleController) PersistReadOnly(_ context.Context, _ ClusterPolicy, readOnly bool) error {
	*controller.calls = append(*controller.calls, "read_only")
	return nil
}

func (controller fakeRoleController) Status(context.Context, ClusterPolicy) (bool, bool, error) {
	*controller.calls = append(*controller.calls, "role_status")
	return controller.readOnly, controller.superReadOnly, nil
}

func testAgentService(t *testing.T, vip *fakeVIPController, roles RoleController) (*Service, ClusterPolicy, time.Time) {
	t.Helper()
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	clusterID := model.NewResourceID()
	policy := ClusterPolicy{
		ClusterID: clusterID, InstanceID: model.NewResourceID(), VIP: "192.0.2.100",
		Interface: "ens160", Prefix: 24, MySQLPort: 3306,
	}
	service, err := NewService(
		Config{SharedSecret: "agent-secret", Clusters: map[model.ResourceID]ClusterPolicy{clusterID: policy}},
		vip, roles, func() time.Time { return now }, WithMutationLedger(testMutationLedger(t)),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service, policy, now
}

func testMutationLedger(t *testing.T) MutationLedger {
	t.Helper()
	ledger, err := NewFileMutationLedger(t.TempDir())
	if err != nil {
		t.Fatalf("new test mutation ledger: %v", err)
	}
	return ledger
}

func signedAgentRequest(t *testing.T, request Request) Request {
	t.Helper()
	if request.OperationID == "" {
		request.OperationID = model.NewResourceID()
	}
	if request.PlanDigest == "" {
		request.PlanDigest = "sha256:" + strings.Repeat("a", 64)
	}
	signature, err := SignRequest(request, "agent-secret")
	if err != nil {
		t.Fatalf("sign request: %v", err)
	}
	request.Signature = signature
	return request
}

func TestAgentMutationRequiresCryptographicPlanDigest(t *testing.T) {
	service, policy, now := testAgentService(t, &fakeVIPController{}, fakeRoleController{calls: &[]string{}})
	request := signedAgentRequest(t, Request{
		Command: CommandVIPAcquire, ClusterID: policy.ClusterID, OperationID: model.NewResourceID(),
		LeaseID: model.NewResourceID(), PlanDigest: "sha256:not-a-real-digest", ExpiresAt: now.Add(time.Minute),
		VIP: policy.VIP, Interface: policy.Interface, Prefix: policy.Prefix,
	})
	response := service.Handle(context.Background(), request)
	if response.Status != StatusBlocked || !strings.Contains(response.Message, "plan digest") {
		t.Fatalf("non-cryptographic plan digest response=%+v", response)
	}
}

func TestAgentRequiresLeaseForVIPMutation(t *testing.T) {
	service, policy, now := testAgentService(t, &fakeVIPController{}, fakeRoleController{calls: &[]string{}})
	request := signedAgentRequest(t, Request{
		Command: CommandVIPAcquire, ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute),
		VIP: policy.VIP, Interface: policy.Interface, Prefix: policy.Prefix,
	})
	response := service.Handle(context.Background(), request)
	if response.Status != StatusBlocked || !strings.Contains(response.Message, "lease") {
		t.Fatalf("missing lease response=%+v", response)
	}
}

func TestAgentRejectsUnknownCommand(t *testing.T) {
	service, policy, now := testAgentService(t, &fakeVIPController{}, fakeRoleController{calls: &[]string{}})
	request := signedAgentRequest(t, Request{Command: "shell", ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute)})
	response := service.Handle(context.Background(), request)
	if response.Status != StatusBlocked || !strings.Contains(response.Message, "unsupported") {
		t.Fatalf("unknown command response=%+v", response)
	}
}

func TestAgentRejectsVIPOutsideAllowlist(t *testing.T) {
	service, policy, now := testAgentService(t, &fakeVIPController{}, fakeRoleController{calls: &[]string{}})
	request := signedAgentRequest(t, Request{
		Command: CommandVIPAcquire, ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute),
		VIP: "192.0.2.200", Interface: policy.Interface, Prefix: policy.Prefix,
	})
	response := service.Handle(context.Background(), request)
	if response.Status != StatusBlocked || !strings.Contains(response.Message, "allowlist") {
		t.Fatalf("outside VIP response=%+v", response)
	}
}

func TestAgentRejectsExpiredOrInvalidSignature(t *testing.T) {
	service, policy, now := testAgentService(t, &fakeVIPController{}, fakeRoleController{calls: &[]string{}})
	expired := signedAgentRequest(t, Request{Command: CommandVIPStatus, ClusterID: policy.ClusterID, ExpiresAt: now.Add(-time.Second), VIP: policy.VIP, Interface: policy.Interface, Prefix: policy.Prefix})
	if response := service.Handle(context.Background(), expired); response.Status != StatusBlocked || !strings.Contains(response.Message, "expired") {
		t.Fatalf("expired response=%+v", response)
	}
	invalid := expired
	invalid.ExpiresAt = now.Add(time.Minute)
	invalid.Signature = "invalid"
	if response := service.Handle(context.Background(), invalid); response.Status != StatusBlocked || !strings.Contains(response.Message, "signature") {
		t.Fatalf("invalid signature response=%+v", response)
	}
}

func TestAgentSelfIsolationReleasesVIPBeforeReadOnly(t *testing.T) {
	calls := []string{}
	vip := &fakeVIPController{owns: true, calls: &calls}
	roles := fakeRoleController{calls: &calls}
	service, policy, now := testAgentService(t, vip, roles)
	request := signedAgentRequest(t, Request{Command: CommandSelfIsolate, ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute), VIP: policy.VIP, Interface: policy.Interface, Prefix: policy.Prefix})
	response := service.Handle(context.Background(), request)
	if response.Status != StatusOK {
		t.Fatalf("self isolate response=%+v", response)
	}
	if strings.Join(calls, ",") != "release,read_only" {
		t.Fatalf("self isolation order=%v", calls)
	}
}

func TestAgentSelfIsolationEnforcesReadOnlyWhenVIPReleaseFails(t *testing.T) {
	calls := []string{}
	vip := &fakeVIPController{owns: true, calls: &calls, releaseErr: errors.New("release failed")}
	service, policy, now := testAgentService(t, vip, fakeRoleController{calls: &calls})
	request := signedAgentRequest(t, Request{Command: CommandSelfIsolate, ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute), VIP: policy.VIP, Interface: policy.Interface, Prefix: policy.Prefix})
	response := service.Handle(context.Background(), request)
	if response.Status != StatusError {
		t.Fatalf("self isolate with release failure response=%+v", response)
	}
	if strings.Join(calls, ",") != "release,read_only" {
		t.Fatalf("self isolation did not enforce read-only after release failure: %v", calls)
	}
}

func TestAgentRoleStatusReportsBothMySQLReadOnlyFlags(t *testing.T) {
	calls := []string{}
	roles := fakeRoleController{calls: &calls, readOnly: true, superReadOnly: true}
	service, policy, now := testAgentService(t, &fakeVIPController{}, roles)
	request := signedAgentRequest(t, Request{Command: CommandRoleStatus, ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute)})
	response := service.Handle(context.Background(), request)
	if response.Status != StatusOK || response.ReadOnly == nil || response.SuperReadOnly == nil || !*response.ReadOnly || !*response.SuperReadOnly {
		t.Fatalf("role status response=%+v", response)
	}
	if strings.Join(calls, ",") != "role_status" {
		t.Fatalf("role status calls=%v", calls)
	}
}

func TestAgentResponseDoesNotExposeSharedSecret(t *testing.T) {
	service, policy, now := testAgentService(t, &fakeVIPController{}, fakeRoleController{calls: &[]string{}})
	request := signedAgentRequest(t, Request{Command: CommandVIPStatus, ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute), VIP: policy.VIP, Interface: policy.Interface, Prefix: policy.Prefix})
	request.Signature = "agent-secret"
	response := service.Handle(context.Background(), request)
	if strings.Contains(response.Message, "agent-secret") || strings.Contains(response.Error, "agent-secret") {
		t.Fatalf("agent response exposed secret: %+v", response)
	}
}

func TestPostgreSQLAgentSignatureBindsEngineAndSourceIdentity(t *testing.T) {
	now := time.Now().UTC()
	request := signedAgentRequest(t, Request{
		Command: CommandPostgreSQLRepoint, Engine: model.EnginePostgreSQL, ClusterID: model.NewResourceID(), OperationID: model.NewResourceID(),
		LeaseID: model.NewResourceID(), PlanDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute),
		SourceInstanceID: model.NewResourceID(), SourceNodeID: model.NewResourceID(), SourceHostname: "pg-01", SourceIPAddress: "192.0.2.10", SourcePort: 5432,
	})
	for name, mutate := range map[string]func(*Request){
		"engine":          func(candidate *Request) { candidate.Engine = model.EngineMySQL },
		"source identity": func(candidate *Request) { candidate.SourceInstanceID = model.NewResourceID() },
		"source node":     func(candidate *Request) { candidate.SourceNodeID = model.NewResourceID() },
		"source address":  func(candidate *Request) { candidate.SourceIPAddress = "192.0.2.99" },
		"source port":     func(candidate *Request) { candidate.SourcePort = 6432 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			signature, err := SignRequest(changed, "agent-secret")
			if err != nil {
				t.Fatal(err)
			}
			if signature == request.Signature {
				t.Fatalf("%s was not bound by the request signature", name)
			}
		})
	}
}

func TestPostgreSQLAgentSelfIsolationReleasesVIPBeforeStoppingService(t *testing.T) {
	calls := []string{}
	clusterID := model.NewResourceID()
	policy := ClusterPolicy{ClusterID: clusterID, InstanceID: model.NewResourceID(), Engine: model.EnginePostgreSQL, VIP: "192.0.2.100", Interface: "ens160", Prefix: 24, PostgreSQLPort: 5432, PostgreSQLService: "postgresql-16", PostgreSQLUser: "postgres", PostgreSQLDataDirectory: "/var/lib/postgresql/16/main", PostgreSQLBinaryDirectory: "/usr/lib/postgresql/16/bin", PostgreSQLPassfile: "/etc/clusterguard/pgpass"}
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	postgres := &fakePostgreSQLController{calls: &calls, running: true}
	service, err := NewService(Config{SharedSecret: "agent-secret", Clusters: map[model.ResourceID]ClusterPolicy{clusterID: policy}}, &fakeVIPController{owns: true, calls: &calls}, fakeRoleController{calls: &calls}, func() time.Time { return now }, WithPostgreSQLController(postgres), WithMutationLedger(testMutationLedger(t)))
	if err != nil {
		t.Fatal(err)
	}
	request := signedAgentRequest(t, Request{Command: CommandSelfIsolate, Engine: model.EnginePostgreSQL, ClusterID: clusterID, OperationID: model.NewResourceID(), LeaseID: model.NewResourceID(), PlanDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute), VIP: policy.VIP, Interface: policy.Interface, Prefix: policy.Prefix})
	response := service.Handle(context.Background(), request)
	if response.Status != StatusOK || strings.Join(calls, ",") != "release,postgresql_stop" {
		t.Fatalf("PostgreSQL self isolation response=%+v calls=%v", response, calls)
	}
}

func TestPostgreSQLAgentRejectsInvalidSourceAndAcceptsExactPeer(t *testing.T) {
	calls := []string{}
	clusterID := model.NewResourceID()
	peer := PostgreSQLPeer{InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(), Hostname: "pg-01", IPAddress: "192.0.2.10", Port: 5432}
	policy := ClusterPolicy{ClusterID: clusterID, InstanceID: model.NewResourceID(), Engine: model.EnginePostgreSQL, PostgreSQLNodeID: model.NewResourceID(), PostgreSQLPort: 5432, PostgreSQLService: "postgresql-16", PostgreSQLUser: "postgres", PostgreSQLDataDirectory: "/var/lib/postgresql/16/main", PostgreSQLBinaryDirectory: "/usr/lib/postgresql/16/bin", PostgreSQLPassfile: "/etc/clusterguard/pgpass", PostgreSQLPeers: []PostgreSQLPeer{peer}}
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	postgres := &fakePostgreSQLController{calls: &calls, running: true, inRecovery: true}
	service, err := NewService(Config{SharedSecret: "agent-secret", Clusters: map[model.ResourceID]ClusterPolicy{clusterID: policy}}, &fakeVIPController{}, fakeRoleController{calls: &calls}, func() time.Time { return now }, WithPostgreSQLController(postgres), WithMutationLedger(testMutationLedger(t)))
	if err != nil {
		t.Fatal(err)
	}
	base := Request{Command: CommandPostgreSQLRepoint, Engine: model.EnginePostgreSQL, ClusterID: clusterID, OperationID: model.NewResourceID(), LeaseID: model.NewResourceID(), PlanDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute), SourceInstanceID: peer.InstanceID, SourceNodeID: peer.NodeID, SourceHostname: peer.Hostname, SourceIPAddress: peer.IPAddress, SourcePort: peer.Port}
	outside := base
	outside.SourceIPAddress = "not-an-ip"
	outside = signedAgentRequest(t, outside)
	if response := service.Handle(context.Background(), outside); response.Status != StatusBlocked || !strings.Contains(response.Message, "source") {
		t.Fatalf("invalid source response=%+v", response)
	}
	accepted := signedAgentRequest(t, base)
	if response := service.Handle(context.Background(), accepted); response.Status != StatusOK || strings.Join(calls, ",") != "postgresql_repoint:"+string(peer.InstanceID) {
		t.Fatalf("listed source response=%+v calls=%v", response, calls)
	}
}

func TestPostgreSQLAgentAcceptsSignedDynamicSourceWhenPeerAllowlistIsStale(t *testing.T) {
	calls := []string{}
	clusterID := model.NewResourceID()
	stalePeer := PostgreSQLPeer{InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(), Hostname: "pg-02", IPAddress: "192.0.2.20", Port: 5432}
	currentSource := PostgreSQLPeer{InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(), Hostname: "pg-02", IPAddress: "192.0.2.21", Port: 5432}
	policy := ClusterPolicy{
		ClusterID: clusterID, InstanceID: model.NewResourceID(), Engine: model.EnginePostgreSQL,
		PostgreSQLNodeID: model.NewResourceID(), PostgreSQLPort: 5432, PostgreSQLService: "postgresql-16",
		PostgreSQLUser: "postgres", PostgreSQLDataDirectory: "/var/lib/postgresql/16/main",
		PostgreSQLBinaryDirectory: "/usr/lib/postgresql/16/bin", PostgreSQLPassfile: "/etc/clusterguard/pgpass",
		PostgreSQLPeers: []PostgreSQLPeer{stalePeer},
	}
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	postgres := &fakePostgreSQLController{calls: &calls, running: true, inRecovery: true}
	service, err := NewService(Config{SharedSecret: "agent-secret", Clusters: map[model.ResourceID]ClusterPolicy{clusterID: policy}}, &fakeVIPController{}, fakeRoleController{calls: &calls}, func() time.Time { return now }, WithPostgreSQLController(postgres), WithMutationLedger(testMutationLedger(t)))
	if err != nil {
		t.Fatal(err)
	}
	request := signedAgentRequest(t, Request{
		Command: CommandPostgreSQLRepoint, Engine: model.EnginePostgreSQL, ClusterID: clusterID,
		OperationID: model.NewResourceID(), LeaseID: model.NewResourceID(),
		PlanDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute),
		SourceInstanceID: currentSource.InstanceID, SourceNodeID: currentSource.NodeID,
		SourceHostname: currentSource.Hostname, SourceIPAddress: currentSource.IPAddress, SourcePort: currentSource.Port,
	})
	if response := service.Handle(context.Background(), request); response.Status != StatusOK || strings.Join(calls, ",") != "postgresql_repoint:"+string(currentSource.InstanceID) {
		t.Fatalf("signed dynamic source response=%+v calls=%v", response, calls)
	}
}

func TestPostgreSQLAgentMutationRequiresLease(t *testing.T) {
	calls := []string{}
	clusterID := model.NewResourceID()
	policy := ClusterPolicy{ClusterID: clusterID, InstanceID: model.NewResourceID(), Engine: model.EnginePostgreSQL, PostgreSQLPort: 5432, PostgreSQLService: "postgresql-16", PostgreSQLUser: "postgres", PostgreSQLDataDirectory: "/var/lib/postgresql/16/main", PostgreSQLBinaryDirectory: "/usr/lib/postgresql/16/bin", PostgreSQLPassfile: "/etc/clusterguard/pgpass"}
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	service, err := NewService(Config{SharedSecret: "agent-secret", Clusters: map[model.ResourceID]ClusterPolicy{clusterID: policy}}, &fakeVIPController{}, fakeRoleController{calls: &calls}, func() time.Time { return now }, WithPostgreSQLController(&fakePostgreSQLController{calls: &calls}), WithMutationLedger(testMutationLedger(t)))
	if err != nil {
		t.Fatal(err)
	}
	request := signedAgentRequest(t, Request{Command: CommandPostgreSQLPromote, Engine: model.EnginePostgreSQL, ClusterID: clusterID, OperationID: model.NewResourceID(), PlanDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute)})
	if response := service.Handle(context.Background(), request); response.Status != StatusBlocked || !strings.Contains(response.Message, "lease") {
		t.Fatalf("missing lease response=%+v", response)
	}
}

func TestAgentMutationLedgerPreventsDuplicateHostMutationAcrossProcesses(t *testing.T) {
	calls := []string{}
	clusterID := model.NewResourceID()
	policy := ClusterPolicy{
		ClusterID: clusterID, InstanceID: model.NewResourceID(), Engine: model.EnginePostgreSQL,
		PostgreSQLPort: 5432, PostgreSQLService: "postgresql-16", PostgreSQLUser: "postgres",
		PostgreSQLDataDirectory: "/var/lib/postgresql/16/main", PostgreSQLBinaryDirectory: "/usr/lib/postgresql/16/bin",
		PostgreSQLPassfile: "/etc/clusterguard/pgpass", PostgreSQLDatabase: "postgres", PostgreSQLReplicationUser: "replicator",
	}
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	request := signedAgentRequest(t, Request{
		Command: CommandPostgreSQLPromote, Engine: model.EnginePostgreSQL, ClusterID: clusterID,
		OperationID: model.NewResourceID(), LeaseID: model.NewResourceID(),
		PlanDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute),
	})
	directory := t.TempDir()
	for attempt := 0; attempt < 2; attempt++ {
		ledger, err := NewFileMutationLedger(directory)
		if err != nil {
			t.Fatal(err)
		}
		service, err := NewService(
			Config{SharedSecret: "agent-secret", Clusters: map[model.ResourceID]ClusterPolicy{clusterID: policy}},
			&fakeVIPController{}, fakeRoleController{calls: &calls}, func() time.Time { return now },
			WithPostgreSQLController(&fakePostgreSQLController{calls: &calls, running: true, inRecovery: true}),
			WithMutationLedger(ledger),
		)
		if err != nil {
			t.Fatal(err)
		}
		if response := service.Handle(context.Background(), request); response.Status != StatusOK {
			t.Fatalf("attempt %d response=%+v", attempt, response)
		}
	}
	if got := strings.Join(calls, ","); got != "postgresql_promote" {
		t.Fatalf("durable replay executed mutation more than once: %s", got)
	}
}
