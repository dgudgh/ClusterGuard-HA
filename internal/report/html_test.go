package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestHTMLReportEscapesEvidenceAndIncludesVerification(t *testing.T) {
	operationID := model.NewResourceID()
	sourceID := model.NewResourceID()
	targetID := model.NewResourceID()
	data := Data{
		Report: model.Report{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), CreatedAt: time.Now().UTC()}, OperationID: operationID, Title: `<script>alert("title")</script>`, Status: model.OperationSucceeded, Summary: "verified"},
		Operation: model.OperationRecord{
			ResourceMeta: model.ResourceMeta{ResourceID: operationID},
			Operation:    model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: operationID}, ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba"},
			Plan:         model.OperationPlan{SourceID: sourceID, TargetID: targetID},
			Verification: model.Verification{Passed: true, Checks: []model.Check{{Name: "single_writer", Status: model.CheckPass, Message: "one writer"}}},
		},
		Audits: []model.AuditEvent{{OperationID: operationID, Stage: model.StageExecute, Message: `<img src=x onerror=alert(1)>`}},
	}
	var output bytes.Buffer
	if err := RenderHTML(&output, data); err != nil {
		t.Fatalf("render HTML report: %v", err)
	}
	html := output.String()
	for _, expected := range []string{string(operationID), string(sourceID), string(targetID), "single_writer", "one writer", "<details", "<summary>原始证据</summary>"} {
		if !strings.Contains(html, expected) {
			t.Fatalf("HTML report missing %q: %s", expected, html)
		}
	}
	for _, unsafe := range []string{`<script>alert`, `<img src=x`} {
		if strings.Contains(html, unsafe) {
			t.Fatalf("HTML report did not escape %q: %s", unsafe, html)
		}
	}
	if strings.Contains(html, "<details open") {
		t.Fatal("raw evidence must be collapsed by default")
	}
}
