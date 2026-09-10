package store

import (
	"clusterguard.io/ha/pkg/model"
	"clusterguard.io/ha/pkg/redact"
)

func redactAudit(event model.AuditEvent) model.AuditEvent {
	event.Message = redact.Text(event.Message)
	event.Actor = redact.Text(event.Actor)
	return event
}

func redactReport(report model.Report) model.Report {
	report.Title = redact.Text(report.Title)
	report.Summary = redact.Text(report.Summary)
	return report
}
