package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
)

// A write needs the control token, exactly like every other mutating control
// API route in this package.
func clusterPolicyRequest(t *testing.T, method, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, "/api/v1/cluster-policy", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	return request
}

func clusterPolicyServer(repository *store.Repository) *Server {
	return NewServer(adapter.NewRegistry(), repository, nil, nil, WithControlToken(testControlToken))
}

func TestClusterPolicyRouteReadsAnEmptyPolicyBeforeOneIsStored(t *testing.T) {
	server := clusterPolicyServer(store.NewMemory())
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, clusterPolicyRequest(t, http.MethodGet, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("cluster policy read status=%d body=%s", response.Code, response.Body.String())
	}
	if cache := response.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("cluster policy must not be cached: %q", cache)
	}
	var envelope struct {
		Status string `json:"status"`
		Result struct {
			Policy  store.ClusterPolicy `json:"policy"`
			Summary []string            `json:"summary"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode cluster policy: %v", err)
	}
	if len(envelope.Result.Policy.Engines) != 0 || len(envelope.Result.Summary) != 0 {
		t.Fatalf("a fresh cluster must not report a policy: %+v", envelope.Result)
	}
}

func TestClusterPolicyRouteStoresAuditsAndClears(t *testing.T) {
	repository := store.NewMemory()
	server := clusterPolicyServer(repository)

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, clusterPolicyRequest(t, http.MethodPut,
		`{"engines":{"mysql":{"automatic_failover_minimum_observations":6,"automatic_failover_suppressed":true}},"note":"计划维护"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("cluster policy write status=%d body=%s", response.Code, response.Body.String())
	}
	if !repository.AutomaticFailoverSuppressed("mysql") {
		t.Fatal("the console change did not reach the replicated policy")
	}
	if settings := repository.ClusterEnginePolicy("mysql"); settings.AutomaticFailoverMinimumObservations != 6 {
		t.Fatalf("observations override lost: %+v", settings)
	}
	audits := repository.Audits()
	if len(audits) != 1 {
		t.Fatalf("a policy change must be audited exactly once, got %d", len(audits))
	}
	message := audits[0].Message
	if !strings.Contains(message, "mysql") || !strings.Contains(message, "观测次数 6") ||
		!strings.Contains(message, "维护抑制") {
		t.Fatalf("the audit must describe what actually changed: %q", message)
	}
	if strings.TrimSpace(audits[0].Actor) == "" {
		t.Fatalf("the audit must record who changed it: %+v", audits[0])
	}

	cleared := httptest.NewRecorder()
	server.Handler().ServeHTTP(cleared, clusterPolicyRequest(t, http.MethodPut, `{}`))
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear cluster policy status=%d body=%s", cleared.Code, cleared.Body.String())
	}
	if repository.AutomaticFailoverSuppressed("mysql") {
		t.Fatal("clearing the policy must restore the configured behaviour")
	}
	if len(repository.Audits()) != 2 || !strings.Contains(repository.Audits()[1].Message, "清除集群策略") {
		t.Fatalf("clearing must be audited too: %+v", repository.Audits())
	}
}

func TestClusterPolicyRouteRejectsOutOfRangeValuesWithoutStoringThem(t *testing.T) {
	repository := store.NewMemory()
	server := clusterPolicyServer(repository)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, clusterPolicyRequest(t, http.MethodPut,
		`{"engines":{"mysql":{"automatic_failover_minimum_observations":1}}}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("one observation cannot reject a missed probe, status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repository.ClusterPolicy().Engines) != 0 {
		t.Fatal("a rejected policy must not be stored")
	}
	if len(repository.Audits()) != 0 {
		t.Fatal("a rejected policy must not be audited as a change")
	}

	unsupported := httptest.NewRecorder()
	server.Handler().ServeHTTP(unsupported, clusterPolicyRequest(t, http.MethodPut,
		`{"engines":{"oracle":{"automatic_failover_minimum_observations":6}}}`))
	if unsupported.Code != http.StatusBadRequest {
		t.Fatalf("oracle has no automatic failover, status=%d body=%s", unsupported.Code, unsupported.Body.String())
	}
	duplicate := httptest.NewRecorder()
	server.Handler().ServeHTTP(duplicate, clusterPolicyRequest(t, http.MethodPut,
		`{"engines":{"mysql":{"automatic_failover_suppressed":true}," mysql ":{}}}`))
	if duplicate.Code != http.StatusBadRequest || len(repository.ClusterPolicy().Engines) != 0 {
		t.Fatalf("whitespace-aliased engine keys must be rejected, status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
}

func TestClusterPolicyRouteReportsUnavailableStore(t *testing.T) {
	server := clusterPolicyServer(nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, clusterPolicyRequest(t, http.MethodGet, ""))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("cluster policy without a store status=%d body=%s", response.Code, response.Body.String())
	}
}
