package runtime

import (
	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/pkg/model"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type instancePolicyMembers []consensus.ControllerMember

func (m instancePolicyMembers) ControllerMembers(context.Context) ([]consensus.ControllerMember, error) {
	return m, nil
}
func TestInstanceFailoverPeerProtocolRequiresEveryVoterAndIdentity(t *testing.T) {
	id, controller := model.NewResourceID(), model.NewResourceID()
	version := 1
	available := true
	status := 200
	identity := controller
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.URL.Path != "/api/v1/clusters/"+string(id)+"/failover-policy" {
			t.Error("unbound or unauthenticated peer request")
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]interface{}{"result": map[string]interface{}{"protocol_version": version, "controller_id": identity, "cluster_id": id, "automatic_available": available}})
	}))
	defer server.Close()
	check := newInstanceFailoverPeerCheck(instancePolicyMembers{{ResourceID: controller, APIAddress: server.URL}}, server.Client(), "fixture-token", false)
	if e := check(context.Background(), id, model.EngineMySQL, true); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"old", "unknown", "wrong identity", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			version = 1
			status = 200
			identity = controller
			available = true
			switch kind {
			case "old":
				status = 404
			case "unknown":
				version = 2
			case "wrong identity":
				identity = model.NewResourceID()
			case "unavailable":
				available = false
			}
			if e := check(context.Background(), id, model.EngineMySQL, true); e == nil {
				t.Fatal("unsafe voter admitted")
			}
		})
	}
	status = 200
	version = 1
	identity = controller
	available = false
	if e := check(context.Background(), id, model.EngineMySQL, false); e != nil {
		t.Fatal("manual-only policy cannot be set on compatible voter", e)
	}
	if calls != 6 {
		t.Fatal("peer was not revalidated", calls)
	}
	unavailable := newInstanceFailoverPeerCheck(instancePolicyMembers{{ResourceID: controller, APIAddress: server.URL}, {ResourceID: model.NewResourceID(), APIAddress: "https://127.0.0.1:1"}}, server.Client(), "fixture-token", false)
	if e := unavailable(context.Background(), id, model.EngineMySQL, false); e == nil {
		t.Fatal("missing voter ignored")
	}
}

func TestInstanceFailoverPeerDoesNotFollowRedirect(t *testing.T) {
	id, controller := model.NewResourceID(), model.NewResourceID()
	calls := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]interface{}{"result": map[string]interface{}{"protocol_version": 1, "controller_id": controller, "cluster_id": id, "automatic_available": true}})
	}))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	check := newInstanceFailoverPeerCheck(instancePolicyMembers{{ResourceID: controller, APIAddress: server.URL}}, server.Client(), "fixture-token", false)
	if err := check(context.Background(), id, model.EngineMySQL, true); err == nil || calls != 0 {
		t.Fatalf("registered voter redirect accepted: error=%v redirected calls=%d", err, calls)
	}
}
