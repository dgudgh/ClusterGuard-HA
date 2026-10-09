package platformupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Exercise the actual persisted status/events reader at each node boundary.
// Optional output feeds the real-browser polling regression with Manager results.
func TestHotfixStagingProgressAcrossThreeNodes(t *testing.T) {
	manager, _, id := preparedHotfixManager(t)
	at := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	type step struct {
		phase            string
		status           Status
		current, percent int
	}
	steps := []step{
		{"preparing", StatusRunning, 0, 8},
		{"staging", StatusRunning, 0, 15},
		{"updating", StatusRunning, 0, 15},
		{"updating", StatusRunning, 1, 38},
		{"staging", StatusRunning, 1, 38},
		{"updating", StatusRunning, 1, 38},
		{"updating", StatusRunning, 2, 61},
		{"staging", StatusRunning, 2, 61},
		{"updating", StatusRunning, 2, 61},
		{"updating", StatusRunning, 3, 85},
		{"finalizing", StatusRunning, 3, 92},
		{"completed", StatusSucceeded, 3, 100},
	}
	var records []PackageStatus
	var events []Event
	for i, s := range steps {
		node := "node-1"
		if i >= 4 {
			node = "node-2"
		}
		if i >= 7 {
			node = "node-3"
		}
		stamp := at.Add(time.Duration(i) * time.Second)
		// Runner's on-disk percent is node-count based; Manager must project phases.
		job := Job{PatchID: id, OperationID: "hotfix-progress-run", Mode: ModeExecute,
			Status: s.status, Node: node, StartedAt: at, UpdatedAt: stamp,
			MaintenanceActive: s.status == StatusRunning,
			Progress:          Progress{Phase: s.phase, Current: s.current, Total: 3, Percent: s.current * 100 / 3}}
		writeJobForTest(t, manager.config.RootDirectory, job)
		eventStatus := string(s.status)
		if i == 3 || i == 6 || i == 9 {
			eventStatus = "verified"
		} else if s.phase == "finalizing" {
			eventStatus = "finalizing"
		}
		events = append(events, Event{PatchID: id, OperationID: job.OperationID, Mode: job.Mode,
			Status: eventStatus, Node: node, Phase: s.phase, Current: s.current, Total: 3, UpdatedAt: stamp})
		var bytes []byte
		for _, event := range events {
			b, _ := json.Marshal(event)
			bytes = append(bytes, b...)
			bytes = append(bytes, '\n')
		}
		if err := os.WriteFile(filepath.Join(manager.config.RootDirectory, id, eventsFileName), bytes, 0600); err != nil {
			t.Fatal(err)
		}
		actual, found := manager.Job(id)
		if !found {
			t.Fatal("missing job")
		}
		softwarePackage, _ := manager.Package(id)
		records = append(records, PackageStatus{Package: softwarePackage, Job: &actual})
		if actual.Progress.Percent != s.percent {
			t.Errorf("step %d %s node-count %d: percent=%d, want %d", i, s.phase, s.current, actual.Progress.Percent, s.percent)
		}
	}
	if out := os.Getenv("CG_STAGING_PROGRESS_FIXTURE"); out != "" {
		data, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
