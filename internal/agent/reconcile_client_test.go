package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestHTTPReconcileClientTriesControllersAndVerifiesLeaderResponse(t *testing.T) {
	now := time.Date(2026, time.July, 13, 21, 30, 0, 0, time.UTC)
	policy := reconcilePolicy()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/agent/reconcile" || request.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected request path=%s authorization=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		var payload ReconcileRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || VerifyReconcileRequest(payload, "agent-secret", now) != nil {
			t.Fatalf("invalid reconcile request=%+v err=%v", payload, err)
		}
		if calls.Add(1) == 1 {
			http.Error(writer, "not leader", http.StatusServiceUnavailable)
			return
		}
		response := ReconcileResponse{
			ClusterID: payload.ClusterID, InstanceID: payload.InstanceID, Action: ReconcileKeepVIP,
			Reason: "active majority lease", LeaseID: model.NewResourceID(), ValidUntil: now.Add(20 * time.Second), ControllerID: model.NewResourceID(),
		}
		if err := SignReconcileResponse(&response, "agent-secret"); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()
	client, err := NewHTTPReconcileClient([]string{server.URL, server.URL}, "agent-secret", server.Client(), false, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	decision, err := client.Decision(context.Background(), policy)
	if err != nil || decision.Action != ReconcileKeepVIP || calls.Load() != 2 {
		t.Fatalf("decision=%+v calls=%d err=%v", decision, calls.Load(), err)
	}
}

func TestHTTPReconcileClientRejectsInsecureOrTamperedControllers(t *testing.T) {
	if _, err := NewHTTPReconcileClient([]string{"http://controller-a:8088"}, "agent-secret", &http.Client{}, false, time.Now); err == nil {
		t.Fatal("plaintext controller URL was accepted")
	}
	now := time.Now().UTC()
	policy := reconcilePolicy()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload ReconcileRequest
		_ = json.NewDecoder(request.Body).Decode(&payload)
		_ = json.NewEncoder(writer).Encode(ReconcileResponse{
			ClusterID: payload.ClusterID, InstanceID: payload.InstanceID, Action: ReconcileKeepVIP,
			Reason: "tampered", LeaseID: model.NewResourceID(), ValidUntil: now.Add(20 * time.Second), ControllerID: model.NewResourceID(), Signature: "invalid",
		})
	}))
	defer server.Close()
	client, err := NewHTTPReconcileClient([]string{server.URL}, "agent-secret", server.Client(), false, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Decision(context.Background(), policy); err == nil {
		t.Fatal("tampered controller response was accepted")
	}
}
