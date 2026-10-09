package platformupdate

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestConfigurationRestarterUsesSoftwareUpdateHelperSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "cg-helper-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "custom.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/configuration-restart" {
			t.Errorf("wrong endpoint")
		}
		if r.Method == "POST" {
			w.WriteHeader(202)
		}
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	m := NewManager(Config{RootDirectory: t.TempDir(), HelperSocketPath: socket})
	r := m.ControllerRestarter()
	if err = r.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = r.RestartController(context.Background()); err != nil {
		t.Fatal(err)
	}
	if NewManager(Config{}, WithHelper(nil)).ControllerRestarter().Ready(context.Background()) == nil {
		t.Fatal("nil helper accepted")
	}
	unit, err := os.ReadFile(filepath.Join("..", "..", "packaging", "systemd", ControllerServiceUnit))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "--config") {
		t.Fatal("restart unit does not identify packaged controller")
	}
}
