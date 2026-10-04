package domain

import (
	"sort"
	"strings"
	"time"
)

// ReviewQueueEntry is one pending command waiting for its interlock review.
// Priority and basis are denormalized from the command at enqueue or
// reprioritize time; Sequence preserves arrival order as the tie-breaker.
type ReviewQueueEntry struct {
	CommandID     string
	Priority      int64
	PriorityBasis string
	QueuedAt      time.Time
	Sequence      int64
}

// ReviewQueue is the per-gate queue of commands awaiting interlock review.
// The head is always derived from the current entries: highest priority
// first, earliest arrival breaks ties. It is never frozen in advance.
type ReviewQueue struct {
	GateID       string
	Entries      []ReviewQueueEntry
	NextSequence int64
	UpdatedAt    time.Time
	Version      int64
}

func NewReviewQueue(gateID string, now time.Time) ReviewQueue {
	return ReviewQueue{
		GateID:       gateID,
		Entries:      []ReviewQueueEntry{},
		NextSequence: 1,
		UpdatedAt:    now.UTC(),
		Version:      1,
	}
}

func (q *ReviewQueue) Contains(commandID string) bool {
	for _, entry := range q.Entries {
		if entry.CommandID == commandID {
			return true
		}
	}
	return false
}

func (q *ReviewQueue) Enqueue(commandID string, priority int64, basis string, now time.Time) error {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return Invalid("invalid_queue_command", "command id is required to enter the review queue")
	}
	if q.Contains(commandID) {
		return Conflict("command_already_queued", "command is already waiting in the review queue")
	}

	q.Entries = append(q.Entries, ReviewQueueEntry{
		CommandID:     commandID,
		Priority:      priority,
		PriorityBasis: strings.TrimSpace(basis),
		QueuedAt:      now.UTC(),
		Sequence:      q.NextSequence,
	})
	q.NextSequence++
	q.touch(now)
	return nil
}

func (q *ReviewQueue) Reprioritize(commandID string, priority int64, basis string, now time.Time) error {
	for i := range q.Entries {
		if q.Entries[i].CommandID == commandID {
			q.Entries[i].Priority = priority
			q.Entries[i].PriorityBasis = strings.TrimSpace(basis)
			q.touch(now)
			return nil
		}
	}
	return Conflict("command_not_queued", "command is not waiting in the review queue")
}

func (q *ReviewQueue) Dequeue(commandID string, now time.Time) error {
	for i := range q.Entries {
		if q.Entries[i].CommandID == commandID {
			q.Entries = append(q.Entries[:i], q.Entries[i+1:]...)
			q.touch(now)
			return nil
		}
	}
	return Conflict("command_not_queued", "command is not waiting in the review queue")
}

// Ordered returns the entries in review order: higher priority first,
// earlier arrival breaks ties. The stored order is left untouched.
func (q *ReviewQueue) Ordered() []ReviewQueueEntry {
	ordered := make([]ReviewQueueEntry, len(q.Entries))
	copy(ordered, q.Entries)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority > ordered[j].Priority
		}
		return ordered[i].Sequence < ordered[j].Sequence
	})
	return ordered
}

// Head returns the entry that is currently allowed to enter review.
func (q *ReviewQueue) Head() (ReviewQueueEntry, bool) {
	ordered := q.Ordered()
	if len(ordered) == 0 {
		return ReviewQueueEntry{}, false
	}
	return ordered[0], true
}

func (q *ReviewQueue) touch(now time.Time) {
	q.UpdatedAt = now.UTC()
	q.Version++
}
