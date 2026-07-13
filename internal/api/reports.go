package api

import (
	"net/http"
	"strings"

	reportview "clusterguard.io/ha/internal/report"
	"clusterguard.io/ha/pkg/model"
)

func (server *Server) reportRoute(writer http.ResponseWriter, request *http.Request, tail string) {
	if !server.authorizeControl(writer, request) {
		return
	}
	tail = strings.Trim(strings.TrimSpace(tail), "/")
	if tail == "" {
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.store.Reports()})
		return
	}
	parts := strings.Split(tail, "/")
	if len(parts) > 2 || (len(parts) == 2 && parts[1] != "html") {
		writeError(writer, http.StatusNotFound, "report route not found")
		return
	}
	reportID := model.ResourceID(parts[0])
	if !model.ValidResourceID(reportID) {
		writeError(writer, http.StatusBadRequest, "report ID must be a platform UUID")
		return
	}
	report, found := server.store.Report(reportID)
	if !found {
		writeError(writer, http.StatusNotFound, "report not found")
		return
	}
	timeline, operationFound := server.store.OperationTimeline(report.OperationID)
	if operationFound {
		if len(parts) == 1 {
			writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
				"report": report, "operation": publicOperationRecord(timeline.Operation), "audits": timeline.Audits,
			}})
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Content-Disposition", `inline; filename="clusterguard-report.html"`)
		writer.WriteHeader(http.StatusOK)
		_ = reportview.RenderHTML(writer, reportview.Data{Report: report, Operation: timeline.Operation, Audits: timeline.Audits})
		return
	}

	task, lifecycleFound := server.store.LifecycleTaskByOperationID(report.OperationID)
	if !lifecycleFound {
		writeError(writer, http.StatusConflict, "report operation timeline is unavailable")
		return
	}
	audits := make([]model.AuditEvent, 0)
	for _, event := range server.store.Audits() {
		if event.OperationID == report.OperationID {
			audits = append(audits, event)
		}
	}
	if len(parts) == 1 {
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
			"report": report, "lifecycle_task": task, "audits": audits,
		}})
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Disposition", `inline; filename="clusterguard-report.html"`)
	writer.WriteHeader(http.StatusOK)
	_ = reportview.RenderHTML(writer, reportview.Data{Report: report, Lifecycle: &task, Audits: audits})
}
