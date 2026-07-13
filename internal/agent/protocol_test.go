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

type fakeRoleController struct{ calls *[]string }

func (controller fakeRoleController) PersistReadOnly(context.Context, ClusterPolicy, bool) error {
	*controller.calls = append(*controller.calls, "read_only")
	return nil
}

func testAgentService(t *testing.T, vip *fakeVIPController, roles RoleController) (*Service, ClusterPolicy, time.Time) {
	t.Helper()
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	clusterID := model.NewResourceID()
	policy := ClusterPolicy{
		ClusterID: clusterID, InstanceID: model.NewResourceID(), VIP: "192.0.2.100",
		Interface: "ens160", Prefix: 24, MySQLPort: 3306,
	}
	service, err := NewService(Config{SharedSecret: "agent-secret", Clusters: map[model.ResourceID]ClusterPolicy{clusterID: policy}}, vip, roles, func() time.Time { return now })
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service, policy, now
}

func signedAgentRequest(t *testing.T, request Request) Request {
	t.Helper()
	if request.OperationID == "" {
		request.OperationID = model.NewResourceID()
	}
	if request.PlanDigest == "" {
		request.PlanDigest = "sha256:0123456789abcdef"
	}
	signature, err := SignRequest(request, "agent-secret")
	if err != nil {
		t.Fatalf("sign request: %v", err)
	}
	request.Signature = signature
	return request
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

func TestAgentResponseDoesNotExposeSharedSecret(t *testing.T) {
	service, policy, now := testAgentService(t, &fakeVIPController{}, fakeRoleController{calls: &[]string{}})
	request := signedAgentRequest(t, Request{Command: CommandVIPStatus, ClusterID: policy.ClusterID, ExpiresAt: now.Add(time.Minute), VIP: policy.VIP, Interface: policy.Interface, Prefix: policy.Prefix})
	request.Signature = "agent-secret"
	response := service.Handle(context.Background(), request)
	if strings.Contains(response.Message, "agent-secret") || strings.Contains(response.Error, "agent-secret") {
		t.Fatalf("agent response exposed secret: %+v", response)
	}
}
