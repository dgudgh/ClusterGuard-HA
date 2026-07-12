package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func newDurableOperationAPIServer(t *testing.T) (*Server, *store.Repository) {
	t.Helper()
	registry := adapter.NewRegistry()
	if err := registry.Register(mysql.New(apiRunner{})); err != nil {
		t.Fatalf("register MySQL adapter: %v", err)
	}
	repository := store.NewMemory()
	resolver := workflow.OperationResolverFunc(func(_ context.Context, request adapter.OperationRequest) (adapter.OperationRequest, error) {
		return request, nil
	})
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.TokenApproval{}, repository,
		workflow.WithOperationStore(repository), workflow.WithOperationResolver(resolver))
	return NewServer(registry, repository, service, &fakeRefresher{}, WithControlToken(testControlToken)), repository
}

func operationRequestBody(clusterID model.ResourceID, targetID model.ResourceID, key string) map[string]interface{} {
	return map[string]interface{}{
		"operation": map[string]interface{}{
			"cluster_id": clusterID, "engine": "mysql", "kind": "switchover", "requested_by": "dba",
		},
		"target_id": targetID, "idempotency_key": key,
	}
}

func decodeOperationResult(t *testing.T, responseBody []byte) model.OperationRecord {
	t.Helper()
	var envelope struct {
		Result model.OperationRecord `json:"result"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		t.Fatalf("decode operation response: %v", err)
	}
	return envelope.Result
}

func TestOperationAPIProvidesIdempotentCreateAndRead(t *testing.T) {
	server, _ := newDurableOperationAPIServer(t)
	clusterID := model.NewResourceID()
	targetID := model.NewResourceID()
	body := operationRequestBody(clusterID, targetID, "api-switch-1")

	createdResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", body)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	created := decodeOperationResult(t, createdResponse.Body.Bytes())
	if !model.ValidResourceID(created.ResourceID) || created.IdempotencyKey != "api-switch-1" {
		t.Fatalf("unexpected created operation: %+v", created)
	}

	reusedResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", body)
	if reusedResponse.Code != http.StatusOK {
		t.Fatalf("reuse status=%d body=%s", reusedResponse.Code, reusedResponse.Body.String())
	}
	reused := decodeOperationResult(t, reusedResponse.Body.Bytes())
	if reused.ResourceID != created.ResourceID {
		t.Fatalf("idempotent create returned another operation: created=%s reused=%s", created.ResourceID, reused.ResourceID)
	}

	readResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations/"+string(created.ResourceID), nil)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}
	read := decodeOperationResult(t, readResponse.Body.Bytes())
	if read.ResourceID != created.ResourceID || read.TargetID != targetID {
		t.Fatalf("read returned wrong operation: %+v", read)
	}
}

func TestOperationAPIRejectsMissingKeyAndConflictingReuse(t *testing.T) {
	server, _ := newDurableOperationAPIServer(t)
	clusterID := model.NewResourceID()
	targetID := model.NewResourceID()

	missing := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(clusterID, targetID, ""))
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing key status=%d body=%s", missing.Code, missing.Body.String())
	}
	if response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(clusterID, targetID, "api-switch-conflict")); response.Code != http.StatusCreated {
		t.Fatalf("initial create status=%d body=%s", response.Code, response.Body.String())
	}
	conflict := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(clusterID, model.NewResourceID(), "api-switch-conflict"))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting reuse status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestOperationAPIDefaultExecutionIsUnsupportedBeforeSideEffects(t *testing.T) {
	server, repository := newDurableOperationAPIServer(t)
	createdResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(model.NewResourceID(), model.NewResourceID(), "api-default-unsupported"))
	created := decodeOperationResult(t, createdResponse.Body.Bytes())

	executeResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/execute", map[string]string{"approval_token": "approved"})
	if executeResponse.Code != http.StatusNotImplemented {
		t.Fatalf("execute status=%d body=%s", executeResponse.Code, executeResponse.Body.String())
	}
	persisted, found := repository.Operation(created.ResourceID)
	if !found || persisted.Status != model.OperationUnsupported {
		t.Fatalf("unsupported terminal outcome was not persisted: found=%t record=%+v", found, persisted)
	}
	if len(persisted.Attempts) != 0 {
		t.Fatalf("unsupported operation recorded mutating steps: %+v", persisted.Attempts)
	}
}

func TestOperationAPIExposesResourceScopedPrecheckAndPlanActions(t *testing.T) {
	server, _ := newDurableOperationAPIServer(t)
	createdResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(model.NewResourceID(), model.NewResourceID(), "api-prepare-actions"))
	created := decodeOperationResult(t, createdResponse.Body.Bytes())
	for _, action := range []string{"precheck", "plan"} {
		response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/"+action, map[string]interface{}{})
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "topology observation") {
			t.Fatalf("%s route status=%d body=%s", action, response.Code, response.Body.String())
		}
	}
	verify := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/verify", map[string]interface{}{})
	if verify.Code != http.StatusNotImplemented {
		t.Fatalf("verify route status=%d body=%s", verify.Code, verify.Body.String())
	}
}
