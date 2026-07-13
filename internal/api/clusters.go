package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const maximumCandidateLagSeconds = int64(86400)
const maximumDiscoveryBodyBytes = 1024

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
	if writeClusterRegistrationFailure(writer, cluster, createdEndpoints, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]interface{}{
		"status": "ok", "result": map[string]interface{}{"cluster": cluster, "endpoints": createdEndpoints},
	})
}

func writeClusterRegistrationFailure(writer http.ResponseWriter, cluster model.DatabaseCluster, endpoints []model.Endpoint, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, store.ErrPostCommitDurability):
		writeJSON(writer, http.StatusInternalServerError, map[string]interface{}{
			"status": "error", "message": "cluster registration committed with durability warning",
			"result": map[string]interface{}{"cluster": cluster, "endpoints": endpoints},
		})
	case errors.Is(err, store.ErrValidation):
		writeError(writer, http.StatusBadRequest, "invalid cluster registration")
	case errors.Is(err, store.ErrConflict):
		writeError(writer, http.StatusConflict, "cluster registration conflicts with existing inventory")
	default:
		writeError(writer, http.StatusInternalServerError, "cluster registration failed")
	}
	return true
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
			"status": "ok", "result": map[string]interface{}{
				"cluster": cluster, "instances": server.store.Instances(clusterID), "endpoints": server.store.Endpoints(clusterID),
			},
		})
		return
	}

	action := strings.Join(parts[1:], "/")
	if action == "discover" {
		server.discoverCluster(writer, request, clusterID)
		return
	}
	if action == "ha-endpoints" {
		server.clusterHAEndpoints(writer, request, clusterID)
		return
	}
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if action == "candidates" {
		expectedObservation, err := requestedObservation(request)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid observation identifier")
			return
		}
		policy, err := candidatePolicy(request)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid candidate policy")
			return
		}
		if _, found := server.store.Cluster(clusterID); !found {
			writeError(writer, http.StatusNotFound, "cluster not found")
			return
		}
		server.clusterCandidates(writer, request, clusterID, policy, expectedObservation)
		return
	}
	if _, found := server.store.Cluster(clusterID); !found {
		writeError(writer, http.StatusNotFound, "cluster not found")
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
		expectedObservation, err := requestedObservation(request)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid observation identifier")
			return
		}
		snapshot, found := server.store.TopologySnapshot(clusterID)
		if !found {
			if expectedObservation != nil {
				writeError(writer, http.StatusConflict, "topology observation changed")
				return
			}
			writeError(writer, http.StatusConflict, "cluster has no persisted topology observation")
			return
		}
		if !matchesObservation(expectedObservation, snapshot.ObservedAt) {
			writeError(writer, http.StatusConflict, "topology observation changed")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
			"cluster_id": clusterID, "health": snapshot.Health, "probes": snapshot.Probes, "observed_at": snapshot.ObservedAt,
		}})
	case "metrics":
		expectedObservation, err := requestedObservation(request)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid observation identifier")
			return
		}
		server.clusterMetrics(writer, clusterID, expectedObservation)
	case "metrics/prometheus":
		server.clusterPrometheusMetrics(writer, clusterID)
	default:
		writeError(writer, http.StatusNotFound, "cluster route not found")
	}
}

type haEndpointPayload struct {
	Kind      model.EndpointKind `json:"kind"`
	IPAddress string             `json:"ip_address"`
	Interface string             `json:"interface"`
	Prefix    int                `json:"prefix"`
	OwnerID   model.ResourceID   `json:"owner_id"`
	Active    bool               `json:"active"`
}

type haEndpointView struct {
	Resource model.HAEndpoint `json:"resource"`
	Endpoint model.Endpoint   `json:"endpoint"`
}

func (server *Server) clusterHAEndpoints(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if _, found := server.store.Cluster(clusterID); !found {
		writeError(writer, http.StatusNotFound, "cluster not found")
		return
	}
	switch request.Method {
	case http.MethodGet:
		resources := server.store.HAEndpoints(clusterID)
		views := make([]haEndpointView, 0, len(resources))
		for _, resource := range resources {
			endpoint, found := server.store.Endpoint(resource.EndpointID)
			if !found {
				writeError(writer, http.StatusInternalServerError, "HA endpoint inventory is inconsistent")
				return
			}
			views = append(views, haEndpointView{Resource: resource, Endpoint: endpoint})
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": views})
	case http.MethodPost:
		payload := haEndpointPayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid HA endpoint")
			return
		}
		resource, endpoint, err := server.store.PutHAEndpoint(store.HAEndpointSpec{
			ClusterID: clusterID, Kind: payload.Kind, IPAddress: payload.IPAddress,
			Interface: payload.Interface, Prefix: payload.Prefix, OwnerID: payload.OwnerID, Active: payload.Active,
		})
		if err != nil {
			switch {
			case errors.Is(err, store.ErrValidation):
				writeError(writer, http.StatusBadRequest, "invalid HA endpoint")
			case errors.Is(err, store.ErrConflict):
				writeError(writer, http.StatusConflict, "HA endpoint conflicts with existing inventory")
			default:
				writeError(writer, http.StatusInternalServerError, "store HA endpoint failed")
			}
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]interface{}{"status": "ok", "result": haEndpointView{Resource: resource, Endpoint: endpoint}})
	default:
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (server *Server) discoverCluster(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := validateDiscoveryBody(request); err != nil {
		writeError(writer, http.StatusBadRequest, "discovery request does not accept credentials or endpoint overrides")
		return
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
	if errors.Is(err, store.ErrPostCommitDurability) {
		writeJSON(writer, http.StatusInternalServerError, map[string]interface{}{
			"status": "error", "message": "topology observation published with durability warning", "result": snapshot,
		})
		return
	}
	if errors.Is(err, store.ErrStaleObservation) {
		writeError(writer, http.StatusConflict, "topology observation is stale")
		return
	}
	if errors.Is(err, store.ErrInventoryChanged) {
		writeError(writer, http.StatusConflict, "database inventory changed during discovery")
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadGateway, "discovery refresh failed")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": snapshot})
}

func validateDiscoveryBody(request *http.Request) error {
	contents, err := io.ReadAll(io.LimitReader(request.Body, maximumDiscoveryBodyBytes+1))
	if err != nil {
		return err
	}
	if len(contents) > maximumDiscoveryBodyBytes {
		return errors.New("discovery request body is too large")
	}
	if strings.TrimSpace(string(contents)) == "" {
		return nil
	}
	payload := map[string]json.RawMessage{}
	if err := json.Unmarshal(contents, &payload); err != nil {
		return err
	}
	if payload == nil || len(payload) != 0 {
		return errors.New("discovery request body must be an empty object")
	}
	return nil
}

func hasActiveDatabaseEndpoint(endpoints []model.Endpoint) bool {
	for _, endpoint := range endpoints {
		if endpoint.Active && endpoint.Kind == model.EndpointDatabase {
			return true
		}
	}
	return false
}

func requestedObservation(request *http.Request) (*time.Time, error) {
	values, present := request.URL.Query()["observation_id"]
	if !present {
		return nil, nil
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return nil, errors.New("observation identifier must be singular and non-empty")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, values[0])
	if err != nil {
		return nil, err
	}
	observedAt = observedAt.UTC()
	return &observedAt, nil
}

func matchesObservation(expected *time.Time, actual time.Time) bool {
	return expected == nil || expected.Equal(actual)
}

func candidatePolicy(request *http.Request) (model.CandidatePolicy, error) {
	policy := model.CandidatePolicy{MaximumLagSeconds: 10, RequireGTID: true}
	query := request.URL.Query()
	for name, values := range query {
		if (name != "maximum_lag_seconds" && name != "require_gtid" && name != "observation_id") || len(values) != 1 {
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

func (server *Server) clusterCandidates(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID, policy model.CandidatePolicy, expectedObservation *time.Time) {
	cluster, found := server.store.Cluster(clusterID)
	if !found {
		writeError(writer, http.StatusNotFound, "cluster not found")
		return
	}
	snapshot, found := server.store.TopologySnapshot(clusterID)
	if !found {
		if expectedObservation != nil {
			writeError(writer, http.StatusConflict, "topology observation changed")
			return
		}
		writeError(writer, http.StatusConflict, "candidate evaluation requires persisted probe evidence")
		return
	}
	if !matchesObservation(expectedObservation, snapshot.ObservedAt) {
		writeError(writer, http.StatusConflict, "topology observation changed")
		return
	}
	if !hasCompleteProbeEvidence(snapshot) {
		writeError(writer, http.StatusConflict, "candidate evaluation requires persisted probe evidence")
		return
	}
	primaries := make([]model.DatabaseInstance, 0, 1)
	for _, instance := range snapshot.Instances {
		if instance.Role == model.RolePrimary && hasCurrentDiscoveryProbe(snapshot.Probes, instance.ResourceID, snapshot.ObservedAt) {
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
		Probes: snapshot.Probes, ObservedAt: snapshot.ObservedAt, Policy: policy,
	})
	if err != nil {
		writeError(writer, http.StatusBadGateway, "candidate evaluation failed")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": assessments})
}

func hasCurrentDiscoveryProbe(probes []model.ProbeStatus, instanceID model.ResourceID, observedAt time.Time) bool {
	if observedAt.IsZero() {
		return false
	}
	for _, probe := range probes {
		if probe.InstanceID == instanceID && probe.DiscoveryObservedAt.Equal(observedAt) {
			return true
		}
	}
	return false
}

func hasCompleteProbeEvidence(snapshot model.TopologySnapshot) bool {
	if len(snapshot.Probes) == 0 || snapshot.ObservedAt.IsZero() {
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
