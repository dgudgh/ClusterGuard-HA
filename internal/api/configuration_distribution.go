package api

import (
	"clusterguard.io/ha/internal/configuration"
	"context"
	"net/http"
	"strings"
	"time"
)

const configurationRoot = "/api/v1/control-plane/configuration/"

func WithConfigurationDistribution(manager *configuration.Manager) ServerOption {
	return func(s *Server) { s.configurationDistribution = manager }
}
func configurationRecoveryRoute(path string) bool {
	return strings.HasPrefix(path, configurationRoot+"tasks/")
}
func (s *Server) configurationDistributionRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	m := s.configurationDistribution
	if m == nil {
		writeError(w, 503, "configuration distribution unavailable")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, configurationRoot)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var result any
	var err error
	switch {
	case r.Method == http.MethodGet && path == "node":
		result, err = m.Local(ctx)
	case r.Method == http.MethodGet && path == "distribution":
		// Membership is read from the actual Leader, never guessed from static peers.
		if !s.authorizeLeaderQuorum(w, r) {
			return
		}
		result, err = m.Status(ctx)
	case r.Method == http.MethodPost && path == "permit":
		var p configuration.Permit
		if decode(r, &p) != nil {
			writeError(w, 400, "invalid configuration restart permit")
			return
		}
		err = m.Permit(ctx, p)
		result = map[string]bool{"allowed": err == nil}
	case r.Method == http.MethodPost && path == "candidate":
		var p struct {
			Changes map[string]int `json:"changes"`
		}
		if decode(r, &p) != nil {
			writeError(w, 400, "invalid configuration candidate")
			return
		}
		err = m.Candidate(ctx, p.Changes)
		result = map[string]bool{"valid": err == nil}
	case r.Method == http.MethodPost && (path == "plan" || path == "dispatch"):
		var p configuration.Request
		if decode(r, &p) != nil {
			writeError(w, 400, "invalid configuration distribution request")
			return
		}
		if path == "plan" {
			result, err = m.Plan(ctx, p)
		} else {
			result, err = m.Dispatch(ctx, p, softwareUpdateActor(r))
		}
	case r.Method == http.MethodPost && strings.HasPrefix(path, "tasks/"):
		parts := strings.Split(path, "/")
		if len(parts) != 3 {
			writeError(w, 404, "route not found")
			return
		}
		var p struct {
			Revision uint64 `json:"revision"`
		}
		if decode(r, &p) != nil {
			writeError(w, 400, "invalid configuration recovery request")
			return
		}
		result, err = m.Recover(ctx, parts[1], parts[2], p.Revision)
	default:
		writeError(w, 404, "route not found")
		return
	}
	if err != nil {
		writeError(w, 409, "configuration request failed validation or safety checks; refresh and review the configuration task")
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "result": result})
}
