package api

import (
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
	"context"
	"errors"
	"net/http"
)

func WithInstanceFailoverCapabilities(defaults, available map[model.Engine]bool) ServerOption {
	return func(s *Server) {
		s.failoverDefaults = make(map[model.Engine]bool)
		s.failoverAvailable = make(map[model.Engine]bool)
		for e, v := range defaults {
			s.failoverDefaults[e] = v
		}
		for e, v := range available {
			s.failoverAvailable[e] = v
		}
	}
}
func WithInstanceFailoverPeerCheck(localID model.ResourceID, check func(context.Context, model.ResourceID, model.Engine, bool) error) ServerOption {
	return func(s *Server) { s.failoverControllerID = localID; s.failoverPeerCheck = check }
}
func (s *Server) instanceFailoverPolicyView(cluster model.DatabaseCluster) map[string]interface{} {
	policy := cluster.FailoverPolicy.Clone()
	if policy == nil {
		policy = &model.InstanceFailoverPolicy{AutomaticEnabled: s.failoverDefaults[cluster.Engine]}
	}
	preferences := map[model.ResourceID]model.InstanceFailoverPreference{}
	for _, instance := range s.store.Instances(cluster.ResourceID) {
		preferences[instance.ResourceID] = cluster.FailoverPolicy.Preference(instance.ResourceID)
	}
	policy.Instances = preferences
	suppressed := s.store.AutomaticFailoverSuppressed(cluster.Engine)
	return map[string]interface{}{"protocol_version": 1, "controller_id": s.failoverControllerID, "cluster_id": cluster.ResourceID, "policy": policy, "automatic_available": s.failoverAvailable[cluster.Engine], "engine_suppressed": suppressed, "effective_automatic_enabled": policy.AutomaticEnabled && s.failoverAvailable[cluster.Engine] && !suppressed}
}
func (s *Server) instanceFailoverPolicyRoute(w http.ResponseWriter, r *http.Request, id model.ResourceID) {
	w.Header().Set("Cache-Control", "no-store")
	cluster, ok := s.store.Cluster(id)
	if !ok {
		writeError(w, 404, "cluster not found")
		return
	}
	if cluster.Engine != model.EngineMySQL && cluster.Engine != model.EnginePostgreSQL {
		writeError(w, 400, "instance failover policy supports MySQL and PostgreSQL")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, map[string]interface{}{"status": "ok", "result": s.instanceFailoverPolicyView(cluster)})
	case http.MethodPut:
		var body struct {
			ExpectedRevision *uint64 `json:"expected_revision"`
			AutomaticEnabled *bool   `json:"automatic_enabled"`
			Instances        map[model.ResourceID]struct {
				AutomaticEligible *bool `json:"automatic_eligible"`
				Weight            *int  `json:"weight"`
			} `json:"instances"`
		}
		if err := decode(r, &body); err != nil || body.ExpectedRevision == nil || body.AutomaticEnabled == nil || body.Instances == nil {
			writeError(w, 400, "explicit policy revision, automatic switch and instance preferences are required")
			return
		}
		if *body.AutomaticEnabled && !s.failoverAvailable[cluster.Engine] {
			writeError(w, 409, "automatic failover prerequisites are unavailable")
			return
		}
		p := model.InstanceFailoverPolicy{AutomaticEnabled: *body.AutomaticEnabled, Instances: map[model.ResourceID]model.InstanceFailoverPreference{}}
		for instanceID, pref := range body.Instances {
			if pref.AutomaticEligible == nil || pref.Weight == nil {
				writeError(w, 400, "explicit instance participation and weight are required")
				return
			}
			p.Instances[instanceID] = model.InstanceFailoverPreference{AutomaticEligible: *pref.AutomaticEligible, Weight: *pref.Weight}
		}
		if s.failoverPeerCheck != nil {
			if err := s.failoverPeerCheck(r.Context(), id, cluster.Engine, p.AutomaticEnabled); err != nil {
				writeError(w, 503, err.Error())
				return
			}
		}
		_, err := s.store.PutInstanceFailoverPolicy(id, *body.ExpectedRevision, p, softwareUpdateActor(r))
		if err != nil {
			status := 503
			if errors.Is(err, store.ErrValidation) {
				status = 400
			} else if errors.Is(err, store.ErrConflict) {
				status = 409
			} else if errors.Is(err, store.ErrNotFound) {
				status = 404
			}
			writeError(w, status, err.Error())
			return
		}
		cluster, _ = s.store.Cluster(id)
		writeJSON(w, 200, map[string]interface{}{"status": "ok", "result": s.instanceFailoverPolicyView(cluster)})
	default:
		writeError(w, 405, "instance failover policy supports GET and PUT")
	}
}
