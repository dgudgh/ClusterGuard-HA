package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const maximumCandidateLagSeconds = int64(86400)

type clusterRegistrationPayload struct {
	DisplayName string       `json:"display_name"`
	Engine      model.Engine `json:"engine"`
	Endpoints   []struct {
		Hostname  string `json:"hostname"`
		IPAddress string `json:"ip_address"`
		Port      int    `json:"port"`
	} `json:"endpoints"`
}

func (server *Server) registerCluster(writer http.ResponseWriter, request *http.Request) {
	payload := clusterRegistrationPayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid cluster registration")
		return
	}
	payload.DisplayName = strings.TrimSpace(payload.DisplayName)
	if payload.DisplayName == "" {
		writeError(writer, http.StatusBadRequest, "cluster display name is required")
		return
	}
	if !payload.Engine.Valid() {
		writeError(writer, http.StatusBadRequest, "supported database engine is required")
		return
	}
	if len(payload.Endpoints) == 0 {
		writeError(writer, http.StatusBadRequest, "at least one database endpoint is required")
		return
	}
	endpoints := make([]model.Endpoint, len(payload.Endpoints))
	for index, endpoint := range payload.Endpoints {
		if strings.TrimSpace(endpoint.Hostname) == "" && strings.TrimSpace(endpoint.IPAddress) == "" {
			writeError(writer, http.StatusBadRequest, "database endpoint address is required")
			return
		}
		endpoints[index] = model.Endpoint{
			Kind: model.EndpointDatabase, Hostname: strings.TrimSpace(endpoint.Hostname),
			IPAddress: strings.TrimSpace(endpoint.IPAddress), Port: endpoint.Port, Active: true,
		}
	}
	cluster, createdEndpoints, err := server.store.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: payload.Engine, DisplayName: payload.DisplayName, Health: model.Health{State: model.HealthUnknown},
	}, endpoints)
	if err != nil {
		writeError(writer, http.StatusConflict, "cluster registration conflicts with existing inventory")
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]interface{}{
		"status": "ok", "result": map[string]interface{}{"cluster": cluster, "endpoints": createdEndpoints},
	})
}

func (server *Server) clusterRoute(writer http.ResponseWriter, request *http.Request, suffix string) {
	parts := strings.Split(suffix, "/")
	if len(parts) == 0 || len(parts) > 3 || !model.ValidResourceID(model.ResourceID(parts[0])) {
		writeError(writer, http.StatusBadRequest, "valid cluster UUID is required")
		return
	}
	clusterID := model.ResourceID(parts[0])
	if len(parts) == 1 {
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		cluster, found := server.store.Cluster(clusterID)
		if !found {
			writeError(writer, http.StatusNotFound, "cluster not found")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{
			"status": "ok", "result": map[string]interface{}{"cluster": cluster, "instances": server.store.Instances(clusterID)},
		})
		return
	}

	action := strings.Join(parts[1:], "/")
	if action == "discover" {
		server.discoverCluster(writer, request, clusterID)
		return
	}
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if action == "candidates" {
		policy, err := candidatePolicy(request)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid candidate policy")
			return
		}
		server.clusterCandidates(writer, request, clusterID, policy)
		return
	}
	switch action {
	case "topology":
		snapshot, found := server.store.TopologySnapshot(clusterID)
		if !found {
			writeError(writer, http.StatusConflict, "cluster has no persisted topology observation")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": snapshot})
	case "health":
		snapshot, found := server.store.TopologySnapshot(clusterID)
		if !found {
			writeError(writer, http.StatusConflict, "cluster has no persisted topology observation")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
			"cluster_id": clusterID, "health": snapshot.Health, "probes": snapshot.Probes, "observed_at": snapshot.ObservedAt,
		}})
	case "metrics":
		server.clusterMetrics(writer, clusterID)
	case "metrics/prometheus":
		server.clusterPrometheusMetrics(writer, clusterID)
	default:
		writeError(writer, http.StatusNotFound, "cluster route not found")
	}
}

func (server *Server) discoverCluster(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if request.ContentLength != 0 {
		payload := struct{}{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, "discovery request does not accept credentials or endpoint overrides")
			return
		}
	}
	if _, found := server.store.Cluster(clusterID); !found || !hasActiveDatabaseEndpoint(server.store.Endpoints(clusterID)) {
		writeError(writer, http.StatusUnprocessableEntity, "registered active database inventory is required")
		return
	}
	if server.refresher == nil {
		writeError(writer, http.StatusServiceUnavailable, "discovery refresh is unavailable")
		return
	}
	snapshot, err := server.refresher.Refresh(request.Context(), clusterID)
	if errors.Is(err, adapter.ErrUnsupported) {
		server.unsupported(writer, "discovery is unsupported for this engine")
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadGateway, "discovery refresh failed")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": snapshot})
}

func hasActiveDatabaseEndpoint(endpoints []model.Endpoint) bool {
	for _, endpoint := range endpoints {
		if endpoint.Active && endpoint.Kind == model.EndpointDatabase {
			return true
		}
	}
	return false
}

func candidatePolicy(request *http.Request) (model.CandidatePolicy, error) {
	policy := model.CandidatePolicy{MaximumLagSeconds: 10, RequireGTID: true}
	query := request.URL.Query()
	for name, values := range query {
		if (name != "maximum_lag_seconds" && name != "require_gtid") || len(values) != 1 {
			return model.CandidatePolicy{}, errors.New("invalid candidate policy query")
		}
	}
	if values, present := query["maximum_lag_seconds"]; present {
		value := values[0]
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 || parsed > maximumCandidateLagSeconds {
			return model.CandidatePolicy{}, errors.New("maximum lag is out of bounds")
		}
		policy.MaximumLagSeconds = parsed
	}
	if values, present := query["require_gtid"]; present {
		value := values[0]
		if value != "true" && value != "false" {
			return model.CandidatePolicy{}, errors.New("require GTID must be boolean")
		}
		policy.RequireGTID = value == "true"
	}
	return policy, nil
}

func (server *Server) clusterCandidates(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID, policy model.CandidatePolicy) {
	cluster, found := server.store.Cluster(clusterID)
	if !found {
		writeError(writer, http.StatusNotFound, "cluster not found")
		return
	}
	snapshot, found := server.store.TopologySnapshot(clusterID)
	if !found || !hasCompleteProbeEvidence(snapshot) {
		writeError(writer, http.StatusConflict, "candidate evaluation requires persisted probe evidence")
		return
	}
	primaries := make([]model.DatabaseInstance, 0, 1)
	for _, instance := range snapshot.Instances {
		if instance.Role == model.RolePrimary {
			primaries = append(primaries, instance)
		}
	}
	if len(primaries) != 1 {
		writeError(writer, http.StatusConflict, "candidate evaluation requires exactly one current primary")
		return
	}
	candidate, registered := server.registry.Get(cluster.Engine)
	if !registered || !candidate.Capabilities(request.Context()).Supports(adapter.CapabilityCandidates) {
		server.unsupported(writer, "candidate evaluation is unsupported for this engine")
		return
	}
	assessments, err := candidate.EvaluateCandidates(request.Context(), adapter.CandidateRequest{
		Cluster: cluster, Primary: primaries[0], Instances: snapshot.Instances, Links: snapshot.Links,
		Probes: snapshot.Probes, Policy: policy,
	})
	if err != nil {
		writeError(writer, http.StatusBadGateway, "candidate evaluation failed")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": assessments})
}

func hasCompleteProbeEvidence(snapshot model.TopologySnapshot) bool {
	if len(snapshot.Probes) == 0 {
		return false
	}
	for _, instance := range snapshot.Instances {
		found := false
		for _, probe := range snapshot.Probes {
			if probe.InstanceID == instance.ResourceID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
