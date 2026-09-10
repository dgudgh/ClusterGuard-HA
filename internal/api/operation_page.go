package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

type operationPageCursor struct {
	Version int                         `json:"v"`
	Filter  string                      `json:"filter"`
	Before  store.OperationPagePosition `json:"before"`
}

type operationPageItem struct {
	operationListItem
	IncidentAttemptCount int       `json:"incident_attempt_count"`
	IncidentFirstAt      time.Time `json:"incident_first_at"`
	ClusterLabel         string    `json:"cluster_label,omitempty"`
	SourceLabel          string    `json:"source_label,omitempty"`
	TargetLabel          string    `json:"target_label,omitempty"`
}

func (server *Server) operationLogPage(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	params := request.URL.Query()
	query := store.OperationPageQuery{ClusterID: clusterID, Limit: store.DefaultOperationPageSize,
		Kind: model.OperationKind(params.Get("kind")), Status: model.OperationStatus(params.Get("status")), Search: strings.TrimSpace(params.Get("q"))}
	if values, present := params["limit"]; present {
		limit, err := strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > store.MaximumOperationPageSize {
			writeError(writer, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		query.Limit = limit
	}
	filterJSON, _ := json.Marshal([]string{string(clusterID), string(query.Kind), string(query.Status), query.Search})
	digest := sha256.Sum256(filterJSON)
	filter := hex.EncodeToString(digest[:])
	if cursor := params.Get("cursor"); cursor != "" {
		if len(cursor) > 1024 {
			writeError(writer, http.StatusBadRequest, "invalid operation page cursor")
			return
		}
		var decoded operationPageCursor
		bytes, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(bytes, &decoded) != nil || decoded.Version != 1 || decoded.Filter != filter || !model.ValidResourceID(decoded.Before.ID) {
			writeError(writer, http.StatusBadRequest, "invalid operation page cursor or changed filters")
			return
		}
		query.Before = &decoded.Before
	}
	page, err := server.store.OperationLogPage(query)
	if err != nil {
		server.writeOperationStoreError(writer, err)
		return
	}
	items := make([]operationPageItem, 0, len(page.Items))
	for _, entry := range page.Items {
		items = append(items, operationPageItem{operationListItem: summarizeOperation(entry.Record),
			IncidentAttemptCount: entry.Attempts, IncidentFirstAt: entry.FirstAt,
			ClusterLabel: entry.ClusterLabel, SourceLabel: entry.SourceLabel, TargetLabel: entry.TargetLabel})
	}
	var nextCursor string
	if page.Next != nil {
		encoded, _ := json.Marshal(operationPageCursor{Version: 1, Filter: filter, Before: *page.Next})
		nextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	writeDiagnosticJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
		"view": "page", "items": items, "total": page.Total, "record_count": page.RecordCount,
		"limit": query.Limit, "remaining": page.Remaining, "next_cursor": nextCursor,
	}})
}
