package agent

import (
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/disaster"
	"clusterguard.io/ha/pkg/model"
)

func TestRecoveryRequestsAcceptCanonicalEvidenceFingerprint(t *testing.T) {
	calls := []string{}
	service, p, now := testAgentService(t, &fakeVIPController{}, fakeRoleController{calls: &calls})
	p.Engine = model.EnginePostgreSQL
	peer := PostgreSQLPeer{InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(), IPAddress: "192.0.2.10", Hostname: "pg-source", Port: 5432}
	p.PostgreSQLPeers = []PostgreSQLPeer{peer}
	service.configuration.Clusters[p.ClusterID] = p
	fingerprint := disaster.EvidenceFingerprint(model.RecoveryEvidence{InstanceID: p.InstanceID, Engine: model.EnginePostgreSQL, ControlState: "shut down"})
	for _, command := range []string{CommandRecoveryGuard, CommandRecoveryStart, CommandRecoveryRebuild} {
		request := Request{Command: command, Engine: model.EnginePostgreSQL, ClusterID: p.ClusterID, LeaseID: model.NewResourceID(), RecoveryTaskID: model.NewResourceID(), RecoveryFingerprint: fingerprint, PlanDigest: disaster.Digest("frozen inventory"), ExpiresAt: now.Add(time.Minute)}
		if command == CommandRecoveryRebuild {
			request.SourceInstanceID = peer.InstanceID
			request.SourceNodeID = peer.NodeID
			request.SourceIPAddress = peer.IPAddress
			request.SourceHostname = peer.Hostname
			request.SourcePort = peer.Port
		}
		request = signedAgentRequest(t, request)
		if _, response, ok := service.validate(request); !ok {
			t.Fatalf("%s rejected emitted evidence: %s", command, response.Message)
		}
		for _, badFingerprint := range []string{"", strings.TrimPrefix(fingerprint, "sha256:"), "sha256:" + fingerprint} {
			bad := request
			bad.RecoveryFingerprint = badFingerprint
			bad = signedAgentRequest(t, bad)
			if _, _, ok := service.validate(bad); ok {
				t.Fatalf("%s accepted malformed fingerprint", command)
			}
		}
	}
}

func TestRecoveryReconcileSignatureBindsTaskAndShortLease(t *testing.T) {
	now := time.Now().UTC()
	request := ReconcileRequest{ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID()}
	for _, action := range []ReconcileAction{ReconcileRecoveryPrimary, ReconcileRecoveryActivate} {
		response := ReconcileResponse{ClusterID: request.ClusterID, InstanceID: request.InstanceID, Action: action, Reason: "recovery fixture", LeaseID: model.NewResourceID(), RecoveryTaskID: model.NewResourceID(), ControllerID: model.NewResourceID(), ValidUntil: now.Add(10 * time.Second)}
		if err := SignReconcileResponse(&response, "fixture-only-secret"); err != nil {
			t.Fatal(err)
		}
		if err := VerifyReconcileResponse(response, request, "fixture-only-secret", now); err != nil {
			t.Fatal(err)
		}
		for _, mutate := range []func(*ReconcileResponse){
			func(r *ReconcileResponse) { r.RecoveryTaskID = model.NewResourceID() },
			func(r *ReconcileResponse) { r.LeaseID = model.NewResourceID() },
			func(r *ReconcileResponse) { r.Action = ReconcileKeepVIP },
			func(r *ReconcileResponse) { r.RecoveryTaskID = "" },
			func(r *ReconcileResponse) {
				r.ValidUntil = now.Add(11 * time.Second)
				_ = SignReconcileResponse(r, "fixture-only-secret")
			},
		} {
			bad := response
			mutate(&bad)
			if err := VerifyReconcileResponse(bad, request, "fixture-only-secret", now); err == nil {
				t.Fatal("accepted altered or overly long recovery authorization")
			}
		}
	}
}
