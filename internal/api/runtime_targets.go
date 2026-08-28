package api

import (
	"errors"
	"net/http"
	"strings"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

type runtimeTargetPayload struct {
	ResourceID    model.ResourceID  `json:"resource_id,omitempty"`
	DisplayName   string            `json:"display_name"`
	Kind          model.RuntimeKind `json:"kind"`
	Endpoint      string            `json:"endpoint,omitempty"`
	CredentialRef string            `json:"credential_ref,omitempty"`
	TLSProfile    string            `json:"tls_profile,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Active        bool              `json:"active"`
}

func (payload runtimeTargetPayload) target(resourceID model.ResourceID) model.RuntimeTarget {
	return model.RuntimeTarget{
		ResourceMeta: model.ResourceMeta{ResourceID: resourceID}, DisplayName: payload.DisplayName,
		Kind: payload.Kind, Endpoint: payload.Endpoint, CredentialRef: payload.CredentialRef,
		TLSProfile: payload.TLSProfile, Labels: payload.Labels, Active: payload.Active,
	}
}

type workloadBindingPayload struct {
	ResourceID        model.ResourceID             `json:"resource_id,omitempty"`
	InstanceID        model.ResourceID             `json:"instance_id"`
	RuntimeTargetID   model.ResourceID             `json:"runtime_target_id"`
	RuntimeKind       model.RuntimeKind            `json:"runtime_kind"`
	HostNodeID        model.ResourceID             `json:"host_node_id,omitempty"`
	Docker            *model.DockerWorkloadRef     `json:"docker,omitempty"`
	Kubernetes        *model.KubernetesWorkloadRef `json:"kubernetes,omitempty"`
	ObservedRuntimeID string                       `json:"observed_runtime_id,omitempty"`
	Active            bool                         `json:"active"`
}

func (payload workloadBindingPayload) binding(resourceID model.ResourceID) model.WorkloadBinding {
	return model.WorkloadBinding{
		ResourceMeta: model.ResourceMeta{ResourceID: resourceID}, InstanceID: payload.InstanceID,
		RuntimeTargetID: payload.RuntimeTargetID, RuntimeKind: payload.RuntimeKind, HostNodeID: payload.HostNodeID,
		Docker: payload.Docker, Kubernetes: payload.Kubernetes, ObservedRuntimeID: payload.ObservedRuntimeID, Active: payload.Active,
	}
}

func (server *Server) runtimeTargetsCollection(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.store.RuntimeTargets()})
	case http.MethodPost:
		payload := runtimeTargetPayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid runtime target payload")
			return
		}
		target, err := server.store.PutRuntimeTarget(payload.target(payload.ResourceID))
		if err != nil {
			writeRuntimeInventoryError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]interface{}{"status": "ok", "result": target})
	default:
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (server *Server) runtimeTargetResource(writer http.ResponseWriter, request *http.Request, tail string) {
	if strings.Contains(tail, "/") || !model.ValidResourceID(model.ResourceID(tail)) {
		writeError(writer, http.StatusNotFound, "runtime target not found")
		return
	}
	resourceID := model.ResourceID(tail)
	switch request.Method {
	case http.MethodGet:
		target, found := server.store.RuntimeTarget(resourceID)
		if !found {
			writeError(writer, http.StatusNotFound, "runtime target not found")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": target})
	case http.MethodPut:
		if _, found := server.store.RuntimeTarget(resourceID); !found {
			writeError(writer, http.StatusNotFound, "runtime target not found")
			return
		}
		payload := runtimeTargetPayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid runtime target payload")
			return
		}
		if payload.ResourceID != "" && payload.ResourceID != resourceID {
			writeError(writer, http.StatusConflict, "runtime target identity is immutable")
			return
		}
		target, err := server.store.PutRuntimeTarget(payload.target(resourceID))
		if err != nil {
			writeRuntimeInventoryError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": target})
	default:
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (server *Server) workloadBindingsCollection(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		instanceID := model.ResourceID(strings.TrimSpace(request.URL.Query().Get("instance_id")))
		if instanceID != "" {
			if !model.ValidResourceID(instanceID) {
				writeError(writer, http.StatusBadRequest, "instance ID is invalid")
				return
			}
			binding, found := server.store.WorkloadBindingForInstance(instanceID)
			if !found {
				writeError(writer, http.StatusNotFound, "active workload binding not found")
				return
			}
			writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": binding})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.store.WorkloadBindings()})
	case http.MethodPost:
		payload := workloadBindingPayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid workload binding payload")
			return
		}
		binding, err := server.store.PutWorkloadBinding(payload.binding(payload.ResourceID))
		if err != nil {
			writeRuntimeInventoryError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]interface{}{"status": "ok", "result": binding})
	default:
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (server *Server) workloadBindingResource(writer http.ResponseWriter, request *http.Request, tail string) {
	if strings.Contains(tail, "/") || !model.ValidResourceID(model.ResourceID(tail)) {
		writeError(writer, http.StatusNotFound, "workload binding not found")
		return
	}
	resourceID := model.ResourceID(tail)
	switch request.Method {
	case http.MethodGet:
		binding, found := server.store.WorkloadBinding(resourceID)
		if !found {
			writeError(writer, http.StatusNotFound, "workload binding not found")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": binding})
	case http.MethodPut:
		if _, found := server.store.WorkloadBinding(resourceID); !found {
			writeError(writer, http.StatusNotFound, "workload binding not found")
			return
		}
		payload := workloadBindingPayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid workload binding payload")
			return
		}
		if payload.ResourceID != "" && payload.ResourceID != resourceID {
			writeError(writer, http.StatusConflict, "workload binding identity is immutable")
			return
		}
		binding, err := server.store.PutWorkloadBinding(payload.binding(resourceID))
		if err != nil {
			writeRuntimeInventoryError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": binding})
	default:
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func writeRuntimeInventoryError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrValidation):
		writeError(writer, http.StatusBadRequest, "runtime inventory is invalid")
	case errors.Is(err, store.ErrConflict):
		writeError(writer, http.StatusConflict, "runtime inventory conflicts with an existing resource")
	default:
		writeError(writer, http.StatusInternalServerError, "store runtime inventory failed")
	}
}
