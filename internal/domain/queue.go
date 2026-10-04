package domain

import (
	"sort"
	"strings"
	"time"
)

const (
	DefaultPriority = 0
	MinPriority     = -1000
	MaxPriority     = 1000
)

// InitialQueueBasis is recorded when a pending command joins the review
// queue at creation time.
const InitialQueueBasis = "initial enqueue; submission FIFO order"

// PriorityChange is one immutable entry in a queued command's priority
// history. Adjustments only append facts; they never rewrite earlier ones.
type PriorityChange struct {
	Priority  int
	Basis     string
	ChangedBy string
	ChangedAt time.Time
}

// QueueEntry is one pending command waiting in a gate's review queue.
type QueueEntry struct {
	CommandID  string
	Priority   int
	Sequence   uint64
	EnqueuedAt time.Time
	History    []PriorityChange
}

// Basis returns the latest recorded priority basis.
func (e QueueEntry) Basis() string {
	if len(e.History) == 0 {
		return ""
	}
	return e.History[len(e.History)-1].Basis
}

// ChangedBy returns the principal that set the current priority.
func (e QueueEntry) ChangedBy() string {
	if len(e.History) == 0 {
		return ""
	}
	return e.History[len(e.History)-1].ChangedBy
}

// ReviewQueue holds the pending-review commands of a single gate. The head
// is the entry with the highest priority; ties keep the original FIFO
// order given by Sequence.
type ReviewQueue struct {
	GateID       string
	entries      []QueueEntry
	nextSequence uint64
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func NewReviewQueue(gateID string, now time.Time) ReviewQueue {
	return ReviewQueue{
		GateID:       strings.TrimSpace(gateID),
		nextSequence: 1,
		Version:      1,
		CreatedAt:    now.UTC(),
		UpdatedAt:    now.UTC(),
	}
}

// Enqueue appends a pending command to the tail of the queue. A command
// that is already queued is rejected without touching its existing entry,
// so duplicate enqueue attempts cannot rewrite position or priority.
func (q *ReviewQueue) Enqueue(
	commandID string,
	priority int,
	basis string,
	changedBy string,
	now time.Time,
) (QueueEntry, error) {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return QueueEntry{}, Invalid("invalid_command_reference", "command id is required")
	}
	if q.Contains(commandID) {
		return QueueEntry{}, Conflict("command_already_queued", "command is already waiting in the review queue")
	}
	if err := validatePriority(priority); err != nil {
		return QueueEntry{}, err
	}
	basis = strings.TrimSpace(basis)
	if basis == "" {
		return QueueEntry{}, Invalid("invalid_priority_basis", "priority basis is required")
	}
	changedBy = strings.TrimSpace(changedBy)
	if changedBy == "" {
		return QueueEntry{}, Invalid("invalid_priority_changed_by", "changed_by is required")
	}

	entry := QueueEntry{
		CommandID:  commandID,
		Priority:   priority,
		Sequence:   q.nextSequence,
		EnqueuedAt: now.UTC(),
		History: []PriorityChange{{
			Priority:  priority,
			Basis:     basis,
			ChangedBy: changedBy,
			ChangedAt: now.UTC(),
		}},
	}
	q.entries = append(q.entries, entry)
	q.nextSequence++
	q.Version++
	q.UpdatedAt = now.UTC()
	return cloneEntry(entry), nil
}

func (q *ReviewQueue) Contains(commandID string) bool {
	for _, entry := range q.entries {
		if entry.CommandID == commandID {
			return true
		}
	}
	return false
}

// Entries returns the queue in effective review order (highest priority
// first, FIFO sequence breaking ties) as an independent copy.
func (q *ReviewQueue) Entries() []QueueEntry {
	ordered := make([]QueueEntry, len(q.entries))
	copy(ordered, q.entries)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority > ordered[j].Priority
		}
		return ordered[i].Sequence < ordered[j].Sequence
	})
	for i := range ordered {
		ordered[i] = cloneEntry(ordered[i])
	}
	return ordered
}

// Head returns the entry currently allowed to enter review, if any.
func (q *ReviewQueue) Head() (QueueEntry, bool) {
	ordered := q.Entries()
	if len(ordered) == 0 {
		return QueueEntry{}, false
	}
	return ordered[0], true
}

// Reprioritize appends a new priority fact to a queued command. Repeating
// the exact current priority and basis is a no-op: it neither appends a
// duplicate record nor bumps the queue version. Commands that have left
// the queue cannot be adjusted through it.
func (q *ReviewQueue) Reprioritize(
	commandID string,
	priority int,
	basis string,
	changedBy string,
	now time.Time,
) (QueueEntry, error) {
	commandID = strings.TrimSpace(commandID)
	if err := validatePriority(priority); err != nil {
		return QueueEntry{}, err
	}
	basis = strings.TrimSpace(basis)
	if basis == "" {
		return QueueEntry{}, Invalid("invalid_priority_basis", "priority basis is required")
	}
	changedBy = strings.TrimSpace(changedBy)
	if changedBy == "" {
		return QueueEntry{}, Invalid("invalid_priority_changed_by", "changed_by is required")
	}

	for i := range q.entries {
		if q.entries[i].CommandID != commandID {
			continue
		}
		latest := q.entries[i].History[len(q.entries[i].History)-1]
		if latest.Priority == priority && latest.Basis == basis {
			return cloneEntry(q.entries[i]), nil
		}
		q.entries[i].Priority = priority
		q.entries[i].History = append(q.entries[i].History, PriorityChange{
			Priority:  priority,
			Basis:     basis,
			ChangedBy: changedBy,
			ChangedAt: now.UTC(),
		})
		q.Version++
		q.UpdatedAt = now.UTC()
		return cloneEntry(q.entries[i]), nil
	}
	return QueueEntry{}, Conflict("command_not_queued", "command is not waiting in the gate review queue")
}

// Remove drops a command from the queue after its review has been
// committed. It reports whether the command was present.
func (q *ReviewQueue) Remove(commandID string, now time.Time) bool {
	for i, entry := range q.entries {
		if entry.CommandID != commandID {
			continue
		}
		q.entries = append(q.entries[:i], q.entries[i+1:]...)
		q.Version++
		q.UpdatedAt = now.UTC()
		return true
	}
	return false
}

// RetainPending drops entries whose commands no longer satisfy the
// pending precondition according to the store's current facts. The next
// head is therefore computed from real conditions rather than an assumed
// queue shape.
func (q *ReviewQueue) RetainPending(isPending func(commandID string) bool, now time.Time) {
	kept := q.entries[:0]
	changed := false
	for _, entry := range q.entries {
		if isPending(entry.CommandID) {
			kept = append(kept, entry)
			continue
		}
		changed = true
	}
	q.entries = kept
	if changed {
		q.Version++
		q.UpdatedAt = now.UTC()
	}
}

func validatePriority(priority int) error {
	if priority < MinPriority || priority > MaxPriority {
		return Invalid("invalid_priority", "priority must be between -1000 and 1000")
	}
	return nil
}

func cloneEntry(entry QueueEntry) QueueEntry {
	if entry.History != nil {
		entry.History = append([]PriorityChange(nil), entry.History...)
	}
	return entry
}
