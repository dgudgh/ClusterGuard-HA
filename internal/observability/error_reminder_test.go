package observability

import (
	"errors"
	"testing"
	"time"
)

func TestErrorReminderReportsChangesAndPeriodicReminders(t *testing.T) {
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	reminder := NewErrorReminder(5*time.Minute, func() time.Time { return now })

	if !reminder.ShouldReport(errors.New("ownership blocked")) {
		t.Fatal("first error was suppressed")
	}
	if reminder.ShouldReport(errors.New("ownership blocked")) {
		t.Fatal("duplicate error was not suppressed")
	}
	now = now.Add(4 * time.Minute)
	if reminder.ShouldReport(errors.New("ownership blocked")) {
		t.Fatal("duplicate error was reported before reminder interval")
	}
	now = now.Add(time.Minute)
	if !reminder.ShouldReport(errors.New("ownership blocked")) {
		t.Fatal("periodic reminder was suppressed")
	}
	if !reminder.ShouldReport(errors.New("ownership target changed")) {
		t.Fatal("changed error was suppressed")
	}
}

func TestErrorReminderResetsAfterRecovery(t *testing.T) {
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	reminder := NewErrorReminder(time.Hour, func() time.Time { return now })

	if !reminder.ShouldReport(errors.New("blocked")) {
		t.Fatal("first error was suppressed")
	}
	if reminder.ShouldReport(nil) {
		t.Fatal("successful cycle must not be reported as an error")
	}
	if !reminder.ShouldReport(errors.New("blocked")) {
		t.Fatal("same error after recovery was suppressed")
	}
}

func TestErrorReminderTreatsJoinedErrorsAsAnOrderIndependentCondition(t *testing.T) {
	reminder := NewErrorReminder(time.Hour, time.Now)
	if !reminder.ShouldReport(errors.Join(errors.New("cluster-a blocked"), errors.New("cluster-b blocked"))) {
		t.Fatal("first joined error was suppressed")
	}
	if reminder.ShouldReport(errors.Join(errors.New("cluster-b blocked"), errors.New("cluster-a blocked"))) {
		t.Fatal("joined error ordering bypassed duplicate suppression")
	}
}
