package api

import (
	"errors"
	"net/http"
	"strings"

	"clusterguard.io/ha/internal/store"
)

// The cluster policy endpoint is the writable half of the configuration story.
// Everything under /api/v1/control-plane/configuration is node-local and fixed
// at start-up; this record is replicated, so a change made through the console
// reaches every controller without a restart and is audited like any other
// metadata change.

type clusterEnginePolicyPayload struct {
	AutomaticFailoverMinimumObservations     int  `json:"automatic_failover_minimum_observations,omitempty"`
	AutomaticFailoverFailureWindowSeconds    int  `json:"automatic_failover_failure_window_seconds,omitempty"`
	AutomaticFailoverOperationTimeoutSeconds int  `json:"automatic_failover_operation_timeout_seconds,omitempty"`
	AutomaticFailoverSuppressed              bool `json:"automatic_failover_suppressed,omitempty"`
}

type clusterPolicyPayload struct {
	Engines map[string]clusterEnginePolicyPayload `json:"engines,omitempty"`
	Note    string                                `json:"note,omitempty"`
}

func (server *Server) clusterPolicyRoute(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if server.store == nil {
		writeError(writer, http.StatusServiceUnavailable, "cluster policy store is unavailable")
		return
	}
	switch request.Method {
	case http.MethodGet:
		server.readClusterPolicy(writer)
	case http.MethodPut:
		server.writeClusterPolicy(writer, request)
	default:
		writeError(writer, http.StatusMethodNotAllowed, "cluster policy supports GET and PUT")
	}
}

func (server *Server) readClusterPolicy(writer http.ResponseWriter) {
	policy := server.store.ClusterPolicy()
	writeJSON(writer, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"result": map[string]interface{}{
			"policy":  policy,
			"summary": server.store.ClusterPolicySummary(),
		},
	})
}

func (server *Server) writeClusterPolicy(writer http.ResponseWriter, request *http.Request) {
	payload := clusterPolicyPayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid cluster policy request")
		return
	}
	policy := store.ClusterPolicy{Note: strings.TrimSpace(payload.Note)}
	if len(payload.Engines) > 0 {
		policy.Engines = make(map[string]store.ClusterEnginePolicy, len(payload.Engines))
		for engine, settings := range payload.Engines {
			engine = strings.TrimSpace(engine)
			if engine == "" {
				writeError(writer, http.StatusBadRequest, "cluster policy engine is required")
				return
			}
			if _, exists := policy.Engines[engine]; exists {
				writeError(writer, http.StatusBadRequest, "duplicate cluster policy engine")
				return
			}
			policy.Engines[engine] = store.ClusterEnginePolicy{
				AutomaticFailoverMinimumObservations:     settings.AutomaticFailoverMinimumObservations,
				AutomaticFailoverFailureWindowSeconds:    settings.AutomaticFailoverFailureWindowSeconds,
				AutomaticFailoverOperationTimeoutSeconds: settings.AutomaticFailoverOperationTimeoutSeconds,
				AutomaticFailoverSuppressed:              settings.AutomaticFailoverSuppressed,
			}
		}
	}
	actor := softwareUpdateActor(request)
	stored, err := server.store.PutClusterPolicy(policy, actor)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrValidation):
			writeError(writer, http.StatusBadRequest, err.Error())
		default:
			writeError(writer, http.StatusServiceUnavailable, "cluster policy persistence failed")
		}
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"result": map[string]interface{}{
			"policy":  stored,
			"summary": server.store.ClusterPolicySummary(),
			"actor":   actor,
		},
	})
}
