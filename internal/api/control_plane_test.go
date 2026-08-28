package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func TestHealthAndReadinessProbesExposeOnlySafeControlPlaneState(t *testing.T) {
	leaderID := model.NewResourceID()
	provider := ControlPlaneStatusProviderFunc(func(context.Context) (ControlPlaneStatus, error) {
		return ControlPlaneStatus{
			Mode: "raft", LocalControllerID: model.NewResourceID(), Role: "candidate",
			LeaderID: leaderID, LeaderAddress: "192.0.2.11:10009", LeaderAPIAddress: "https://controller-a.example:8088",
			VoterCount: 3, Ready: false, ReadinessReason: "leader_unavailable",
		}, nil
	})
	server := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil, WithControlPlaneStatus(provider))

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request := httptest.NewRequest(method, "/healthz", nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get(requestIDHeader) == "" {
			t.Fatalf("healthz %s status=%d headers=%v body=%s", method, response.Code, response.Header(), response.Body.String())
		}
		if method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatalf("healthz HEAD emitted body %q", response.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	request.Header.Set(requestIDHeader, "probe-123")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get(requestIDHeader) != "probe-123" {
		t.Fatalf("readyz status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	for _, secret := range []string{string(leaderID), "192.0.2.11", "controller-a.example"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("public readiness exposed control-plane detail %q: %s", secret, response.Body.String())
		}
	}
	if !strings.Contains(response.Body.String(), `"reason":"leader_unavailable"`) {
		t.Fatalf("readiness reason missing: %s", response.Body.String())
	}
}

func TestReadyProbeFailsClosedWhenStatusCollectionFails(t *testing.T) {
	provider := ControlPlaneStatusProviderFunc(func(context.Context) (ControlPlaneStatus, error) {
		return ControlPlaneStatus{}, errors.New("sensitive backend failure")
	})
	server := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil, WithControlPlaneStatus(provider))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "sensitive backend failure") {
		t.Fatalf("failed status readiness=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthenticatedControlPlaneStatusReturnsOperationalEvidence(t *testing.T) {
	startedAt := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	want := ControlPlaneStatus{
		Mode: "raft", LocalControllerID: model.NewResourceID(), Role: "leader",
		LeaderID: model.NewResourceID(), LeaderKnown: true, VoterCount: 3,
		QuorumConfirmed: true, MutationAuthority: true, SnapshotCASActive: true, ReplicatedLogCompressionActive: true,
		Term: 9, LastIndex: 71, AppliedIndex: 71, StateRevision: 42,
		Ready: true, ReadinessReason: "ready", StartedAt: startedAt, UptimeSeconds: 600,
		ClusterCount: 2, ActiveOperations: 1, IndeterminateOperations: 1, ActiveLifecycleTasks: 1,
		ControllerMembers: []ControllerMemberStatus{{
			ResourceID: model.NewResourceID(), RaftAddress: "192.0.2.11:10009", APIAddress: "https://192.0.2.11:3000",
		}},
		DataNodeMembers: []DataNodeMemberStatus{{
			ResourceID: model.NewResourceID(), NodeName: "cg-data-0001", Kind: model.NodeData,
			Hostname: "database-a", IPAddress: "192.0.2.31",
		}},
	}
	server := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil,
		WithControlToken(testControlToken),
		WithControlPlaneStatus(ControlPlaneStatusProviderFunc(func(context.Context) (ControlPlaneStatus, error) { return want, nil })),
	)
	response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/control-plane/status", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("control-plane status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Status string             `json:"status"`
		Result ControlPlaneStatus `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode control-plane status: %v", err)
	}
	if envelope.Status != "ok" || !reflect.DeepEqual(envelope.Result, want) {
		t.Fatalf("control-plane status=%+v want=%+v", envelope.Result, want)
	}
}

func TestAuthenticatedPlatformVersionExposesPatchCompatibility(t *testing.T) {
	server := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil, WithControlToken(testControlToken))
	response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/platform/version", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("platform version=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Status string `json:"status"`
		Result struct {
			Product        string `json:"product"`
			Binary         string `json:"binary"`
			Version        string `json:"version"`
			Release        string `json:"release"`
			StateFormat    int    `json:"state_format"`
			UpdateProtocol int    `json:"update_protocol"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode platform version: %v", err)
	}
	if envelope.Status != "ok" || envelope.Result.Product != "ClusterGuard HA" || envelope.Result.Binary != "clusterguard" ||
		envelope.Result.Version == "" || envelope.Result.Release == "" || envelope.Result.StateFormat < 1 || envelope.Result.UpdateProtocol < 1 {
		t.Fatalf("platform version=%+v", envelope)
	}
}

func TestStandaloneControlPlaneStatusDoesNotTreatDormantPlansAsActive(t *testing.T) {
	repository := store.NewMemory()
	clusterID := model.NewResourceID()
	if _, _, err := repository.CreateOperation(model.OperationRecord{
		Operation: model.Operation{ClusterID: clusterID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover},
		TargetID:  model.NewResourceID(), IdempotencyKey: "dormant-plan",
	}); err != nil {
		t.Fatalf("create planned operation: %v", err)
	}
	server := NewServer(adapter.NewRegistry(), repository, nil, nil)
	if status := server.localControlPlaneStatus(); status.ActiveOperations != 0 {
		t.Fatalf("planned history reported as active work: %+v", status)
	}
}

func TestStandaloneControlPlaneCountsOnlyUnreviewedIndeterminateOperations(t *testing.T) {
	repository := store.NewMemory()
	create := func(key string) model.OperationRecord {
		record, _, err := repository.CreateOperation(model.OperationRecord{
			Operation: model.Operation{ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationSwitchover},
			TargetID:  model.NewResourceID(), IdempotencyKey: key,
		})
		if err != nil {
			t.Fatalf("create %s: %v", key, err)
		}
		record, err = repository.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{
			Stage: model.StageVerify, Status: model.OperationIndeterminate, Message: "review required",
		})
		if err != nil {
			t.Fatalf("mark %s indeterminate: %v", key, err)
		}
		return record
	}
	unreviewed := create("control-plane-unreviewed")
	reviewed := create("control-plane-reviewed")
	if _, err := repository.ReviewIndeterminateOperation(reviewed.ResourceID, reviewed.MetadataRevision, "dba", "verified against the live database"); err != nil {
		t.Fatalf("review operation: %v", err)
	}
	server := NewServer(adapter.NewRegistry(), repository, nil, nil)
	status := server.localControlPlaneStatus()
	if status.IndeterminateOperations != 1 || !unreviewed.RequiresReview() {
		t.Fatalf("control-plane review count=%+v", status)
	}
}

func TestHandlerCorrelatesErrorsAndReplacesUnsafeRequestIDs(t *testing.T) {
	server := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil)
	tests := []struct {
		name       string
		requestID  string
		wantExact  string
		wantChange bool
	}{
		{name: "preserve", requestID: "ops-console.123:attempt-2", wantExact: "ops-console.123:attempt-2"},
		{name: "replace spaces", requestID: "unsafe request id", wantChange: true},
		{name: "replace oversized", requestID: strings.Repeat("x", 129), wantChange: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/missing", nil)
			request.Header.Set(requestIDHeader, test.requestID)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			got := response.Header().Get(requestIDHeader)
			if got == "" || (test.wantExact != "" && got != test.wantExact) || (test.wantChange && got == test.requestID) {
				t.Fatalf("request ID input=%q output=%q", test.requestID, got)
			}
			var envelope struct {
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.RequestID != got {
				t.Fatalf("error request ID=%q header=%q err=%v body=%s", envelope.RequestID, got, err, response.Body.String())
			}
		})
	}
}
