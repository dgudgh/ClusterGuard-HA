package observability

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultErrorReminderInterval = 5 * time.Minute

// ErrorReminder suppresses identical background-loop errors while still
// reporting state changes and periodic reminders for unresolved conditions.
type ErrorReminder struct {
	mu          sync.Mutex
	repeatAfter time.Duration
	now         func() time.Time
	lastMessage string
	lastAt      time.Time
}

func NewErrorReminder(repeatAfter time.Duration, now func() time.Time) *ErrorReminder {
	if repeatAfter <= 0 {
		repeatAfter = defaultErrorReminderInterval
	}
	if now == nil {
		now = time.Now
	}
	return &ErrorReminder{repeatAfter: repeatAfter, now: now}
}

func (reminder *ErrorReminder) ShouldReport(err error) bool {
	if reminder == nil {
		return err != nil
	}
	reminder.mu.Lock()
	defer reminder.mu.Unlock()
	if err == nil {
		reminder.lastMessage = ""
		reminder.lastAt = time.Time{}
		return false
	}
	now := reminder.now().UTC()
	message := errorFingerprint(err)
	if message == reminder.lastMessage && !reminder.lastAt.IsZero() && !now.Before(reminder.lastAt) && now.Sub(reminder.lastAt) < reminder.repeatAfter {
		return false
	}
	reminder.lastMessage = message
	reminder.lastAt = now
	return true
}

func errorFingerprint(err error) string {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return err.Error()
	}
	messages := make([]string, 0, len(joined.Unwrap()))
	for _, nested := range joined.Unwrap() {
		if nested != nil {
			messages = append(messages, errorFingerprint(nested))
		}
	}
	if len(messages) == 0 {
		return err.Error()
	}
	sort.Strings(messages)
	return strings.Join(messages, "\n")
}
