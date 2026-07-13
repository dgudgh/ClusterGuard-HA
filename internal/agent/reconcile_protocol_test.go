package agent

import (
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestReconcileProtocolRejectsTamperingAndExpiredMessages(t *testing.T) {
	now := time.Date(2026, time.July, 13, 21, 0, 0, 0, time.UTC)
	request := ReconcileRequest{ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID(), RequestedAt: now, Nonce: "0123456789abcdef"}
	if err := SignReconcileRequest(&request, "agent-secret"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyReconcileRequest(request, "agent-secret", now); err != nil {
		t.Fatalf("verify request: %v", err)
	}
	tampered := request
	tampered.InstanceID = model.NewResourceID()
	if err := VerifyReconcileRequest(tampered, "agent-secret", now); err == nil {
		t.Fatal("tampered request was accepted")
	}
	if err := VerifyReconcileRequest(request, "agent-secret", now.Add(time.Minute)); err == nil {
		t.Fatal("expired request was accepted")
	}

	response := ReconcileResponse{ClusterID: request.ClusterID, InstanceID: request.InstanceID, Action: ReconcileKeepVIP, Reason: "active majority lease", LeaseID: model.NewResourceID(), ValidUntil: now.Add(20 * time.Second), ControllerID: model.NewResourceID()}
	if err := SignReconcileResponse(&response, "agent-secret"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyReconcileResponse(response, request, "agent-secret", now); err != nil {
		t.Fatalf("verify response: %v", err)
	}
	response.Action = ReconcileSelfIsolate
	if err := VerifyReconcileResponse(response, request, "agent-secret", now); err == nil {
		t.Fatal("tampered response was accepted")
	}
}
