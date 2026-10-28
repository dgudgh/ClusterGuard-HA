package api

import (
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

var databaseEndpointHostname = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)

func (server *Server) registerDatabaseEndpoints(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := server.store.Cluster(clusterID); !ok {
		writeError(writer, http.StatusNotFound, "cluster not found")
		return
	}
	var payload struct {
		Endpoints []struct {
			Hostname  string `json:"hostname"`
			IPAddress string `json:"ip_address"`
			Port      int    `json:"port"`
		} `json:"endpoints"`
	}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid database endpoint registration")
		return
	}
	endpoints := make([]model.Endpoint, 0, len(payload.Endpoints))
	for _, value := range payload.Endpoints {
		host, ip := strings.TrimSpace(value.Hostname), strings.TrimSpace(value.IPAddress)
		if (host != "" && !databaseEndpointHostname.MatchString(host)) || (ip != "" && net.ParseIP(ip) == nil) {
			writeError(writer, http.StatusBadRequest, "database endpoint hostname or IP is invalid")
			return
		}
		endpoints = append(endpoints, model.Endpoint{Hostname: host, IPAddress: ip, Port: value.Port})
	}
	actor := "service-api"
	if auth, ok := requestAuthentication(request); ok {
		actor = auth.principal.Username
	}
	registered, err := server.store.RegisterDatabaseEndpoints(clusterID, endpoints, actor)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, store.ErrValidation):
			status = http.StatusBadRequest
		case errors.Is(err, store.ErrConflict):
			status = http.StatusConflict
		case errors.Is(err, store.ErrNotFound):
			status = http.StatusNotFound
		}
		writeError(writer, status, "database endpoint registration failed")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"endpoints": registered}})
}
