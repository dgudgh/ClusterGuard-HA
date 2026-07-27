package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

func TestCompactSnapshotBoundsHistoryWithoutDroppingActiveWork(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	value := emptySnapshot()
	runningID := model.NewResourceID()
	value.Operations[runningID] = model.OperationRecord{
		ResourceMeta: model.ResourceMeta{ResourceID: runningID, UpdatedAt: now.Add(-24 * time.Hour)},
		Status:       model.OperationRunning,
	}
	value.OperationKeys["running"] = runningID
	const expectedPlannedHistory = 128
	for index := 0; index < expectedPlannedHistory+2; index++ {
		resourceID := model.NewResourceID()
		value.Operations[resourceID] = model.OperationRecord{
			ResourceMeta: model.ResourceMeta{ResourceID: resourceID, UpdatedAt: now.Add(time.Duration(index) * time.Second)},
			Status:       model.OperationPlanned,
		}
		value.OperationKeys[fmt.Sprintf("planned-%d", index)] = resourceID
	}
	for index := 0; index < maximumTerminalOperations+2; index++ {
		resourceID := model.NewResourceID()
		value.Operations[resourceID] = model.OperationRecord{
			ResourceMeta: model.ResourceMeta{ResourceID: resourceID, UpdatedAt: now.Add(time.Duration(index) * time.Second)},
			Status:       model.OperationSucceeded,
		}
		value.OperationKeys[fmt.Sprintf("terminal-%d", index)] = resourceID
	}
	activeTaskID := model.NewResourceID()
	value.LifecycleTasks[activeTaskID] = lifecycle.Task{
		ResourceMeta: model.ResourceMeta{ResourceID: activeTaskID, UpdatedAt: now.Add(-24 * time.Hour)},
		Status:       lifecycle.TaskRunning,
	}
	for index := 0; index < maximumTerminalLifecycleTasks+2; index++ {
		resourceID := model.NewResourceID()
		value.LifecycleTasks[resourceID] = lifecycle.Task{
			ResourceMeta: model.ResourceMeta{ResourceID: resourceID, UpdatedAt: now.Add(time.Duration(index) * time.Second)},
			Status:       lifecycle.TaskSucceeded,
		}
	}
	activeSessionID := model.NewResourceID()
	value.PlatformSessions[activeSessionID] = model.PlatformSession{
		ResourceMeta: model.ResourceMeta{ResourceID: activeSessionID},
		ExpiresAt:    now.Add(time.Hour),
	}
	expiredSessionID := model.NewResourceID()
	value.PlatformSessions[expiredSessionID] = model.PlatformSession{
		ResourceMeta: model.ResourceMeta{ResourceID: expiredSessionID, UpdatedAt: now.Add(-time.Hour)},
		ExpiresAt:    now.Add(-time.Second),
	}
	for index := 0; index < maximumTerminalPlatformSessions; index++ {
		resourceID := model.NewResourceID()
		value.PlatformSessions[resourceID] = model.PlatformSession{
			ResourceMeta: model.ResourceMeta{ResourceID: resourceID, UpdatedAt: now.Add(time.Duration(index) * time.Second)},
			ExpiresAt:    now.Add(-time.Second),
		}
	}

	compacted := compactSnapshotHistory(value, now)
	if _, found := compacted.Operations[runningID]; !found {
		t.Fatal("active operation was removed by retention")
	}
	if len(compacted.Operations) != maximumTerminalOperations+expectedPlannedHistory+1 {
		t.Fatalf("retained operations=%d", len(compacted.Operations))
	}
	if len(compacted.OperationKeys) != len(compacted.Operations) {
		t.Fatalf("operation keys=%d operations=%d", len(compacted.OperationKeys), len(compacted.Operations))
	}
	if _, found := compacted.LifecycleTasks[activeTaskID]; !found {
		t.Fatal("active lifecycle task was removed by retention")
	}
	if len(compacted.LifecycleTasks) != maximumTerminalLifecycleTasks+1 {
		t.Fatalf("retained lifecycle tasks=%d", len(compacted.LifecycleTasks))
	}
	if _, found := compacted.PlatformSessions[activeSessionID]; !found {
		t.Fatal("active session was removed by retention")
	}
	if _, found := compacted.PlatformSessions[expiredSessionID]; found {
		t.Fatal("oldest expired session was retained beyond the terminal history window")
	}
}

func TestRepositoryBoundsAuditAndReportHistory(t *testing.T) {
	repository := NewMemory()
	for index := 0; index < maximumAuditEvents+2; index++ {
		if err := repository.RecordAudit(model.AuditEvent{Message: fmt.Sprintf("audit-%d", index)}); err != nil {
			t.Fatalf("record audit %d: %v", index, err)
		}
	}
	if audits := repository.Audits(); len(audits) != maximumAuditEvents || audits[0].Message != "audit-2" {
		t.Fatalf("bounded audits=%d first=%q", len(audits), audits[0].Message)
	}
	for index := 0; index < maximumReports+2; index++ {
		if err := repository.RecordReport(model.Report{Title: fmt.Sprintf("report-%d", index), Status: model.OperationSucceeded}); err != nil {
			t.Fatalf("record report %d: %v", index, err)
		}
	}
	if reports := repository.Reports(); len(reports) != maximumReports || reports[0].Title != "report-2" {
		t.Fatalf("bounded reports=%d first=%q", len(reports), reports[0].Title)
	}
}

func TestCompactSnapshotBoundsSecurityEventHistory(t *testing.T) {
	value := emptySnapshot()
	for index := 0; index < maximumSecurityEvents+2; index++ {
		value.SecurityEvents = append(value.SecurityEvents, model.SecurityEvent{
			ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
			Message:      fmt.Sprintf("security-%d", index),
		})
	}
	compacted := compactSnapshotHistory(value, time.Now().UTC())
	if len(compacted.SecurityEvents) != maximumSecurityEvents || compacted.SecurityEvents[0].Message != "security-2" {
		t.Fatalf("bounded security events=%d first=%q", len(compacted.SecurityEvents), compacted.SecurityEvents[0].Message)
	}
}

func TestRepositoryRejectsOversizedAuditAndReportText(t *testing.T) {
	repository := NewMemory()
	if err := repository.RecordAudit(model.AuditEvent{Message: strings.Repeat("a", maximumAuditMessageLength+1)}); err == nil {
		t.Fatal("oversized audit message was accepted")
	}
	if err := repository.RecordReport(model.Report{
		Title: "report", Summary: strings.Repeat("r", maximumReportSummaryLength+1), Status: model.OperationSucceeded,
	}); err == nil {
		t.Fatal("oversized report summary was accepted")
	}
}

func TestRepositoryRejectsUnboundedActiveWork(t *testing.T) {
	repository := NewMemory()
	for index := 0; index < maximumActiveOperations; index++ {
		resourceID := model.NewResourceID()
		repository.snapshot.Operations[resourceID] = model.OperationRecord{
			ResourceMeta: model.ResourceMeta{ResourceID: resourceID}, Status: model.OperationRunning,
		}
	}
	_, _, err := repository.CreateOperation(model.OperationRecord{
		Operation: model.Operation{
			ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationSwitchover,
		},
		TargetID: model.NewResourceID(), IdempotencyKey: "active-capacity",
	})
	if err == nil || !strings.Contains(err.Error(), "active operation capacity") {
		t.Fatalf("active operation capacity error=%v", err)
	}

	for index := 0; index < maximumActiveLifecycleTasks; index++ {
		resourceID := model.NewResourceID()
		repository.snapshot.LifecycleTasks[resourceID] = lifecycle.Task{
			ResourceMeta: model.ResourceMeta{ResourceID: resourceID}, Status: lifecycle.TaskRunning,
		}
	}
	_, err = repository.PutLifecycleTask(lifecycle.Task{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		ClusterID:    model.NewResourceID(),
		Status:       lifecycle.TaskPlanned,
	})
	if err == nil || !strings.Contains(err.Error(), "active lifecycle task capacity") {
		t.Fatalf("active lifecycle task capacity error=%v", err)
	}
}

func TestOpenMigratesBoundedLegacyHistoryOnNextCommit(t *testing.T) {
	value := emptySnapshot()
	message := strings.Repeat("a", 1800)
	for index := 0; index < 10000; index++ {
		value.Audits = append(value.Audits, model.AuditEvent{
			ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
			Message:      message,
		})
	}
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode legacy snapshot: %v", err)
	}
	if len(contents) <= 16<<20 || len(contents) >= 64<<20 {
		t.Fatalf("legacy fixture size=%d", len(contents))
	}

	path := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open bounded legacy snapshot: %v", err)
	}
	if len(repository.Audits()) != 10000 {
		t.Fatalf("legacy audits=%d", len(repository.Audits()))
	}
	if err := repository.RecordAudit(model.AuditEvent{Message: "migration commit"}); err != nil {
		t.Fatalf("compact legacy snapshot: %v", err)
	}
	if len(repository.Audits()) != maximumAuditEvents {
		t.Fatalf("compacted audits=%d", len(repository.Audits()))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat compacted snapshot: %v", err)
	}
	if info.Size() >= 16<<20 {
		t.Fatalf("compacted snapshot bytes=%d", info.Size())
	}
}
