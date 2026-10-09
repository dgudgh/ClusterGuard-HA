package platformupdate

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigurationRestartAcceptsOnlyFixedEmptyRequest(t *testing.T) {
	h := NewHelperHandler(t.TempDir(), nil)
	calls := 0
	h.restartController = func(context.Context) error { calls++; return nil }
	for _, tt := range []struct {
		Method, Body string
		Status       int
	}{{"GET", "", 200}, {"POST", `{"unit":"arbitrary.service"}`, 400}, {"POST", "", 202}, {"PUT", "", 405}} {
		r := httptest.NewRequest(tt.Method, "/v1/configuration-restart", strings.NewReader(tt.Body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tt.Status {
			t.Fatalf("%s status %d: %s", tt.Method, w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal("unexpected restart calls")
	}
	h.active = true
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/configuration-restart", nil))
	if w.Code != 409 || calls != 1 {
		t.Fatal("restarted during software update")
	}
}
