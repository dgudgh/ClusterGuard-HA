package platformupdate

import (
	"clusterguard.io/ha/internal/maintenance"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"
)

// ControllerServiceUnit is deliberately fixed, never a request/config parameter.
const ControllerServiceUnit = "clusterguard-ha.service"

// ControllerRestarter reuses the exact helper transport chosen for software updates.
func (m *Manager) ControllerRestarter() ControllerRestarter {
	client, _ := m.helper.(*UnixHelperClient)
	return ControllerRestarter{Client: client}
}

// ControllerRestarter exposes only a fixed service restart on the peer-credential
// authenticated Unix socket. It accepts no unit name, shell text or file path.
type ControllerRestarter struct{ Client *UnixHelperClient }

func (r ControllerRestarter) Ready(ctx context.Context) error { return r.call(ctx, http.MethodGet) }
func (r ControllerRestarter) RestartController(ctx context.Context) error {
	return r.call(ctx, http.MethodPost)
}
func (r ControllerRestarter) call(ctx context.Context, method string) error {
	if r.Client == nil {
		return fmt.Errorf("configuration restart helper unavailable")
	}
	req, _ := http.NewRequestWithContext(ctx, method, "http://unix/v1/configuration-restart", nil)
	resp, e := r.Client.client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if (method == http.MethodGet && resp.StatusCode != 200) || (method == http.MethodPost && resp.StatusCode != 202) {
		return fmt.Errorf("configuration restart capability unavailable")
	}
	return nil
}
func (h *HelperHandler) configurationRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		helperError(w, 405, "method not allowed")
		return
	}
	// Unlike software jobs, a configuration restart cannot acquire or bypass the
	// upgrade maintenance lock. Reject it while that root-owned marker exists.
	if e := maintenance.NewFileGate(maintenance.DefaultMarkerPath).Check(r.Context()); e != nil {
		helperError(w, 423, "software update maintenance active")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active {
		helperError(w, 409, "software update job active")
		return
	}
	if r.Method == http.MethodGet {
		w.WriteHeader(200)
		return
	}
	body, e := io.ReadAll(io.LimitReader(r.Body, 2))
	if e != nil || len(body) > 0 {
		helperError(w, 400, "configuration restart takes no payload")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	restart := h.restartController
	if restart == nil {
		restart = func(ctx context.Context) error {
			return exec.CommandContext(ctx, "systemctl", "--no-block", "restart", ControllerServiceUnit).Run()
		}
	}
	if e = restart(ctx); e != nil {
		helperError(w, 503, "controller restart request failed")
		return
	}
	w.WriteHeader(202)
}
