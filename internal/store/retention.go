package store

import (
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

const (
	maximumActivePlatformSessions   = 1024
	maximumTerminalPlatformSessions = 512
	maximumApprovalGrants           = 1024
	maximumActiveOperations         = 128
	maximumPlannedOperations        = 128
	maximumTerminalOperations       = 512
	maximumActiveLifecycleTasks     = 64
	maximumTerminalLifecycleTasks   = 256
	maximumAuditEvents              = 1024
	maximumReports                  = 512
	maximumSecurityEvents           = 2048
	maximumAuditActorLength         = 256
	maximumAuditMessageLength       = 2048
	maximumReportTitleLength        = 256
	maximumReportSummaryLength      = 4096
)

type retainedResource struct {
	resourceID model.ResourceID
	updatedAt  time.Time
}

func newestResourceIDs(resources []retainedResource, limit int) map[model.ResourceID]struct{} {
	sort.Slice(resources, func(left, right int) bool {
		if resources[left].updatedAt.Equal(resources[right].updatedAt) {
			return resources[left].resourceID > resources[right].resourceID
		}
		return resources[left].updatedAt.After(resources[right].updatedAt)
	})
	if len(resources) > limit {
		resources = resources[:limit]
	}
	result := make(map[model.ResourceID]struct{}, len(resources))
	for _, resource := range resources {
		result[resource.resourceID] = struct{}{}
	}
	return result
}

func compactSnapshotHistory(value snapshot, now time.Time) snapshot {
	originalSessions := value.PlatformSessions
	activeSessions := make([]retainedResource, 0, len(originalSessions))
	terminalSessions := make([]retainedResource, 0, len(originalSessions))
	for resourceID, session := range originalSessions {
		if session.RevokedAt.IsZero() && session.ExpiresAt.After(now) {
			activeSessions = append(activeSessions, retainedResource{resourceID: resourceID, updatedAt: session.UpdatedAt})
		} else {
			terminalSessions = append(terminalSessions, retainedResource{resourceID: resourceID, updatedAt: session.UpdatedAt})
		}
	}
	keptActiveSessions := newestResourceIDs(activeSessions, maximumActivePlatformSessions)
	keptTerminalSessions := newestResourceIDs(terminalSessions, maximumTerminalPlatformSessions)
	value.PlatformSessions = make(map[model.ResourceID]model.PlatformSession, len(keptActiveSessions)+len(keptTerminalSessions))
	for resourceID := range keptActiveSessions {
		value.PlatformSessions[resourceID] = originalSessions[resourceID]
	}
	for resourceID := range keptTerminalSessions {
		value.PlatformSessions[resourceID] = originalSessions[resourceID]
	}

	originalGrants := value.ApprovalGrants
	grantCandidates := make([]retainedResource, 0, len(originalGrants))
	for resourceID, grant := range originalGrants {
		grantCandidates = append(grantCandidates, retainedResource{resourceID: resourceID, updatedAt: grant.UpdatedAt})
	}
	keptGrants := newestResourceIDs(grantCandidates, maximumApprovalGrants)
	value.ApprovalGrants = make(map[model.ResourceID]model.ApprovalGrant, len(keptGrants))
	for resourceID := range keptGrants {
		value.ApprovalGrants[resourceID] = originalGrants[resourceID]
	}

	originalOperations := value.Operations
	originalOperationKeys := value.OperationKeys
	plannedOperations := make([]retainedResource, 0, len(originalOperations))
	terminalOperations := make([]retainedResource, 0, len(originalOperations))
	value.Operations = make(map[model.ResourceID]model.OperationRecord, len(originalOperations))
	for resourceID, operation := range originalOperations {
		if operation.Status == model.OperationPlanned {
			plannedOperations = append(plannedOperations, retainedResource{resourceID: resourceID, updatedAt: operation.UpdatedAt})
			continue
		}
		if terminalOperationStatus(operation.Status) {
			terminalOperations = append(terminalOperations, retainedResource{resourceID: resourceID, updatedAt: operation.UpdatedAt})
			continue
		}
		value.Operations[resourceID] = operation
	}
	for resourceID := range newestResourceIDs(plannedOperations, maximumPlannedOperations) {
		value.Operations[resourceID] = originalOperations[resourceID]
	}
	for resourceID := range newestResourceIDs(terminalOperations, maximumTerminalOperations) {
		value.Operations[resourceID] = originalOperations[resourceID]
	}
	value.OperationKeys = make(map[string]model.ResourceID, len(value.Operations))
	for key, resourceID := range originalOperationKeys {
		if _, found := value.Operations[resourceID]; found {
			value.OperationKeys[key] = resourceID
		}
	}
	for resourceID, operation := range value.Operations {
		if key := strings.TrimSpace(operation.IdempotencyKey); key != "" {
			value.OperationKeys[key] = resourceID
		}
	}

	originalTasks := value.LifecycleTasks
	terminalTasks := make([]retainedResource, 0, len(originalTasks))
	value.LifecycleTasks = make(map[model.ResourceID]lifecycle.Task, len(originalTasks))
	for resourceID, task := range originalTasks {
		switch task.Status {
		case lifecycle.TaskSucceeded, lifecycle.TaskFailed, lifecycle.TaskInterrupted, lifecycle.TaskIndeterminate:
			terminalTasks = append(terminalTasks, retainedResource{resourceID: resourceID, updatedAt: task.UpdatedAt})
		default:
			value.LifecycleTasks[resourceID] = task
		}
	}
	for resourceID := range newestResourceIDs(terminalTasks, maximumTerminalLifecycleTasks) {
		value.LifecycleTasks[resourceID] = originalTasks[resourceID]
	}

	if len(value.Audits) > maximumAuditEvents {
		value.Audits = append([]model.AuditEvent{}, value.Audits[len(value.Audits)-maximumAuditEvents:]...)
	} else {
		value.Audits = append([]model.AuditEvent{}, value.Audits...)
	}
	if len(value.Reports) > maximumReports {
		value.Reports = append([]model.Report{}, value.Reports[len(value.Reports)-maximumReports:]...)
	} else {
		value.Reports = append([]model.Report{}, value.Reports...)
	}
	if len(value.SecurityEvents) > maximumSecurityEvents {
		value.SecurityEvents = append([]model.SecurityEvent{}, value.SecurityEvents[len(value.SecurityEvents)-maximumSecurityEvents:]...)
	} else {
		value.SecurityEvents = append([]model.SecurityEvent{}, value.SecurityEvents...)
	}
	return value
}

func validateAuditText(event model.AuditEvent) error {
	if len(strings.TrimSpace(event.Actor)) > maximumAuditActorLength || len(strings.TrimSpace(event.Message)) > maximumAuditMessageLength {
		return validationError("audit actor or message exceeds maximum length")
	}
	return nil
}

func validateReportText(report model.Report) error {
	if len(strings.TrimSpace(report.Title)) > maximumReportTitleLength || len(strings.TrimSpace(report.Summary)) > maximumReportSummaryLength {
		return validationError("report title or summary exceeds maximum length")
	}
	return nil
}
