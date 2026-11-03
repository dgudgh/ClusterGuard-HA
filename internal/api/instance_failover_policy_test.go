package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

func TestInstanceFailoverPolicyRouteExists(t *testing.T) {
	r := store.NewMemory()
	c, err := r.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "policy-test"})
	if err != nil {
		t.Fatal(err)
	}
	s := clusterPolicyServer(r)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters/"+string(c.ResourceID)+"/failover-policy", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+testControlToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("instance policy route status=%d body=%s", w.Code, w.Body.String())
	}
}

func instanceAPIFixture(t *testing.T) (*Server, *store.Repository, model.DatabaseCluster, model.DatabaseInstance) {
	t.Helper()
	r := store.NewMemory()
	c, e := r.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "selected"})
	if e != nil {
		t.Fatal(e)
	}
	x, e := r.ReconcileInstance(model.DatabaseInstance{ClusterID: c.ResourceID, Engine: c.Engine, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}, IPAddress: "127.0.0.1", Port: 3306})
	if e != nil {
		t.Fatal(e)
	}
	s := clusterPolicyServer(r)
	WithInstanceFailoverCapabilities(map[model.Engine]bool{c.Engine: true}, map[model.Engine]bool{c.Engine: true})(s)
	return s, r, c, x.Instance
}
func instancePolicyCall(s *Server, method string, id model.ResourceID, body string, auth bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/clusters/"+string(id)+"/failover-policy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth {
		req.Header.Set("Authorization", "Bearer "+testControlToken)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}
func TestInstanceFailoverPolicyAPIExplicitScopeCASAndGates(t *testing.T) {
	s, r, c, i := instanceAPIFixture(t)
	body := `{"expected_revision":0,"automatic_enabled":false,"instances":{"` + string(i.ResourceID) + `":{"automatic_eligible":false,"weight":700}}}`
	if w := instancePolicyCall(s, "PUT", c.ResourceID, body, false); w.Code != 401 {
		t.Fatal("unauthorized mutation accepted", w.Code)
	}
	for _, bad := range []string{`{}`, `{"expected_revision":0,"automatic_enabled":null,"instances":{}}`, `{"expected_revision":0,"automatic_enabled":false,"instances":null}`, `{"expected_revision":0,"automatic_enabled":false,"instances":{},"unknown":1}`, strings.Replace(body, `"weight":700`, `"weight":1001`, 1), strings.Replace(body, `"weight":700`, `"weight":1.5`, 1), strings.Replace(body, `"automatic_eligible":false,`, "", 1), strings.Replace(body, string(i.ResourceID), string(model.NewResourceID()), 1)} {
		if w := instancePolicyCall(s, "PUT", c.ResourceID, bad, true); w.Code != 400 {
			t.Fatalf("bad policy status=%d body=%s", w.Code, w.Body.String())
		}
	}
	s.maintenance = mutationMaintenanceStub{err: errors.New("maintenance")}
	if w := instancePolicyCall(s, "PUT", c.ResourceID, body, true); w.Code != 423 {
		t.Fatal("maintenance bypass", w.Code)
	}
	s.maintenance = nil
	s.authority = &apiMutationAuthorityStub{err: errors.New("no majority")}
	if w := instancePolicyCall(s, "PUT", c.ResourceID, body, true); w.Code != 503 {
		t.Fatal("majority bypass", w.Code)
	}
	s.authority = nil
	if len(r.Audits()) != 0 {
		t.Fatal("rejected policy published audit")
	}
	w := instancePolicyCall(s, "PUT", c.ResourceID, body, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("policy can be cached")
	}
	cluster, _ := r.Cluster(c.ResourceID)
	if cluster.FailoverPolicy.AutomaticEnabled || cluster.FailoverPolicy.Preference(i.ResourceID).AutomaticEligible || cluster.FailoverPolicy.Preference(i.ResourceID).Weight != 700 {
		t.Fatal("wrong persisted scope")
	}
	if w = instancePolicyCall(s, "PUT", c.ResourceID, body, true); w.Code != 409 {
		t.Fatal("stale CAS accepted", w.Code)
	}
	if _, e := r.PutClusterPolicy(store.ClusterPolicy{Engines: map[string]store.ClusterEnginePolicy{"mysql": {AutomaticFailoverSuppressed: true}}}, "admin"); e != nil {
		t.Fatal(e)
	}
	body = strings.Replace(body, `"expected_revision":0`, `"expected_revision":1`, 1)
	body = strings.Replace(body, `"automatic_enabled":false`, `"automatic_enabled":true`, 1)
	w = instancePolicyCall(s, "PUT", c.ResourceID, body, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"engine_suppressed":true`) || !strings.Contains(w.Body.String(), `"effective_automatic_enabled":false`) {
		t.Fatal("global suppression hidden", w.Body.String())
	}
	s.failoverAvailable[c.Engine] = false
	body = strings.Replace(body, `"expected_revision":1`, `"expected_revision":2`, 1)
	if w = instancePolicyCall(s, "PUT", c.ResourceID, body, true); w.Code != 409 {
		t.Fatal("unavailable execution accepted", w.Code)
	}
}
func TestInstanceFailoverPolicyAPIMissingCSRFAndOperatorCannotSave(t *testing.T) {
	s, r, _ := newAuthenticationTestServer(t)
	c, e := r.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "auth-policy"})
	if e != nil {
		t.Fatal(e)
	}
	client := &authTestClient{handler: s.Handler()}
	if w := client.login(t, "admin", apiBootstrapPassword); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := client.request(t, "POST", "/api/v1/auth/password", map[string]string{"current_password": apiBootstrapPassword, "new_password": "New-password-for-policy-123"}, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	client.cookies = nil
	client.csrf = ""
	if w := client.login(t, "admin", "New-password-for-policy-123"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	path := "/api/v1/clusters/" + string(c.ResourceID) + "/failover-policy"
	body := map[string]interface{}{"expected_revision": 0, "automatic_enabled": false, "instances": map[string]interface{}{}}
	if w := client.request(t, "PUT", path, body, false); w.Code != 403 {
		t.Fatal("CSRF bypass", w.Code)
	}
	user, _ := r.PlatformUserByUsername("admin")
	user.Role = model.PlatformRoleOperator
	expected := user.MetadataRevision
	user.MetadataRevision++
	if e = r.ReplacePlatformUser(expected, user); e != nil {
		t.Fatal(e)
	}
	client.cookies = nil
	client.csrf = ""
	if w := client.login(t, "admin", "New-password-for-policy-123"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := client.request(t, "PUT", path, body, true); w.Code != 403 {
		t.Fatal("operator mutated administrative policy", w.Code, w.Body.String())
	}
	if len(r.Audits()) != 0 {
		t.Fatal("denied mutation committed")
	}
}
