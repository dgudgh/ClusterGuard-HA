package store

import (
	"encoding/json"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestAuditAndReportSecretsAreRedactedOnWriteAndLegacyRead(t *testing.T) {
	r := NewMemory()
	event := model.AuditEvent{Message: `primary_conninfo='host=db password=never-disclose passfile=/private/replication'`}
	report := model.Report{Status: model.OperationFailed, Title: "failure", Summary: `password="never-disclose" token=private-token`}
	if err := r.RecordAudit(event); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordReport(report); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{r.snapshot.Audits, r.snapshot.Reports, r.Audits(), r.Reports()} {
		encoded, _ := json.Marshal(value)
		if strings.Contains(string(encoded), "never-disclose") || strings.Contains(string(encoded), "private-token") {
			t.Fatalf("secret leaked: %s", encoded)
		}
	}
	r.mu.Lock()
	r.snapshot.Audits = append(r.snapshot.Audits, event)
	r.snapshot.Reports = append(r.snapshot.Reports, report)
	r.mu.Unlock()
	for _, value := range []any{r.Audits(), r.Reports()} {
		encoded, _ := json.Marshal(value)
		if strings.Contains(string(encoded), "never-disclose") || strings.Contains(string(encoded), "private-token") {
			t.Fatalf("legacy diagnostic leaked: %s", encoded)
		}
	}
}
