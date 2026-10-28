package api

import (
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
	"context"
	"net/http"
	"time"
)

type GatewayInspector interface {
	InspectGateway(context.Context, model.ResourceID) (endpoint.GatewayView, error)
}

func WithGatewayInspector(inspector GatewayInspector) ServerOption {
	return func(s *Server) { s.gateway = inspector }
}
func (s *Server) clusterGateway(w http.ResponseWriter, r *http.Request, id model.ResourceID) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "gateway inspection supports GET only")
		return
	}
	if _, ok := s.store.Cluster(id); !ok {
		writeError(w, http.StatusNotFound, "cluster not found")
		return
	}
	if s.gateway == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway provider is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	result, err := s.gateway.InspectGateway(ctx, id)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "gateway route inspection failed; check controller configuration and authenticated gateway connectivity")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "result": result})
}
