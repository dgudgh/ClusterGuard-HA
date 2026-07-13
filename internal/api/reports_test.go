package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

func TestReportJSONAndHTMLRequireControlAuthAndRenderTimeline(t *testing.T) {
	server, repository := newTestServer(t)
	created, _, err := repository.CreateOperation(model.OperationRecord{
		Operation: model.Operation{ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba"},
		TargetID:  model.NewResourceID(), IdempotencyKey: "report-api-test",
	})
	if err != nil {
		t.Fatalf("create report operation: %v", err)
	}
	if err := repository.RecordAudit(model.AuditEvent{OperationID: created.ResourceID, Stage: model.StageExecute, Actor: "dba", Message: "source fenced"}); err != nil {
		t.Fatalf("record report audit: %v", err)
	}
	if err := repository.RecordReport(model.Report{OperationID: created.ResourceID, Title: "MySQL 受控切换报告", Status: model.OperationSucceeded, Summary: "verified"}); err != nil {
		t.Fatalf("record report: %v", err)
	}
	reportID := repository.Reports()[0].ResourceID

	unauthorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/reports/"+string(reportID), nil)
	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, unauthorizedRequest)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous report status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	jsonResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/reports/"+string(reportID), nil)
	if jsonResponse.Code != http.StatusOK || !strings.Contains(jsonResponse.Body.String(), string(reportID)) || !strings.Contains(jsonResponse.Body.String(), "source fenced") {
		t.Fatalf("JSON report: %d %s", jsonResponse.Code, jsonResponse.Body.String())
	}
	htmlResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/reports/"+string(reportID)+"/html", nil)
	if htmlResponse.Code != http.StatusOK || !strings.Contains(htmlResponse.Header().Get("Content-Type"), "text/html") || !strings.Contains(htmlResponse.Body.String(), "MySQL 受控切换报告") || !strings.Contains(htmlResponse.Body.String(), "<details") || !strings.Contains(htmlResponse.Body.String(), "source fenced") {
		t.Fatalf("HTML report: %d %s", htmlResponse.Code, htmlResponse.Body.String())
	}
}

func TestLifecycleReportRendersWithoutGenericOperationRecord(t *testing.T) {
	server, repository := newTestServer(t)
	clusterID := model.NewResourceID()
	operationID := model.NewResourceID()
	targetID := model.NewResourceID()
	task, err := repository.PutLifecycleTask(lifecycle.Task{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		ClusterID:    clusterID,
		OperationID:  operationID,
		Status:       lifecycle.TaskSucceeded,
		Request: lifecycle.Request{
			ClusterID:   clusterID,
			Action:      lifecycle.ActionAdd,
			Donor:       lifecycle.Donor{InstanceID: model.NewResourceID()},
			RequestedBy: "dba",
		},
		Plan:   lifecycle.Plan{ClusterID: clusterID, Action: lifecycle.ActionAdd, Targets: []lifecycle.TargetPlan{{Target: lifecycle.Target{NodeID: targetID, NodeName: "cg-data-0004", Kind: model.NodeData}}}},
		Checks: []model.Check{{Name: "replication", Status: model.CheckPass, Message: "replica is following the primary"}},
	})
	if err != nil {
		t.Fatalf("store lifecycle task: %v", err)
	}
	if err := repository.RecordAudit(model.AuditEvent{OperationID: operationID, Stage: model.StageVerify, Actor: "dba", Message: "node sync verified"}); err != nil {
		t.Fatalf("record lifecycle audit: %v", err)
	}
	if err := repository.RecordReport(model.Report{OperationID: operationID, Title: "MySQL node lifecycle report", Status: model.OperationSucceeded, Summary: "verified"}); err != nil {
		t.Fatalf("record lifecycle report: %v", err)
	}
	reportID := repository.Reports()[0].ResourceID

	jsonResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/reports/"+string(reportID), nil)
	if jsonResponse.Code != http.StatusOK || !strings.Contains(jsonResponse.Body.String(), string(task.ResourceID)) || !strings.Contains(jsonResponse.Body.String(), "node sync verified") {
		t.Fatalf("lifecycle JSON report: %d %s", jsonResponse.Code, jsonResponse.Body.String())
	}
	htmlResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/reports/"+string(reportID)+"/html", nil)
	if htmlResponse.Code != http.StatusOK || !strings.Contains(htmlResponse.Body.String(), string(clusterID)) || !strings.Contains(htmlResponse.Body.String(), string(targetID)) || !strings.Contains(htmlResponse.Body.String(), "replica is following the primary") {
		t.Fatalf("lifecycle HTML report: %d %s", htmlResponse.Code, htmlResponse.Body.String())
	}
}
