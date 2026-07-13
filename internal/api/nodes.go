package api

import (
	"errors"
	"net/http"
	"strings"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

type nodePayload struct {
	ResourceID  model.ResourceID `json:"resource_id,omitempty"`
	NodeName    string           `json:"node_name"`
	DisplayName string           `json:"display_name,omitempty"`
	Hostname    string           `json:"hostname,omitempty"`
	IPAddress   string           `json:"ip_address,omitempty"`
	Kind        model.NodeKind   `json:"kind"`
	HostClass   string           `json:"host_class,omitempty"`
	Active      bool             `json:"active"`
}

func (payload nodePayload) node(resourceID model.ResourceID) model.DatabaseNode {
	return model.DatabaseNode{
		ResourceMeta: model.ResourceMeta{ResourceID: resourceID}, NodeName: payload.NodeName,
		DisplayName: payload.DisplayName, Hostname: payload.Hostname, IPAddress: payload.IPAddress,
		Kind: payload.Kind, HostClass: payload.HostClass, Active: payload.Active,
	}
}

func (server *Server) nodesCollection(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.store.Nodes()})
	case http.MethodPost:
		payload := nodePayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid node payload")
			return
		}
		node, err := server.store.PutNode(payload.node(payload.ResourceID))
		if err != nil {
			writeNodeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]interface{}{"status": "ok", "result": node})
	default:
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (server *Server) nodeResource(writer http.ResponseWriter, request *http.Request, tail string) {
	if strings.Contains(tail, "/") || !model.ValidResourceID(model.ResourceID(tail)) {
		writeError(writer, http.StatusNotFound, "node not found")
		return
	}
	resourceID := model.ResourceID(tail)
	switch request.Method {
	case http.MethodGet:
		node, found := server.store.Node(resourceID)
		if !found {
			writeError(writer, http.StatusNotFound, "node not found")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": node})
	case http.MethodPut:
		if _, found := server.store.Node(resourceID); !found {
			writeError(writer, http.StatusNotFound, "node not found")
			return
		}
		payload := nodePayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid node payload")
			return
		}
		if payload.ResourceID != "" && payload.ResourceID != resourceID {
			writeError(writer, http.StatusConflict, "node resource identity is immutable")
			return
		}
		node, err := server.store.PutNode(payload.node(resourceID))
		if err != nil {
			writeNodeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": node})
	default:
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func writeNodeError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrValidation):
		writeError(writer, http.StatusBadRequest, "invalid node inventory")
	case errors.Is(err, store.ErrConflict):
		writeError(writer, http.StatusConflict, "node inventory conflicts with an existing resource")
	default:
		writeError(writer, http.StatusInternalServerError, "store node inventory failed")
	}
}
