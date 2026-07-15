package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestHTTPReconcileClientReusesDurableSignedDecisionDuringLeaderElection(t *testing.T) {
	now := time.Date(2026, time.July, 15, 2, 30, 0, 0, time.UTC)
	policy := reconcilePolicy()
	var available atomic.Bool
	available.Store(true)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !available.Load() {
			http.Error(writer, "leader election", http.StatusServiceUnavailable)
			return
		}
		var payload ReconcileRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
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

	cacheDirectory := filepath.Join(t.TempDir(), "decisions")
	newClient := func() *HTTPReconcileClient {
		cache, err := NewFileReconcileDecisionCache(cacheDirectory)
		if err != nil {
			t.Fatal(err)
		}
		client, err := NewHTTPReconcileClient(
			[]string{server.URL}, "agent-secret", server.Client(), false, func() time.Time { return now },
			WithReconcileDecisionCache(cache),
		)
		if err != nil {
			t.Fatal(err)
		}
		return client
	}

	fresh, err := newClient().Decision(context.Background(), policy)
	if err != nil || fresh.Action != ReconcileKeepVIP {
		t.Fatalf("fresh decision=%+v err=%v", fresh, err)
	}
	available.Store(false)
	now = now.Add(5 * time.Second)
	cached, err := newClient().Decision(context.Background(), policy)
	if err != nil || cached != fresh {
		t.Fatalf("cached decision=%+v fresh=%+v err=%v", cached, fresh, err)
	}
	if entries, err := os.ReadDir(cacheDirectory); err != nil || len(entries) != 1 {
		t.Fatalf("durable cache entries=%d err=%v", len(entries), err)
	}

	now = now.Add(16 * time.Second)
	if _, err := newClient().Decision(context.Background(), policy); err == nil {
		t.Fatal("expired cached decision was accepted")
	}
}

func TestHTTPReconcileClientRejectsTamperedDurableDecisionCache(t *testing.T) {
	now := time.Date(2026, time.July, 15, 2, 45, 0, 0, time.UTC)
	policy := reconcilePolicy()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "leader election", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cacheDirectory := filepath.Join(t.TempDir(), "decisions")
	cache, err := NewFileReconcileDecisionCache(cacheDirectory)
	if err != nil {
		t.Fatal(err)
	}
	tampered := ReconcileResponse{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileKeepVIP,
		Reason: "forged", LeaseID: model.NewResourceID(), ValidUntil: now.Add(20 * time.Second), ControllerID: model.NewResourceID(), Signature: "invalid",
	}
	if err := cache.Store(tampered); err != nil {
		t.Fatal(err)
	}
	client, err := NewHTTPReconcileClient(
		[]string{server.URL}, "agent-secret", server.Client(), false, func() time.Time { return now },
		WithReconcileDecisionCache(cache),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Decision(context.Background(), policy); err == nil {
		t.Fatal("tampered durable decision cache was accepted")
	}
}
