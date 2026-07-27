package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type fakeOracleController struct {
	calls  []string
	status OracleBrokerStatus
}

func (controller *fakeOracleController) Discover(_ context.Context, _ ClusterPolicy) (OracleBrokerStatus, error) {
	controller.calls = append(controller.calls, "discover")
	return controller.status, nil
}

func (controller *fakeOracleController) Status(_ context.Context, _ ClusterPolicy, target string) (OracleBrokerStatus, error) {
	controller.calls = append(controller.calls, "status:"+target)
	return controller.status, nil
}

func (controller *fakeOracleController) Switchover(_ context.Context, _ ClusterPolicy, target string) error {
	controller.calls = append(controller.calls, "switchover:"+target)
	return nil
}

func testOracleAgentService(t *testing.T) (*Service, ClusterPolicy, *fakeOracleController, time.Time) {
	t.Helper()
	now := time.Date(2026, time.July, 24, 8, 0, 0, 0, time.UTC)
	clusterID := model.NewResourceID()
	policy := ClusterPolicy{
		ClusterID: clusterID, InstanceID: model.NewResourceID(), Engine: model.EngineOracle,
		OracleDatabaseUniqueName: "mesdb", OracleBrokerConfiguration: "MESDB_DG",
		OracleMembers: []string{"mesdb", "reportdb"},
	}
	controller := &fakeOracleController{status: OracleBrokerStatus{
		Database: "mesdb", Role: "PRIMARY", ConfigurationStatus: "SUCCESS",
		ReadyForSwitchover: true,
	}}
	service, err := NewService(
		Config{SharedSecret: "agent-secret", Clusters: map[model.ResourceID]ClusterPolicy{clusterID: policy}},
		&fakeVIPController{}, fakeRoleController{calls: &[]string{}}, func() time.Time { return now },
		WithOracleController(controller), WithMutationLedger(testMutationLedger(t)),
	)
	if err != nil {
		t.Fatalf("new Oracle agent service: %v", err)
	}
	return service, policy, controller, now
}

func TestOracleAgentSignatureBindsTargetDatabase(t *testing.T) {
	_, policy, _, now := testOracleAgentService(t)
	request := signedAgentRequest(t, Request{
		Command: CommandOracleBrokerStatus, Engine: model.EngineOracle,
		ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute), OracleTarget: "reportdb",
	})
	request.OracleTarget = "mesdb"
	response := NewServiceForTestHandle(t, policy, now, request)
	if response.Status != StatusBlocked || !strings.Contains(response.Message, "signature") {
		t.Fatalf("mutated Oracle target response=%+v", response)
	}
}

func NewServiceForTestHandle(t *testing.T, policy ClusterPolicy, now time.Time, request Request) Response {
	t.Helper()
	service, err := NewService(
		Config{SharedSecret: "agent-secret", Clusters: map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy}},
		&fakeVIPController{}, fakeRoleController{calls: &[]string{}}, func() time.Time { return now },
		WithOracleController(&fakeOracleController{}), WithMutationLedger(testMutationLedger(t)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return service.Handle(context.Background(), request)
}

func TestOracleAgentStatusAndSwitchoverAreAllowlisted(t *testing.T) {
	service, policy, controller, now := testOracleAgentService(t)
	statusRequest := signedAgentRequest(t, Request{
		Command: CommandOracleBrokerStatus, Engine: model.EngineOracle,
		ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute), OracleTarget: "reportdb",
	})
	response := service.Handle(context.Background(), statusRequest)
	if response.Status != StatusOK || response.OracleDatabase != "mesdb" || response.OracleRole != "PRIMARY" ||
		response.OracleConfigurationStatus != "SUCCESS" || response.OracleReadyForSwitchover == nil || !*response.OracleReadyForSwitchover {
		t.Fatalf("Oracle status response=%+v", response)
	}

	mutation := signedAgentRequest(t, Request{
		Command: CommandOracleBrokerSwitchover, Engine: model.EngineOracle,
		ClusterID: policy.ClusterID, LeaseID: model.NewResourceID(),
		ExpiresAt: now.Add(time.Minute), OracleTarget: "reportdb",
	})
	response = service.Handle(context.Background(), mutation)
	if response.Status != StatusOK || strings.Join(controller.calls, ",") != "status:reportdb,switchover:reportdb" {
		t.Fatalf("Oracle switchover response=%+v calls=%v", response, controller.calls)
	}
}

func TestOracleAgentDiscoveryReturnsStableNativeIdentityWithoutTarget(t *testing.T) {
	service, policy, controller, now := testOracleAgentService(t)
	controller.status.DBID = "1234567890"
	controller.status.InstanceName = "mesdb"
	controller.status.DatabaseStatus = "SUCCESS"
	controller.status.BrokerEnabled = true
	request := signedAgentRequest(t, Request{
		Command: CommandOracleBrokerDiscover, Engine: model.EngineOracle,
		ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute),
	})
	response := service.Handle(context.Background(), request)
	if response.Status != StatusOK || response.OracleDBID != "1234567890" ||
		response.OracleDatabase != "mesdb" || response.OracleInstanceName != "mesdb" ||
		response.OracleRole != "PRIMARY" || !response.OracleBrokerEnabled ||
		strings.Join(controller.calls, ",") != "discover" {
		t.Fatalf("Oracle discovery response=%+v calls=%v", response, controller.calls)
	}
}

func TestOracleAgentSwitchoverRequiresLeaseAndKnownMember(t *testing.T) {
	service, policy, _, now := testOracleAgentService(t)
	for name, testCase := range map[string]struct {
		request Request
		want    string
	}{
		"lease": {
			request: Request{Command: CommandOracleBrokerSwitchover, Engine: model.EngineOracle, ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute), OracleTarget: "reportdb"},
			want:    "lease",
		},
		"member": {
			request: Request{Command: CommandOracleBrokerSwitchover, Engine: model.EngineOracle, ClusterID: policy.ClusterID, LeaseID: model.NewResourceID(), ExpiresAt: now.Add(time.Minute), OracleTarget: "otherdb"},
			want:    "allowlist",
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := service.Handle(context.Background(), signedAgentRequest(t, testCase.request))
			if response.Status != StatusBlocked || !strings.Contains(response.Message, testCase.want) {
				t.Fatalf("response=%+v", response)
			}
		})
	}
}
