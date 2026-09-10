package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

type recoveryManagerStub struct {
	preflightErr, errorOnExecute error
	calls                        int
}

func (m *recoveryManagerStub) Preflight(context.Context, model.RecoveryTask) error {
	return m.preflightErr
}
func (m *recoveryManagerStub) Execute(context.Context, model.ResourceID, uint64) (model.RecoveryTask, error) {
	m.calls++
	return model.RecoveryTask{}, m.errorOnExecute
}

func recoveryAPIFixture(t *testing.T, engine model.Engine) (*store.Repository, model.DatabaseCluster) {
	t.Helper()
	r := store.NewMemory()
	c, err := r.UpsertCluster(model.DatabaseCluster{DisplayName: "recovery-api", Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		identity := model.EngineIdentity{"server_uuid": string(model.NewResourceID())}
		if engine == model.EnginePostgreSQL {
			identity = model.EngineIdentity{"resource_id": string(model.NewResourceID()), "system_identifier": "7678901569924304935"}
		}
		if _, err = r.ReconcileInstance(model.DatabaseInstance{ClusterID: c.ResourceID, Engine: engine, EngineIdentity: identity, Hostname: fmt.Sprintf("db%d", i), IPAddress: fmt.Sprintf("192.0.2.%d", i+1), Port: 5432}); err != nil {
			t.Fatal(err)
		}
	}
	return r, c
}

func TestDisasterAPIConfirmationAndDurableFailure(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		t.Run(string(engine), func(t *testing.T) {
			r, c := recoveryAPIFixture(t, engine)
			manager := &recoveryManagerStub{errorOnExecute: errors.New("connection password=hidden-recovery-secret failed")}
			var pending func(context.Context)
			s := NewServer(nil, r, nil, nil, WithControlToken(testControlToken), WithDisasterRecovery(manager, func(run func(context.Context)) { pending = run }))
			path := "/api/v1/clusters/" + string(c.ResourceID) + "/recovery/"
			anonymous := httptest.NewRecorder()
			s.Handler().ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost, path+"execute", strings.NewReader(`{}`)))
			if anonymous.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous mutation: %d", anonymous.Code)
			}
			planned := callJSON(t, s.Handler(), http.MethodPost, path+"plan", map[string]interface{}{})
			if planned.Code != http.StatusOK {
				t.Fatalf("plan %d %s", planned.Code, planned.Body.String())
			}
			var response struct {
				Result struct {
					Task  model.RecoveryTask `json:"task"`
					Ready bool               `json:"ready"`
				} `json:"result"`
			}
			if err := json.Unmarshal(planned.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if !response.Result.Ready || len(response.Result.Task.Members) != 3 {
				t.Fatal("preflight not ready")
			}
			cluster, _ := r.Cluster(c.ResourceID)
			if cluster.RecoveryFreeze {
				t.Fatal("read-only plan froze the database")
			}
			task := response.Result.Task
			payload := map[string]interface{}{"task_id": task.ResourceID, "revision": task.MetadataRevision, "confirm_cluster": c.DisplayName, "acknowledge_fencing": false}
			if result := callJSON(t, s.Handler(), http.MethodPost, path+"execute", payload); result.Code != http.StatusBadRequest {
				t.Fatal("unacknowledged fencing accepted")
			}
			payload["acknowledge_fencing"] = true
			payload["revision"] = task.MetadataRevision + 1
			if result := callJSON(t, s.Handler(), http.MethodPost, path+"execute", payload); result.Code != http.StatusConflict {
				t.Fatal("stale confirmation accepted")
			}
			payload["revision"] = task.MetadataRevision
			if result := callJSON(t, s.Handler(), http.MethodPost, path+"execute", payload); result.Code != http.StatusAccepted {
				t.Fatalf("execute: %d %s", result.Code, result.Body.String())
			}
			if result := callJSON(t, s.Handler(), http.MethodPost, path+"execute", payload); result.Code != http.StatusConflict {
				t.Fatal("double submission accepted")
			}
			if pending == nil || manager.calls != 0 {
				t.Fatal("execution not detached from HTTP response")
			}
			pending(context.Background())
			status := callJSON(t, s.Handler(), http.MethodGet, path+"status", nil)
			if status.Code != http.StatusOK || strings.Contains(status.Body.String(), "hidden-recovery-secret") {
				t.Fatal("recovery status leaked failure secret")
			}
			stored, _ := r.RecoveryTask(task.ResourceID)
			if stored.Stage != model.RecoveryBlocked || manager.calls != 1 {
				t.Fatal("pre-start failure was not durable")
			}
			cluster, _ = r.Cluster(c.ResourceID)
			if cluster.RecoveryFreeze {
				t.Fatal("preflight failure changed production freeze")
			}
		})
	}
}

func TestDisasterAPIPreflightFailureDoesNotEnableExecution(t *testing.T) {
	r, c := recoveryAPIFixture(t, model.EnginePostgreSQL)
	m := &recoveryManagerStub{preflightErr: errors.New("old Agent lacks recovery support")}
	s := NewServer(nil, r, nil, nil, WithControlToken(testControlToken), WithDisasterRecovery(m, func(func(context.Context)) {}))
	response := callJSON(t, s.Handler(), http.MethodPost, "/api/v1/clusters/"+string(c.ResourceID)+"/recovery/plan", map[string]interface{}{})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":false`) {
		t.Fatalf("unqualified preflight: %s", response.Body.String())
	}
}

func TestRecoveryPublicTaskOmitsConnectionMetadata(t *testing.T) {
	task := model.RecoveryTask{Members: []model.DatabaseInstance{{EngineMetadata: map[string]string{"primary_conninfo": "host=db password=secret-value"}, EngineIdentity: model.EngineIdentity{"resource_id": string(model.NewResourceID()), "token": "secret-value"}}}, Message: "token=secret-value", Events: []model.RecoveryEvent{{Message: "primary_conninfo=host=db password=secret-value"}}}
	out, err := json.Marshal(publicRecoveryTask(task))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "secret-value") {
		t.Fatal("public recovery task leaked credentials")
	}
}
