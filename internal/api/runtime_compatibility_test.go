package api

import (
	"clusterguard.io/ha/internal/platformupdate"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSavedPolicyRejectsUnverifiableUpdateBeforeManager(t *testing.T) {
	for _, automatic := range []string{"true", "false"} {
		t.Run(automatic, func(t *testing.T) {
			s, r, c, i := instanceAPIFixture(t)
			body := `{"expected_revision":0,"automatic_enabled":` + automatic + `,"instances":{"` + string(i.ResourceID) + `":{"automatic_eligible":false,"weight":913}}}`
			if w := instancePolicyCall(s, "PUT", c.ResourceID, body, true); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			before := r.RuntimeCompatibilityRequirements()
			manager := &softwareUpdateManagerStub{packageValue: platformupdate.Package{PatchID: "policy-patch"}}
			WithSoftwareUpdates(manager)(s)
			for _, mode := range []string{"rollback", "execute", "retry"} {
				req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/updates/policy-patch/"+mode, strings.NewReader(`{"confirmation":"policy-patch"}`))
				req.Header.Set("Authorization", "Bearer "+testControlToken)
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, req)
				var result struct {
					Result struct {
						Rejected bool `json:"runtime_compatibility_rejected"`
					} `json:"result"`
				}
				if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil || !result.Result.Rejected {
					t.Fatal("admission rejection must be explicitly distinguishable from uncertain conflict", w.Body.String())
				}
				if w.Code != 409 || manager.startedMode != "" {
					t.Fatal("unverifiable target reached execution", mode, w.Code, manager.startedMode, w.Body.String())
				}
			}
			if r.RuntimeCompatibilityRequirements() != before {
				t.Fatal("failed update changed floor")
			}
		})
	}
}
